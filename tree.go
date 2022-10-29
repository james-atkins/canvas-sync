package main

import "fmt"

type CourseTree struct {
	Course

	Folders []TreeFolder
}

func (course *CourseTree) AddFolder(folder Folder) error {
	course.Folders = append(course.Folders, TreeFolder{Folder: folder})
	return nil
}

func (course *CourseTree) AddFile(file File) error {
	for i := range course.Folders {
		if file.FolderId == course.Folders[i].Id {
			course.Folders[i].Files = append(course.Folders[i].Files, TreeFile{File: file})
			return nil
		}
	}

	return fmt.Errorf("AddFile: could not find folder %d", file.FolderId)
}

type TreeFolder struct {
	Folder

	Folders []TreeFolder
	Files   []TreeFile
}

type TreeFile struct {
	File
}
