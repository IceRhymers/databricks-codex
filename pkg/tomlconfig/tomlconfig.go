// Package tomlconfig manages the Codex CLI config.toml file.
// It uses simple string-based manipulation rather than a full TOML parser,
// keeping the zero-external-dependency constraint.
package tomlconfig

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// PatchConfig holds the values to inject into config.toml.
type PatchConfig struct {
	ProxyURL     string // e.g., "http://127.0.0.1:54321"
	Model        string // e.g., "databricks-gpt-5-4"
	OTELEndpoint string // e.g., "http://127.0.0.1:54321/otel/v1/logs"; empty = no [otel] section
}

// Manager reads, patches, and restores the Codex config.toml file.
type Manager struct {
	configPath string
	backupPath string
	original   []byte // saved original content
}

// NewManager creates a new config.toml manager.
// configPath defaults to ~/.codex/config.toml if empty.
func NewManager(configPath string) *Manager {
	if configPath == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			log.Printf("databricks-codex: cannot determine home dir: %v", err)
			configPath = ".codex/config.toml"
		} else {
			configPath = filepath.Join(home, ".codex", "config.toml")
		}
	}
	return &Manager{
		configPath: configPath,
		backupPath: configPath + ".databricks-codex-backup",
	}
}

// ConfigPath returns the path to config.toml.
func (m *Manager) ConfigPath() string {
	return m.configPath
}

// Backup reads the current config.toml and saves the original content
// both in memory and to a backup file for crash recovery.
func (m *Manager) Backup() error {
	data, err := os.ReadFile(m.configPath)
	if err != nil {
		if os.IsNotExist(err) {
			// No config file yet — original is empty.
			m.original = nil
			return nil
		}
		return fmt.Errorf("read config.toml: %w", err)
	}
	m.original = data

	// Write backup file for crash recovery.
	if err := atomicWrite(m.backupPath, data); err != nil {
		return fmt.Errorf("write backup: %w", err)
	}
	return nil
}

// Patch writes a new config.toml that includes the proxy configuration
// while preserving user-specific sections (projects, notices, etc.).
func (m *Manager) Patch(cfg PatchConfig) error {
	// Extract user sections from the original config to preserve them.
	userSections := extractUserSections(m.original)

	// Build the new config.toml.
	var b strings.Builder
	b.WriteString("# Managed by databricks-codex — do not edit while wrapper is running.\n")
	b.WriteString("profile = \"databricks-proxy\"\n")
	b.WriteString("\n")

	// Write the proxy profile.
	b.WriteString("[profiles.databricks-proxy]\n")
	b.WriteString("model_provider = \"databricks-proxy\"\n")
	if cfg.Model != "" {
		b.WriteString(fmt.Sprintf("model = %q\n", cfg.Model))
	}
	b.WriteString("\n")

	// Write the model provider pointing at the local proxy.
	b.WriteString("[model_providers.databricks-proxy]\n")
	b.WriteString("name = \"Databricks Proxy\"\n")
	b.WriteString(fmt.Sprintf("base_url = %q\n", cfg.ProxyURL))
	b.WriteString("env_key = \"OPENAI_API_KEY\"\n")
	b.WriteString("wire_api = \"responses\"\n")
	b.WriteString("\n")

	// Write the OTEL exporter section when enabled.
	if cfg.OTELEndpoint != "" {
		b.WriteString("[otel]\n")
		b.WriteString("environment = \"production\"\n")
		b.WriteString(fmt.Sprintf("exporter = { otlp-http = { endpoint = %q, protocol = \"binary\" } }\n", cfg.OTELEndpoint))
		b.WriteString("\n")
	}

	// Preserve user sections.
	if userSections != "" {
		b.WriteString(userSections)
	}

	if err := atomicWrite(m.configPath, []byte(b.String())); err != nil {
		return fmt.Errorf("write patched config.toml: %w", err)
	}
	return nil
}

// Restore writes the original config.toml content back and removes the
// backup file. If original was nil (no config existed), the file is removed.
func (m *Manager) Restore() error {
	if m.original == nil {
		// Config didn't exist before — remove our managed version.
		os.Remove(m.configPath)
	} else {
		if err := atomicWrite(m.configPath, m.original); err != nil {
			return fmt.Errorf("restore config.toml: %w", err)
		}
	}
	os.Remove(m.backupPath) // Best-effort cleanup.
	return nil
}

// RestoreFromBackup recovers from a crash by restoring from the backup file.
// Returns false if no backup exists (clean state).
func (m *Manager) RestoreFromBackup() bool {
	data, err := os.ReadFile(m.backupPath)
	if err != nil {
		return false // No backup — nothing to recover.
	}
	log.Printf("databricks-codex: restoring config.toml from crash backup")
	m.original = data
	if err := m.Restore(); err != nil {
		log.Printf("databricks-codex: crash restore failed: %v", err)
	}
	return true
}

// UpdateProxyURL updates only the base_url in the managed config.toml.
// Used for multi-session handoff: when the current session exits,
// it updates the config to point at a surviving session's proxy.
func (m *Manager) UpdateProxyURL(newURL string) error {
	data, err := os.ReadFile(m.configPath)
	if err != nil {
		return fmt.Errorf("read config for proxy URL update: %w", err)
	}

	content := string(data)
	// Find and replace the base_url line in the databricks-proxy provider section.
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "base_url") && strings.Contains(trimmed, "=") {
			lines[i] = fmt.Sprintf("base_url = %q", newURL)
			break
		}
	}

	return atomicWrite(m.configPath, []byte(strings.Join(lines, "\n")))
}

// extractUserSections returns TOML sections from the original config that
// should be preserved (e.g., [projects.*], [notice.*]).
// It skips sections we manage: [profiles.*], [model_providers.*], and
// top-level keys like "profile".
func extractUserSections(original []byte) string {
	if len(original) == 0 {
		return ""
	}

	lines := strings.Split(string(original), "\n")
	var result strings.Builder
	inManagedSection := false
	inRootSection := true // true until we hit the first [section]

	managedSections := []string{
		"[profiles.databricks-proxy]",
		"[model_providers.databricks-proxy]",
		"[otel]",
	}
	managedRootKeys := []string{
		"profile",
	}

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Detect section headers.
		if strings.HasPrefix(trimmed, "[") {
			inRootSection = false
			inManagedSection = false
			for _, ms := range managedSections {
				if strings.HasPrefix(trimmed, ms) {
					inManagedSection = true
					break
				}
			}
			if !inManagedSection {
				result.WriteString(line)
				result.WriteString("\n")
			}
			continue
		}

		// Skip managed root keys.
		if inRootSection {
			isManaged := false
			for _, mk := range managedRootKeys {
				if strings.HasPrefix(trimmed, mk+" ") || strings.HasPrefix(trimmed, mk+"=") {
					isManaged = true
					break
				}
			}
			if isManaged || trimmed == "" || strings.HasPrefix(trimmed, "#") {
				continue
			}
			// Preserve non-managed root keys.
			result.WriteString(line)
			result.WriteString("\n")
			continue
		}

		// Skip lines in managed sections.
		if inManagedSection {
			continue
		}

		// Preserve all other lines.
		result.WriteString(line)
		result.WriteString("\n")
	}

	return result.String()
}

// atomicWrite writes data to a temp file and renames it into place.
func atomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".config-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if err := os.Chmod(tmpPath, 0o600); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return os.Rename(tmpPath, path)
}
