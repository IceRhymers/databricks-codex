package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/IceRhymers/databricks-claude/pkg/headless"
	"github.com/IceRhymers/databricks-claude/pkg/refcount"
)

// headlessEnsure checks whether the proxy is healthy on the given port.
// If not, it starts a detached headless proxy and polls until ready (max 10s).
// Called by the SessionStart hook via: databricks-codex --headless-ensure
//
// The proxy shuts itself down via idle timeout — there is no corresponding
// release hook because Codex CLI has no session-end event.
func headlessEnsure(port int) {
	s := loadState()
	scheme := "http"
	if s.TLSCert != "" {
		scheme = "https"
	}
	headless.Ensure(headless.Config{
		Port:          port,
		Scheme:        scheme,
		TLSCert:       s.TLSCert,
		TLSKey:        s.TLSKey,
		ManagedEnvVar: "DATABRICKS_CODEX_MANAGED",
		LogPrefix:     "databricks-codex",
		RefcountPath:  refcount.PathForPort(".databricks-codex-sessions", port),
	})
}

// installHooks merges the databricks-codex SessionStart and Stop hooks into
// ~/.codex/hooks.json. Idempotent — safe to run after upgrades.
func installHooks(hooksPath string) error {
	doc, err := readHooksDoc(hooksPath)
	if err != nil {
		// File may not exist yet — start with an empty document.
		doc = map[string]interface{}{}
	}

	hooks, _ := doc["hooks"].(map[string]interface{})
	if hooks == nil {
		hooks = map[string]interface{}{}
	}

	// Remove any existing databricks-codex hooks before re-adding (idempotent).
	removeDBXHooks(hooks)

	// SessionStart hook — starts proxy if not already running.
	sessionStart, _ := hooks["SessionStart"].([]interface{})
	sessionStart = append(sessionStart, map[string]interface{}{
		"matcher": "startup",
		"hooks": []interface{}{
			map[string]interface{}{
				"type":    "command",
				"command": "databricks-codex --headless-ensure",
				"timeout": 15,
			},
		},
	})
	hooks["SessionStart"] = sessionStart

	doc["hooks"] = hooks
	if err := writeHooksDoc(hooksPath, doc); err != nil {
		return err
	}

	// Codex CLI requires [features] codex_hooks = true to read hooks.json.
	configPath := filepath.Join(filepath.Dir(hooksPath), "config.toml")
	return ensureHooksFeatureFlag(configPath)
}

// uninstallHooks removes the databricks-codex hooks from ~/.codex/hooks.json.
func uninstallHooks(hooksPath string) error {
	doc, err := readHooksDoc(hooksPath)
	if err != nil {
		return nil // nothing to remove
	}

	hooks, _ := doc["hooks"].(map[string]interface{})
	if hooks == nil {
		return nil
	}

	removeDBXHooks(hooks)

	// Clean up empty hook event keys.
	for k, v := range hooks {
		if arr, ok := v.([]interface{}); ok && len(arr) == 0 {
			delete(hooks, k)
		}
	}
	if len(hooks) == 0 {
		delete(doc, "hooks")
	} else {
		doc["hooks"] = hooks
	}

	return writeHooksDoc(hooksPath, doc)
}

// removeDBXHooks removes any hook entries whose command contains "databricks-codex --headless".
func removeDBXHooks(hooks map[string]interface{}) {
	for event, val := range hooks {
		arr, _ := val.([]interface{})
		filtered := arr[:0]
		for _, entry := range arr {
			if !isDBXHookEntry(entry) {
				filtered = append(filtered, entry)
			}
		}
		hooks[event] = filtered
	}
}

// isDBXHookEntry returns true if any nested hook command references databricks-codex --headless.
func isDBXHookEntry(entry interface{}) bool {
	m, ok := entry.(map[string]interface{})
	if !ok {
		return false
	}
	inner, _ := m["hooks"].([]interface{})
	for _, h := range inner {
		hm, _ := h.(map[string]interface{})
		if cmd, _ := hm["command"].(string); len(cmd) > 0 {
			if len(cmd) >= len("databricks-codex --headless") &&
				cmd[:len("databricks-codex --headless")] == "databricks-codex --headless" {
				return true
			}
		}
	}
	return false
}

// readHooksDoc reads and parses hooks.json, returning the full document.
func readHooksDoc(path string) (map[string]interface{}, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc map[string]interface{}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	return doc, nil
}

// writeHooksDoc writes a hooks document back to disk as indented JSON.
func writeHooksDoc(path string, doc map[string]interface{}) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("creating hooks dir: %w", err)
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("marshalling hooks: %w", err)
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o600)
}

// ensureHooksFeatureFlag ensures config.toml contains codex_hooks = true
// inside a [features] section. Surgical: if the key already exists it's a
// no-op; if [features] exists the key is appended inside it; otherwise a
// new section is appended at the end.
func ensureHooksFeatureFlag(configPath string) error {
	data, err := os.ReadFile(configPath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("reading config.toml: %w", err)
	}
	content := string(data)

	// Already enabled — nothing to do.
	if strings.Contains(content, "codex_hooks") {
		return nil
	}

	// Find the [features] section header.
	idx := strings.Index(content, "[features]")
	if idx >= 0 {
		// Insert the key right after the header line.
		end := strings.Index(content[idx:], "\n")
		if end < 0 {
			end = len(content[idx:])
		}
		insertAt := idx + end
		content = content[:insertAt] + "\ncodex_hooks = true" + content[insertAt:]
	} else {
		// Append a new [features] section.
		sep := "\n"
		if len(content) > 0 && !strings.HasSuffix(content, "\n") {
			sep = "\n\n"
		} else if len(content) > 0 {
			sep = "\n"
		}
		content += sep + "[features]\ncodex_hooks = true\n"
	}

	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		return fmt.Errorf("creating config dir: %w", err)
	}
	return os.WriteFile(configPath, []byte(content), 0o600)
}
