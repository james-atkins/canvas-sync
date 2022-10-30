package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/dustin/go-humanize"
	"github.com/schollz/progressbar/v3"
	"golang.org/x/sync/errgroup"
)

func listCourses(ctx context.Context, api *CanvasApi, coursesC chan<- []Course) error {
	errgrp, ctx := errgroup.WithContext(ctx)

	var worker func(url string) error
	worker = func(url string) error {
		courses, next, err := api.Courses(ctx, url)
		if err != nil {
			return err
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case coursesC <- courses:
		}

		if next != "" {
			// Spawn another worker for next page
			errgrp.Go(func() error { return worker(next) })
		}

		return nil
	}

	// Spawn worker for first page
	errgrp.Go(func() error { return worker(api.MakeCoursesUrl()) })

	if err := errgrp.Wait(); err != nil {
		return err
	}

	close(coursesC)
	return nil
}

func listFoldersInCourse(ctx context.Context, api *CanvasApi, foldersC chan<- []Folder, courseId uint64) error {
	errgrp, ctx := errgroup.WithContext(ctx)

	var worker func(url string) error
	worker = func(url string) error {
		folders, next, err := api.FoldersInCourse(ctx, url)
		if err != nil {
			return err
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case foldersC <- folders:
		}

		if next != "" {
			// Spawn another worker for next page
			errgrp.Go(func() error { return worker(next) })
		}

		return nil
	}

	// Spawn worker for first page
	errgrp.Go(func() error { return worker(api.MakeFoldersInCourseUrl(courseId)) })

	if err := errgrp.Wait(); err != nil {
		return err
	}

	close(foldersC)
	return nil
}

func listFilesInFolders(ctx context.Context, api *CanvasApi, folderC <-chan uint64, filesC chan<- []File) error {
	errgrp, ctx := errgroup.WithContext(ctx)

	var worker func(url string) error
	worker = func(url string) error {
		files, next, err := api.FilesInFolder(ctx, url)
		if err != nil {
			return err
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case filesC <- files:
		}

		if next != "" {
			errgrp.Go(func() error { return worker(next) })
		}
		return nil
	}

Loop:
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case folderId, more := <-folderC:
			if !more {
				break Loop
			}
			errgrp.Go(func() error { return worker(api.MakeFilesInFolderUrl(folderId)) })
		}
	}

	if err := errgrp.Wait(); err != nil {
		return err
	}

	close(filesC)
	return nil
}

func BuildTree(ctx context.Context, api *CanvasApi, course Course) (*CourseTree, error) {
	errgrp, ctx := errgroup.WithContext(ctx)

	n := 10
	foldersC := make(chan []Folder, n)
	filesC := make(chan []File, n)
	folderC := make(chan uint64, n)

	// As Canvas does not necessarily return the folders and files in order, collect them in a flat
	// slice first; and then create the tree structure.
	var flatFolders []Folder
	var flatFiles []File

	// Goroutine to construct the tree
	errgrp.Go(func() error {
	Loop:
		for {
			select {
			case <-ctx.Done():
				return ctx.Err()

			case folders, more := <-foldersC:
				if !more {
					close(folderC)
					foldersC = nil
				}

				for _, folder := range folders {
					flatFolders = append(flatFolders, folder)

					if folder.FilesCount > 0 {
						// Get information about the files in the folder
						select {
						case <-ctx.Done():
							return ctx.Err()
						case folderC <- folder.Id:
						}
					}
				}

			case files, more := <-filesC:
				if !more {
					break Loop
				}

				flatFiles = append(flatFiles, files...)
			}
		}

		return nil
	})

	errgrp.Go(func() error {
		return listFoldersInCourse(ctx, api, foldersC, course.Id)
	})

	errgrp.Go(func() error {
		return listFilesInFolders(ctx, api, folderC, filesC)
	})

	if err := errgrp.Wait(); err != nil {
		return nil, err
	}

	// Now create the tree structure
	tree, err := NewCourseTree(course, flatFolders, flatFiles)
	if err != nil {
		return nil, err
	}

	return tree, nil
}

type Config struct {
	Url            string   `json:"url"`
	Token          string   `json:"token"`
	Directory      string   `json:"directory"`
	IgnoredCourses []uint64 `json:"ignored_courses"`
}

type Statistics struct {
	FilesSynced      atomic.Uint64
	BytesTransferred atomic.Uint64
}

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, os.Interrupt)

	defer func() {
		signal.Stop(signalChan)
		cancel()
	}()

	go func() {
		// First signal
		select {
		case <-signalChan:
			log.Print("Exiting...")
			cancel()
		case <-ctx.Done():
			return
		}

		// Second signal
		select {
		case <-signalChan:
			os.Exit(1)
		case <-ctx.Done():
			return
		}
	}()

	if err := sync(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Print(err)
	}
}

func sync(ctx context.Context) error {
	homedir, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("cannot find home directory: %w", err)
	}

	content, err := os.ReadFile(filepath.Join(homedir, ".canvassync.json"))
	if err != nil {
		return fmt.Errorf("cannot open config file: %w", err)
	}

	var config Config
	if err := json.Unmarshal(content, &config); err != nil {
		return fmt.Errorf("invalid config file: %w", err)
	}

	api := &CanvasApi{
		Client:  http.DefaultClient,
		RootUrl: config.Url,
		Token:   config.Token,
	}

	errgrp, ctx := errgroup.WithContext(ctx)

	coursesC := make(chan []Course)

	errgrp.Go(func() error {
		return listCourses(ctx, api, coursesC)
	})

	treeC := make(chan *CourseTree)

	// Goroutine to loop through all the courses received on the coursesC channel and start
	// child goroutines to build course trees, and then send them to the treeC channel. When
	// finished, closes the treeC channel.
	errgrp.Go(func() error {
		errgrp, ctx := errgroup.WithContext(ctx)

	Loop:
		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case courses, more := <-coursesC:
				if !more {
					break Loop
				}
			CourseLoop:
				for _, course := range courses {
					// Skip ignored courses
					for _, ignoredCourseId := range config.IgnoredCourses {
						if course.Id == ignoredCourseId {
							continue CourseLoop
						}
					}

					course := course
					errgrp.Go(func() error {
						tree, err := BuildTree(ctx, api, course)
						if err != nil {
							return err
						}

						select {
						case <-ctx.Done():
							return ctx.Err()
						case treeC <- tree:
							return nil
						}
					})
				}
			}
		}

		if err := errgrp.Wait(); err != nil {
			return err
		}

		close(treeC)
		return nil
	})

	fileToSyncC := make(chan FileToSync)

	errgrp.Go(func() error {
		errgrp, ctx := errgroup.WithContext(ctx)

	Loop:
		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case tree, more := <-treeC:
				if !more {
					break Loop
				}
				errgrp.Go(func() error { return filesToSync(ctx, config.Directory, fileToSyncC, tree) })
			}
		}

		if err := errgrp.Wait(); err != nil {
			return err
		}

		close(fileToSyncC)
		return nil
	})

	progress := progressbar.NewOptions64(
		-1,
		progressbar.OptionSpinnerType(14),
		progressbar.OptionSetDescription(fmt.Sprintf("Syncing %s", config.Url)),
		progressbar.OptionSetWriter(os.Stderr),
		progressbar.OptionThrottle(20*time.Millisecond),
		progressbar.OptionShowCount(),
		progressbar.OptionShowIts(),
		progressbar.OptionSetItsString("files"),
		progressbar.OptionFullWidth(),
		progressbar.OptionUseANSICodes(true),
	)
	progress.RenderBlank()

	var stats Statistics

	const numDownloaders = 10

	for i := 0; i < numDownloaders; i++ {
		errgrp.Go(func() error {
			for {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case file, more := <-fileToSyncC:
					if !more {
						return nil
					}

					if err := downloadAndWriteFile(ctx, api, file); err != nil {
						return err
					}

					progress.Add(1)
					stats.FilesSynced.Add(1)
					stats.BytesTransferred.Add(uint64(file.File.Size))
				}
			}
		})
	}

	if err := errgrp.Wait(); err != nil {
		return err
	}

	if err := progress.Finish(); err != nil {
		return err
	}

	if stats.FilesSynced.Load() == 0 {
		fmt.Printf("✓ Up to date with %s.\n", config.Url)
	} else if stats.FilesSynced.Load() == 1 {
		fmt.Printf("✓ Transferred 1 file (%s) from %s.\n", humanize.Bytes(stats.BytesTransferred.Load()), config.Url)
	} else {
		fmt.Printf("✓ Transferred %d files (%s) from %s.\n", stats.FilesSynced.Load(), humanize.Bytes(stats.BytesTransferred.Load()), config.Url)
	}

	return nil
}
