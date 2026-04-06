package main

import (
	"log"
	"os"
	"path/filepath"

	"github.com/IceRhymers/databricks-claude/pkg/filelock"
	"github.com/IceRhymers/databricks-claude/pkg/registry"
	"github.com/IceRhymers/databricks-codex/pkg/tomlconfig"
)

// ConfigManager coordinates config.toml patching, file locking, and
// multi-session registration — the Codex equivalent of databricks-claude's
// SettingsManager.
type ConfigManager struct {
	config   *tomlconfig.Manager
	lock     *filelock.FileLock
	registry *registry.SessionRegistry
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
		config:   tomlconfig.NewManager(filepath.Join(codexDir, "config.toml")),
		lock:     filelock.New(filepath.Join(codexDir, ".config.lock")),
		registry: registry.New(filepath.Join(codexDir, ".sessions.json")),
	}
}

// Setup backs up config.toml, patches it with the proxy config, and
// registers the current session. The caller must call Restore on exit.
// otelEndpoint is the OTLP HTTP log endpoint (e.g., "http://127.0.0.1:PORT/otel/v1/logs");
// pass "" to omit the [otel] section.
func (cm *ConfigManager) Setup(proxyURL, model string, modelExplicit bool, otelEndpoint string) error {
	if err := cm.lock.Lock(); err != nil {
		log.Printf("databricks-codex: config lock warning: %v", err)
	}
	defer cm.lock.Unlock()

	// Recover from a previous crash if needed.
	cm.config.RestoreFromBackup()

	if err := cm.config.Backup(); err != nil {
		return err
	}

	if err := cm.config.Patch(tomlconfig.PatchConfig{
		ProxyURL:      proxyURL,
		Model:         model,
		ModelExplicit: modelExplicit,
		OTELEndpoint:  otelEndpoint,
	}); err != nil {
		return err
	}

	if err := cm.registry.Register(os.Getpid(), proxyURL); err != nil {
		log.Printf("databricks-codex: session register warning: %v", err)
	}

	log.Printf("databricks-codex: patched %s (proxy: %s)", cm.config.ConfigPath(), proxyURL)
	return nil
}

// Restore unregisters the current session and restores config.toml.
// If other sessions are still alive, it updates the config to point at
// the most recent survivor's proxy instead of fully restoring.
func (cm *ConfigManager) Restore() {
	if err := cm.lock.Lock(); err != nil {
		log.Printf("databricks-codex: config lock warning: %v", err)
	}
	defer cm.lock.Unlock()

	cm.registry.Unregister(os.Getpid())

	// Check for surviving sessions.
	survivor, err := cm.registry.MostRecentLive()
	if err == nil && survivor != nil {
		// Another session is alive — hand off the config to its proxy.
		log.Printf("databricks-codex: handing off config.toml to session %d (proxy: %s)",
			survivor.PID, survivor.ProxyURL)
		if err := cm.config.UpdateProxyURL(survivor.ProxyURL); err != nil {
			log.Printf("databricks-codex: handoff failed, restoring original: %v", err)
			cm.config.Restore()
		}
		return
	}

	// Last session — restore original config.
	if err := cm.config.Restore(); err != nil {
		log.Printf("databricks-codex: config restore failed: %v", err)
	} else {
		log.Printf("databricks-codex: config.toml restored")
	}
}
