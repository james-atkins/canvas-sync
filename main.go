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

	"golang.org/x/sync/errgroup"
)

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

func BuildTree(ctx context.Context, api *CanvasApi, courseId uint64) error {
	errgrp, ctx := errgroup.WithContext(ctx)

	n := 10
	foldersC := make(chan []Folder, n)
	filesC := make(chan []File, n)
	folderC := make(chan uint64, n)

	course := &Course{
		Id: courseId,
	}

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
					course.AddFolder(folder)

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

				for _, file := range files {
					course.AddFile(file)
				}
			}
		}

		for _, folder := range course.Folders {
			fmt.Print(folder)
		}

		return nil
	})

	errgrp.Go(func() error {
		return listFoldersInCourse(ctx, api, foldersC, courseId)
	})

	errgrp.Go(func() error {
		return listFilesInFolders(ctx, api, folderC, filesC)
	})

	return errgrp.Wait()
}

type Config struct {
	Url            string   `json:"url"`
	Token          string   `json:"token"`
	Directory      string   `json:"directory"`
	IgnoredCourses []uint64 `json:"ignored_courses"`
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

	courses, _, err := api.Courses(ctx, api.MakeCoursesUrl())
	if err != nil {
		return err
	}

CourseLoop:
	for _, course := range courses {
		// Skip ignored courses
		for _, ignoredCourseId := range config.IgnoredCourses {
			if course.Id == ignoredCourseId {
				continue CourseLoop
			}
		}

		if err := BuildTree(ctx, api, course.Id); err != nil {
			return err
		}
	}

	return nil
}