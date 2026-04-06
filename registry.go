package main

import "github.com/IceRhymers/databricks-claude/pkg/registry"

type Session = registry.Session
type SessionRegistry = registry.SessionRegistry

func NewSessionRegistry(path string) *SessionRegistry {
	return registry.New(path)
}
