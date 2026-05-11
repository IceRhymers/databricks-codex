package main

import (
	"log"
	"os"
	"path/filepath"
	"sync"

	"github.com/IceRhymers/databricks-codex/pkg/tomlconfig"
)

// ConfigManager coordinates config.toml patching with in-process locking.
type ConfigManager struct {
	config *tomlconfig.Manager
	mu     sync.Mutex
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
	}
}

// EnsureConfig is an idempotent config writer. It always invokes the
// surgical tomlconfig.Patch — which is itself idempotent for unchanged
// managed keys and additive for new ones — so calling EnsureConfig with
// the same proxy URL but different OTEL endpoints will correctly add or
// update the `[otel]` section without disturbing user content.
func (cm *ConfigManager) EnsureConfig(proxyURL, model string, modelExplicit bool, otelLogsEndpoint, otelMetricsEndpoint string) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if err := cm.config.Patch(tomlconfig.PatchConfig{
		ProxyURL:            proxyURL,
		Model:               model,
		ModelExplicit:       modelExplicit,
		OTELLogsEndpoint:    otelLogsEndpoint,
		OTELMetricsEndpoint: otelMetricsEndpoint,
	}); err != nil {
		return err
	}

	// Clean up any stale backup from pre-v0.6.0 crash recovery.
	os.Remove(cm.config.ConfigPath() + ".databricks-codex-backup")

	log.Printf("databricks-codex: ensured config.toml (proxy: %s)", proxyURL)
	return nil
}
