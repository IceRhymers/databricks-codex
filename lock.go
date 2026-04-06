package main

import "github.com/IceRhymers/databricks-codex/pkg/filelock"

type FileLock = filelock.FileLock

func NewFileLock(path string) *FileLock {
	return filelock.New(path)
}
