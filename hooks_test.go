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

// TestAtomicWriteFile_NoTmpDebris verifies the helper leaves no temp file
// behind after a successful write.
func TestAtomicWriteFile_NoTmpDebris(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "hooks.json")

	if err := atomicWriteFile(dest, []byte(`{"ok":true}`), 0o600); err != nil {
		t.Fatalf("atomicWriteFile: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		name := e.Name()
		if name == "hooks.json" {
			continue
		}
		t.Errorf("unexpected leftover file in dir: %s", name)
	}

	// Permissions preserved.
	info, err := os.Stat(dest)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("expected 0o600 perms, got %v", info.Mode().Perm())
	}
}

// TestAtomicWriteFile_PreservesOriginalOnRenameFailure verifies that if the
// rename fails (simulated by making the destination a directory), the
// original file at the destination path is untouched and no temp debris
// is left behind.
func TestAtomicWriteFile_PreservesOriginalOnFailure(t *testing.T) {
	dir := t.TempDir()
	// Create a directory at the destination path — os.Rename of a regular
	// file onto an existing non-empty directory fails on Linux, simulating
	// a write failure mid-operation.
	dest := filepath.Join(dir, "hooks.json")
	if err := os.Mkdir(dest, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Put a file inside so the dir is non-empty (rename will fail).
	if err := os.WriteFile(filepath.Join(dest, "marker"), []byte("orig"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	err := atomicWriteFile(dest, []byte("new content"), 0o600)
	if err == nil {
		t.Fatal("expected atomicWriteFile to fail when dest is a non-empty dir")
	}

	// The directory and its contents must still be intact.
	got, err := os.ReadFile(filepath.Join(dest, "marker"))
	if err != nil {
		t.Fatalf("original marker missing after failed write: %v", err)
	}
	if string(got) != "orig" {
		t.Errorf("original content corrupted: got %q", got)
	}

	// No .tmp debris left in dir.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.Name() == "hooks.json" {
			continue
		}
		t.Errorf("unexpected leftover file: %s", e.Name())
	}
}

// TestWriteHooksDoc_AtomicReplace verifies writeHooksDoc replaces an existing
// file without leaving temp debris and preserves the new content.
func TestWriteHooksDoc_AtomicReplace(t *testing.T) {
	dir := t.TempDir()
	hooksPath := filepath.Join(dir, "hooks.json")

	// Seed with original content.
	if err := writeHooksDoc(hooksPath, map[string]interface{}{"v": float64(1)}); err != nil {
		t.Fatalf("seed write: %v", err)
	}

	// Overwrite.
	if err := writeHooksDoc(hooksPath, map[string]interface{}{"v": float64(2)}); err != nil {
		t.Fatalf("rewrite: %v", err)
	}

	data, _ := os.ReadFile(hooksPath)
	var doc map[string]interface{}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if doc["v"].(float64) != 2 {
		t.Errorf("expected v=2 after atomic replace, got %v", doc["v"])
	}

	// No .tmp debris.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.Name() == "hooks.json" {
			continue
		}
		t.Errorf("unexpected leftover: %s", e.Name())
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
