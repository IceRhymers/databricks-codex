package main

import (
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/IceRhymers/databricks-claude/pkg/filelock"
	"github.com/IceRhymers/databricks-codex/pkg/tomlconfig"
)

// ConfigManager coordinates config.toml patching and file locking —
// the Codex equivalent of databricks-claude's SettingsManager.
type ConfigManager struct {
	config *tomlconfig.Manager
	lock   *filelock.FileLock
}

// NewConfigManager creates a ConfigManager that manages ~/.codex/config.toml.
func NewConfigManager() *ConfigManager {
	home, err := os.UserHomeDir()
	if err != nil {
		log.Printf("databricks-codex: cannot determine home dir: %v", err)
		home = "."
	}
	codexDir := filepath.Join(home, ".codex")
	return &ConfigManager{
		config: tomlconfig.NewManager(filepath.Join(codexDir, "config.toml")),
		lock:   filelock.New(filepath.Join(codexDir, ".config.lock")),
	}
}

// EnsureConfig is an idempotent config writer. It reads ~/.codex/config.toml,
// checks if the model_providers.databricks-proxy base_url already equals proxyURL,
// and if so returns nil (no-op). Otherwise it patches the config.
// The config persists pointing at the fixed port permanently.
func (cm *ConfigManager) EnsureConfig(proxyURL, model string, modelExplicit bool, otelEndpoint string) error {
	if err := cm.lock.Lock(); err != nil {
		log.Printf("databricks-codex: config lock warning: %v", err)
	}
	defer cm.lock.Unlock()

	// Read existing config to check idempotency.
	existing, _ := os.ReadFile(cm.config.ConfigPath())
	if existing != nil {
		// Check if base_url already matches proxyURL.
		for _, line := range strings.Split(string(existing), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "base_url") && strings.Contains(trimmed, "=") {
				parts := strings.SplitN(trimmed, "=", 2)
				if len(parts) == 2 {
					val := strings.TrimSpace(parts[1])
					val = strings.Trim(val, `"`)
					if val == proxyURL {
						log.Printf("databricks-codex: config.toml already configured for %s", proxyURL)
						return nil
					}
				}
			}
		}
	}

	// Not yet configured — patch config.toml directly.
	// No backup/restore needed: the fixed-port design means EnsureConfig is
	// self-healing on next boot (same URL every time).
	if err := cm.config.Patch(tomlconfig.PatchConfig{
		ProxyURL:      proxyURL,
		Model:         model,
		ModelExplicit: modelExplicit,
		OTELEndpoint:  otelEndpoint,
	}); err != nil {
		return err
	}

	// Clean up any stale backup from pre-v0.6.0 crash recovery.
	os.Remove(cm.config.ConfigPath() + ".databricks-codex-backup")

	log.Printf("databricks-codex: wrote config.toml (proxy: %s)", proxyURL)
	return nil
}

