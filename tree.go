package main

import (
	"fmt"
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

type TreeFolder struct {
	Folder

	folders []*TreeFolder
	files   []*TreeFile
}

type TreeFile struct {
	File
}
