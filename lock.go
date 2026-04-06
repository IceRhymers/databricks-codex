package main

import "github.com/IceRhymers/databricks-claude/pkg/filelock"

type FileLock = filelock.FileLock

func NewFileLock(path string) *FileLock {
	return filelock.New(path)
}
