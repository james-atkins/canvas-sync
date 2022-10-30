package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"

	atomicFile "github.com/natefinch/atomic"

	"golang.org/x/sync/errgroup"
)

type CourseTree struct {
	Course

	root   *TreeFolder
	lookup map[uint64]*TreeFolder
}

func NewCourseTree(course Course, folders []Folder, files []File) (*CourseTree, error) {
	lookup := make(map[uint64]*TreeFolder)
	var root *TreeFolder

	for _, folder := range folders {
		lookup[folder.Id] = &TreeFolder{Folder: folder}
	}

	for _, folder := range lookup {
		if folder.ParentId == 0 {
			if root != nil {
				return nil, fmt.Errorf("root folder already exists")
			}

			root = folder
		} else {
			parent, ok := lookup[folder.ParentId]
			if !ok {
				return nil, fmt.Errorf("parent folder not found for %v", folder)
			}

			parent.folders = append(parent.folders, folder)
		}
	}

	for _, file := range files {
		lookup[file.FolderId].files = append(lookup[file.FolderId].files, &TreeFile{File: file})
	}

	tree := &CourseTree{
		Course: course,
		root:   root,
		lookup: lookup,
	}

	return tree, nil
}

func (tree *CourseTree) Traverse(callback func(folder *TreeFolder, level int) error) error {
	var f func(*TreeFolder, int) error
	f = func(folder *TreeFolder, level int) error {
		if err := callback(folder, level); err != nil {
			return err
		}

		for _, childFolder := range folder.folders {
			if err := f(childFolder, level+1); err != nil {
				return err
			}
		}

		return nil
	}

	return f(tree.root, 0)
}

func (tree *CourseTree) TraverseWithParents(callback func(folder *TreeFolder, parents []*TreeFolder) error) error {
	var f func(*TreeFolder, []*TreeFolder) error
	f = func(folder *TreeFolder, parents []*TreeFolder) error {
		if err := callback(folder, parents); err != nil {
			return err
		}

		for _, childFolder := range folder.folders {
			if err := f(childFolder, append(parents, folder)); err != nil {
				return err
			}
		}

		return nil
	}

	return f(tree.root, nil)
}

type TreeFolder struct {
	Folder

	folders []*TreeFolder
	files   []*TreeFile
}

type TreeFile struct {
	File
}

type FileToSync struct {
	File File
	Path string
}

// Traverse over a course tree and check whether the files and folders exist on the local disk in
// the directory tree at rootDirectory. Send files that do not exist or are not up-to-date with the
// copy on Canvas to the fileToSyncC channel.
// This does NOT close the fileToSyncC channel after exiting.
func filesToSync(ctx context.Context, rootDirectory string, fileToSyncC chan<- FileToSync, tree *CourseTree) error {
	var f func(folder *TreeFolder, pathElems []string, parentsNotOnDisk bool) error
	f = func(folder *TreeFolder, pathElems []string, parentsNotOnDisk bool) error {
		folderPath := filepath.Join(pathElems...)

		// Check whether this folder exists on the disk.
		// If the folder is not on the disk, then its files are not too and so we can speed up by
		// not checking for them. Furthermore, if one of a folder's parent folders are not on the
		// disk, then the folder cannot be either, and so we can avoid an unnecessary Stat.
		var folderNotOnDisk bool
		if parentsNotOnDisk {
			folderNotOnDisk = true
		} else {
			_, err := os.Stat(folderPath)
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			if errors.Is(err, os.ErrNotExist) {
				folderNotOnDisk = true
			}
		}

		for _, file := range folder.files {
			filePath := filepath.Join(folderPath, file.FileName)

			if !folderNotOnDisk {
				fi, err := os.Stat(filePath)
				if err != nil && !errors.Is(err, os.ErrNotExist) {
					return err
				}

				if err == nil && file.UpdatedAt.Equal(fi.ModTime()) && file.Size == fi.Size() {
					// The file exists on disk and is up-to-date with the copy on Canvas. No need
					// to download again.
					continue
				}
			}

			// File does not exist on disk or is not up-to-date with the copy on Canvas.
			select {
			case <-ctx.Done():
				return ctx.Err()
			case fileToSyncC <- FileToSync{File: file.File, Path: filePath}:
			}
		}

		for _, childFolder := range folder.folders {
			// Recurse
			if err := f(childFolder, append(pathElems, childFolder.Name), folderNotOnDisk); err != nil {
				return err
			}
		}

		return nil
	}

	// Start recursing from the root folder of the course tree
	err := f(tree.root, []string{rootDirectory, tree.Course.Name}, false)
	if err != nil {
		return err
	}

	return nil
}

func SyncTree(ctx context.Context, api *CanvasApi, tree *CourseTree, rootDirectory string) error {
	errgrp, ctx := errgroup.WithContext(ctx)

	fileToSyncC := make(chan FileToSync)

	errgrp.Go(func() error {
		if err := filesToSync(ctx, rootDirectory, fileToSyncC, tree); err != nil {
			return err
		}

		close(fileToSyncC)
		return nil
	})

	for i := 0; i < 10; i++ {
		errgrp.Go(func() error {
			for {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case file, more := <-fileToSyncC:
					if !more {
						return nil
					}

					err := func() error {
						if err := os.MkdirAll(filepath.Dir(file.Path), 0755); err != nil {
							return err
						}

						f, err := os.CreateTemp(filepath.Dir(file.Path), "canvassync")
						if err != nil {
							return err
						}
						defer func() {
							f.Close()
							os.Remove(f.Name())
						}()

						if err := api.DownloadFile(ctx, f, file.File.DownloadUrl); err != nil {
							return err
						}

						if err := os.Chtimes(f.Name(), file.File.UpdatedAt, file.File.UpdatedAt); err != nil {
							return err
						}

						if err := atomicFile.ReplaceFile(f.Name(), file.Path); err != nil {
							return err
						}

						log.Printf("Downloaded %s", file.Path)

						return nil
					}()
					if err != nil {
						return err
					}

				}
			}
		})
	}

	return errgrp.Wait()
}
