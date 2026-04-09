package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestHeadlessEnsure_SkipManaged verifies that headlessEnsure returns
// immediately when DATABRICKS_CODEX_MANAGED=1 is set, without attempting any
// network calls.
func TestHeadlessEnsure_SkipManaged(t *testing.T) {
	t.Setenv("DATABRICKS_CODEX_MANAGED", "1")
	// Should return immediately without error or network call.
	headlessEnsure(49154)
}

// TestInstallHooks_CreatesFile verifies installHooks creates hooks.json
// with the expected SessionStart hook.
func TestInstallHooks_CreatesFile(t *testing.T) {
	dir := t.TempDir()
	hooksPath := filepath.Join(dir, ".codex", "hooks.json")

	if err := installHooks(hooksPath); err != nil {
		t.Fatalf("installHooks: %v", err)
	}

	data, err := os.ReadFile(hooksPath)
	if err != nil {
		t.Fatalf("read hooks.json: %v", err)
	}

	var doc map[string]interface{}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parse hooks.json: %v", err)
	}

	hooks, _ := doc["hooks"].(map[string]interface{})
	if hooks == nil {
		t.Fatal("expected hooks key in document")
	}

	// Check SessionStart
	ss, _ := hooks["SessionStart"].([]interface{})
	if len(ss) != 1 {
		t.Fatalf("expected 1 SessionStart entry, got %d", len(ss))
	}
}

// TestInstallHooks_Idempotent verifies running installHooks twice doesn't duplicate.
func TestInstallHooks_Idempotent(t *testing.T) {
	dir := t.TempDir()
	hooksPath := filepath.Join(dir, ".codex", "hooks.json")

	if err := installHooks(hooksPath); err != nil {
		t.Fatalf("first installHooks: %v", err)
	}
	if err := installHooks(hooksPath); err != nil {
		t.Fatalf("second installHooks: %v", err)
	}

	data, err := os.ReadFile(hooksPath)
	if err != nil {
		t.Fatalf("read hooks.json: %v", err)
	}

	var doc map[string]interface{}
	json.Unmarshal(data, &doc)

	hooks := doc["hooks"].(map[string]interface{})
	ss := hooks["SessionStart"].([]interface{})
	if len(ss) != 1 {
		t.Errorf("expected 1 SessionStart entry after double install, got %d", len(ss))
	}
}

// TestUninstallHooks_RemovesEntries verifies uninstallHooks removes the hooks.
func TestUninstallHooks_RemovesEntries(t *testing.T) {
	dir := t.TempDir()
	hooksPath := filepath.Join(dir, ".codex", "hooks.json")

	if err := installHooks(hooksPath); err != nil {
		t.Fatalf("installHooks: %v", err)
	}
	if err := uninstallHooks(hooksPath); err != nil {
		t.Fatalf("uninstallHooks: %v", err)
	}

	data, err := os.ReadFile(hooksPath)
	if err != nil {
		t.Fatalf("read hooks.json: %v", err)
	}

	var doc map[string]interface{}
	json.Unmarshal(data, &doc)

	// Hooks key should be removed entirely (empty).
	if _, exists := doc["hooks"]; exists {
		t.Error("expected hooks key to be removed after uninstall")
	}
}

// TestUninstallHooks_PreservesOtherHooks verifies that uninstall only removes
// databricks-codex hooks, leaving other hooks intact.
func TestUninstallHooks_PreservesOtherHooks(t *testing.T) {
	dir := t.TempDir()
	hooksPath := filepath.Join(dir, ".codex", "hooks.json")

	// Create a hooks.json with a custom hook.
	os.MkdirAll(filepath.Dir(hooksPath), 0o700)
	initial := map[string]interface{}{
		"hooks": map[string]interface{}{
			"SessionStart": []interface{}{
				map[string]interface{}{
					"matcher": "startup",
					"hooks": []interface{}{
						map[string]interface{}{
							"type":    "command",
							"command": "my-custom-hook",
						},
					},
				},
			},
		},
	}
	data, _ := json.MarshalIndent(initial, "", "  ")
	os.WriteFile(hooksPath, data, 0o600)

	// Install then uninstall.
	installHooks(hooksPath)
	uninstallHooks(hooksPath)

	raw, _ := os.ReadFile(hooksPath)
	var doc map[string]interface{}
	json.Unmarshal(raw, &doc)

	hooks := doc["hooks"].(map[string]interface{})
	ss := hooks["SessionStart"].([]interface{})
	if len(ss) != 1 {
		t.Errorf("expected 1 custom SessionStart entry preserved, got %d", len(ss))
	}
}

// TestUninstallHooks_NoFile verifies uninstallHooks is a no-op when file doesn't exist.
func TestUninstallHooks_NoFile(t *testing.T) {
	dir := t.TempDir()
	hooksPath := filepath.Join(dir, "nonexistent", "hooks.json")

	if err := uninstallHooks(hooksPath); err != nil {
		t.Fatalf("uninstallHooks on missing file should return nil, got: %v", err)
	}
}

// TestIsDBXHookEntry verifies detection of databricks-codex hook entries.
func TestIsDBXHookEntry(t *testing.T) {
	tests := []struct {
		name string
		cmd  string
		want bool
	}{
		{"ensure command", "databricks-codex --headless-ensure", true},
		{"release command", "databricks-codex --headless-release", true},
		{"headless base", "databricks-codex --headless", true},
		{"unrelated command", "my-custom-hook", false},
		{"partial match", "databricks-codex --help", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			entry := map[string]interface{}{
				"hooks": []interface{}{
					map[string]interface{}{
						"type":    "command",
						"command": tc.cmd,
					},
				},
			}
			got := isDBXHookEntry(entry)
			if got != tc.want {
				t.Errorf("isDBXHookEntry(%q) = %v, want %v", tc.cmd, got, tc.want)
			}
		})
	}
}
