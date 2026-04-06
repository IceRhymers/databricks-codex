package main

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
)

// persistentState is the JSON schema for ~/.codex/.databricks-codex.json.
// This file survives config.toml restore and persists across sessions.
type persistentState struct {
	Profile string `json:"profile,omitempty"`
}

// statePath returns the path to the persistent state file.
// It is a variable so tests can override it.
var statePath = func() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".codex/.databricks-codex.json"
	}
	return filepath.Join(home, ".codex", ".databricks-codex.json")
}

// loadState reads the persistent state file. Returns zero-value state if
// the file doesn't exist or can't be parsed.
func loadState() persistentState {
	data, err := os.ReadFile(statePath())
	if err != nil {
		return persistentState{}
	}
	var s persistentState
	if err := json.Unmarshal(data, &s); err != nil {
		log.Printf("databricks-codex: invalid state file, ignoring: %v", err)
		return persistentState{}
	}
	return s
}

// saveState writes the persistent state file atomically.
func saveState(s persistentState) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	p := statePath()
	dir := filepath.Dir(p)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}

	tmp, err := os.CreateTemp(dir, ".state-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return os.Rename(tmpPath, p)
}
