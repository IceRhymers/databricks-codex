// Package tomlconfig manages the Codex CLI config.toml file.
// It uses simple string-based manipulation rather than a full TOML parser,
// keeping the zero-external-dependency constraint.
//
// Write-once semantics
// ====================
//
// This package implements a write-once model: Patch() is idempotent and
// final. There is NO Backup/Restore round-trip. The proxy lifecycle does
// not own the user's config.toml across sessions — once we write it, our
// managed keys/sections stay (re-emitted at every session start), and
// non-managed user content is preserved byte-for-byte.
//
// Rationale: restore-on-exit was unreliable in practice (os.Exit skips
// deferred calls, panics or SIGKILL leave the user with a stale config,
// multi-session handoff races make "who restores" ambiguous). The same
// pattern in databricks-claude was already write-once for the same
// reason. Users who want to remove our managed keys can do so manually
// or by uninstalling the wrapper — we do not silently revert their
// config behind their back.
//
// Layout (Codex profile-v2 + GUI root-key fix)
// ============================================
//
//   ~/.codex/config.toml              — base user config. We own:
//                                       - root `model_provider`
//                                       - root `model`
//                                       - [model_providers.databricks-proxy]
//                                       - [otel]
//                                       Anything else (including a
//                                       user-owned root `profile` key)
//                                       is preserved byte-for-byte.
//   ~/.codex/databricks-proxy.config.toml — profile-v2 sibling file. We
//                                       own this file entirely. Contains
//                                       `model_provider` and (when set)
//                                       `model`. Codex is launched with
//                                       `--profile databricks-proxy` so
//                                       this layer is merged on top of
//                                       base for the transparent-wrapper
//                                       path; the GUI and raw `codex`
//                                       use the root keys instead
//                                       (openai/codex#13041).
//
// Pre-v0.7 layouts that wrote `profile = "databricks-proxy"` and
// `[profiles.databricks-proxy]` into base config.toml are rejected by
// current Codex (see https://developers.openai.com/codex/config-advanced#profiles).
// Patch() strips any leftover legacy keys from the user's base config
// and migrates the model value forward into the sibling file. Stripping
// is one-way — the legacy keys do not come back.
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
	ProxyURL            string // e.g., "http://127.0.0.1:54321"
	Model               string // e.g., "databricks-gpt-5-5"
	ModelExplicit       bool   // true when --model was explicitly passed
	OTELLogsEndpoint    string // e.g., "http://127.0.0.1:54321/otel/v1/logs"
	OTELMetricsEndpoint string // e.g., "http://127.0.0.1:54321/otel/v1/metrics"
}

// SiblingProfileName is the Codex profile-v2 name we register under, and
// also the basename of the sibling file (`<name>.config.toml`).
const SiblingProfileName = "databricks-proxy"

// Manager reads and patches the Codex config.toml file plus the
// profile-v2 sibling file. Write-once — no backup, no restore.
type Manager struct {
	configPath  string
	siblingPath string // ~/.codex/databricks-proxy.config.toml
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
		configPath:  configPath,
		siblingPath: filepath.Join(filepath.Dir(configPath), SiblingProfileName+".config.toml"),
	}
}

// ConfigPath returns the path to config.toml.
func (m *Manager) ConfigPath() string { return m.configPath }

// SiblingPath returns the path to the profile-v2 sibling file.
func (m *Manager) SiblingPath() string { return m.siblingPath }

// managedSections lists section headers we own in base config.toml.
// [profiles.databricks-proxy] is intentionally NOT here: under Codex
// profile-v2 it lives in the sibling file. Patch() STRIPS a legacy
// [profiles.databricks-proxy] from base config (one-way migration) so
// old installs become valid profile-v2 configs.
var managedSections = []string{
	"model_providers.databricks-proxy",
	"otel",
}

// managedRootKeys lists root-level keys we own in base config.toml.
//
// Why root keys, even though profile-v2 gives us a sibling layer:
//
//   - The Codex GUI (and certain IDE extensions / raw `codex` invocations
//     that don't pass --profile) ignore profile-v2 layering and read the
//     root-level `model_provider` directly. Without these root keys, the
//     GUI falls through to the built-in `openai` provider (which has
//     supports_websockets = true hardcoded), attempts a WebSocket
//     upgrade against Databricks AI Gateway, and gets HTTP 400.
//     Confirmed by upstream issue openai/codex#13041 — the recommended
//     workaround is exactly this root-level provider override.
//
//   - The transparent-wrapper path (`databricks-codex "..."`) still uses
//     --profile databricks-proxy + the sibling file. Both paths converge
//     on the same provider, because the sibling overrides root with the
//     same values.
//
// We do NOT write a root `profile = "..."` key — Codex profile-v2 rejects
// that (codex-rs/core/src/config/mod.rs:2606). Stripping it on Patch is
// the migration path for users upgrading from pre-v0.7.
var managedRootKeys = []string{"model_provider", "model"}

// Patch is the idempotent write-once entry point. It reads the current
// config.toml (if any), strips legacy v1 keys we own, writes our root
// keys + provider + otel sections, and overwrites the sibling file. All
// non-managed user content is preserved byte-for-byte.
func (m *Manager) Patch(cfg PatchConfig) error {
	content := ""
	if data, err := os.ReadFile(m.configPath); err == nil {
		content = string(data)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("read config.toml: %w", err)
	}

	// --- Strip legacy v1 leftovers from base config.toml ---
	//
	// Codex profile-v2 rejects ANY root `profile = ...` key whenever the
	// merged config has one (codex-rs/core/src/config/mod.rs:2606), and
	// also rejects a [profiles.X] table in base config when --profile X
	// is active (codex-rs/config/src/loader/mod.rs:240). Strip both so
	// the GUI/TUI/IDE all load successfully. This is a one-way
	// migration — the legacy keys are not put back.
	rootProfileVal := findRootProfileValue(content)
	if rootProfileVal == SiblingProfileName {
		// Only strip our own legacy key. A user's unrelated
		// `profile = "myprofile"` is left alone — codex profile-v2
		// rejects it independently and that's a pre-existing
		// condition, not something we should silently overwrite.
		content = stripRootKey(content, "profile")
	}
	content = stripSection(content, "profiles.databricks-proxy")

	// --- Root-level provider override (fixes GUI / raw codex paths) ---
	content = setRootKey(content, "model_provider", fmt.Sprintf("%q", SiblingProfileName))
	resolvedModel := m.resolveModel(cfg, content)
	if resolvedModel != "" {
		content = setRootKey(content, "model", fmt.Sprintf("%q", resolvedModel))
	}

	// --- Sections we own in base config.toml ---
	content = setSection(content, "model_providers.databricks-proxy",
		m.buildProviderSection(cfg))

	// Always handle the [otel] section: when both endpoints are set,
	// write it; when both are empty, remove it. This makes --no-otel
	// actually erase the section from config.toml — not leave stale
	// exporter lines behind.
	if cfg.OTELLogsEndpoint != "" || cfg.OTELMetricsEndpoint != "" {
		content = setSection(content, "otel", m.buildOTELSection(cfg))
	} else {
		content = removeSection(content, "otel")
	}

	if err := atomicWrite(m.configPath, []byte(content)); err != nil {
		return fmt.Errorf("write config.toml: %w", err)
	}

	// --- Write the profile-v2 sibling file (overwrite) ---
	if err := atomicWrite(m.siblingPath, []byte(m.buildSiblingFile(resolvedModel))); err != nil {
		return fmt.Errorf("write sibling config: %w", err)
	}
	return nil
}

// resolveModel applies the model-value resolution chain. content is the
// current base config.toml (post-strip) used to look up a root-level
// model line. The sibling file is checked first so a user-edited or
// previously-written value wins over our resolved default.
//
// Chain:
//
//	1. existing sibling file's `model = ...` (preserve user-set value)
//	2. legacy `[profiles.databricks-proxy] model = ...` from a
//	   pre-strip snapshot of base config (one-time migration)
//	3. explicit --model flag value (cfg.ModelExplicit)
//	4. root-level `model = ...` already in base config
//	5. resolved fallback (cfg.Model) — only if non-empty
func (m *Manager) resolveModel(cfg PatchConfig, currentBase string) string {
	if cfg.ModelExplicit {
		return cfg.Model
	}

	// 1. Sibling file's existing model.
	if sdata, err := os.ReadFile(m.siblingPath); err == nil {
		if existing := findRootModelInString(string(sdata)); existing != "" {
			return existing
		}
	}

	// 2. Legacy section migration: re-read the file from disk so we
	//    see the [profiles.databricks-proxy] block before we stripped
	//    it. currentBase has already had it removed.
	if odata, err := os.ReadFile(m.configPath); err == nil {
		if legacyModel := findModelInSectionString(string(odata), "profiles.databricks-proxy"); legacyModel != "" {
			return legacyModel
		}
	}

	// 3. Root-level model in current base config.
	if rootModel := findRootModelInString(currentBase); rootModel != "" {
		return rootModel
	}

	// 4. Fall back to the resolved value (saved state or built-in default).
	return cfg.Model
}

// buildSiblingFile renders the profile-v2 layer file. Caller resolves the
// model value; passing "" omits the model line entirely.
func (m *Manager) buildSiblingFile(model string) string {
	var b strings.Builder
	b.WriteString("# Managed by databricks-codex. Do not edit by hand.\n")
	b.WriteString("# See https://developers.openai.com/codex/config-advanced#profiles\n")
	b.WriteString("model_provider = \"databricks-proxy\"\n")
	if model != "" {
		b.WriteString(fmt.Sprintf("model = %q\n", model))
	}
	return b.String()
}

// buildProviderSection builds the [model_providers.databricks-proxy] body.
//
// supports_websockets = false is written explicitly (defensive — the serde
// default is also false). Codex's model client prefers WebSocket transport
// for the Responses API whenever the provider has supports_websockets =
// true, falling back to SSE-over-HTTP only on connection failure
// (codex-rs/core/src/client.rs:1601-1640 — stream_responses_websocket
// then try_switch_fallback_transport). Databricks AI Gateway speaks SSE
// over POST /v1/responses but does NOT accept WebSocket upgrades, so we
// must keep this field off — if a stale config has it set to true, the
// Codex GUI will WebSocket-upgrade and receive 400s from upstream until
// the per-session fallback kicks in.
//
// See client.rs:798 (responses_websocket_enabled gate) and
// model-provider-info/src/lib.rs:134-136 (the field itself).
func (m *Manager) buildProviderSection(cfg PatchConfig) string {
	var b strings.Builder
	b.WriteString("name = \"Databricks Proxy\"\n")
	b.WriteString(fmt.Sprintf("base_url = %q\n", cfg.ProxyURL))
	b.WriteString("api_key = \"databricks-proxy\"\n")
	b.WriteString("wire_api = \"responses\"\n")
	b.WriteString("supports_websockets = false\n")
	return b.String()
}

// buildOTELSection builds the [otel] section body.
//
// Note: Codex's upstream `metrics_exporter` default is Statsig
// (https://ab.chatgpt.com/otlp/v1/metrics). We do NOT defensively
// rewrite this at the proxy layer; setting `metrics_exporter` here is
// the user's explicit opt-in to route metrics through Databricks.
func (m *Manager) buildOTELSection(cfg PatchConfig) string {
	var b strings.Builder
	b.WriteString("environment = \"production\"\n")
	if cfg.OTELLogsEndpoint != "" {
		b.WriteString(fmt.Sprintf("exporter = { otlp-http = { endpoint = %q, protocol = \"binary\" } }\n", cfg.OTELLogsEndpoint))
	}
	if cfg.OTELMetricsEndpoint != "" {
		b.WriteString(fmt.Sprintf("metrics_exporter = { otlp-http = { endpoint = %q, protocol = \"binary\" } }\n", cfg.OTELMetricsEndpoint))
	}
	return b.String()
}

// UpdateProxyURL updates only the base_url in the provider section.
// Used for multi-session handoff when an owner session ends and a
// survivor rebinds the port: the proxy URL changes, but everything
// else stays.
func (m *Manager) UpdateProxyURL(newURL string) error {
	data, err := os.ReadFile(m.configPath)
	if err != nil {
		return fmt.Errorf("read config.toml: %w", err)
	}
	lines := strings.Split(string(data), "\n")
	inSection := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "[model_providers.databricks-proxy]" {
			inSection = true
			continue
		}
		if inSection {
			if strings.HasPrefix(trimmed, "[") {
				break
			}
			if strings.HasPrefix(trimmed, "base_url") {
				lines[i] = fmt.Sprintf("base_url = %q", newURL)
				break
			}
		}
	}
	return atomicWrite(m.configPath, []byte(strings.Join(lines, "\n")))
}

// --- Pure string helpers (no Manager state) ---

// setRootKey writes (or overwrites) a root-level key to the given value.
// Pure write, no tracking. If the key was absent, the new line is
// prepended above the first section header.
//
// value is the right-hand side of the assignment — callers are responsible
// for quoting (e.g. fmt.Sprintf("%q", "foo")).
func setRootKey(content, key, value string) string {
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if isRootKey(trimmed, key) && !inAnySection(lines, i) {
			lines[i] = fmt.Sprintf("%s = %s", key, value)
			return strings.Join(lines, "\n")
		}
	}
	insertIdx := 0
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") {
			insertIdx = i
			break
		}
		insertIdx = i + 1
	}
	lines = insertAt(lines, insertIdx, fmt.Sprintf("%s = %s", key, value))
	return strings.Join(lines, "\n")
}

// stripRootKey removes a root-level key if present. No-op if absent.
func stripRootKey(content, key string) string {
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if isRootKey(trimmed, key) && !inAnySection(lines, i) {
			return strings.Join(removeAt(lines, i), "\n")
		}
	}
	return content
}

// stripSection removes a [section] block if present. No-op if absent.
// Also removes a blank line on either side of the removed block so we
// don't leave a double-blank gap.
func stripSection(content, sectionName string) string {
	header := "[" + sectionName + "]"
	lines := strings.Split(content, "\n")
	startIdx := -1
	for i, line := range lines {
		if strings.TrimSpace(line) == header {
			startIdx = i
			break
		}
	}
	if startIdx == -1 {
		return content
	}
	endIdx := len(lines)
	for i := startIdx + 1; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i])
		if strings.HasPrefix(trimmed, "[") && !strings.HasPrefix(trimmed, "[[") {
			endIdx = i
			break
		}
	}
	if endIdx < len(lines) && strings.TrimSpace(lines[endIdx-1]) == "" {
		// blank inside the block being removed — no-op
	} else if endIdx < len(lines) && strings.TrimSpace(lines[endIdx]) == "" {
		endIdx++
	}
	if startIdx > 0 && strings.TrimSpace(lines[startIdx-1]) == "" {
		startIdx--
	}
	newLines := make([]string, 0, len(lines))
	newLines = append(newLines, lines[:startIdx]...)
	newLines = append(newLines, lines[endIdx:]...)
	return strings.Join(newLines, "\n")
}

// removeSection is a thin alias for stripSection, kept distinct only for
// callsite-readability where we mean "user said disable, not migration".
func removeSection(content, sectionName string) string {
	return stripSection(content, sectionName)
}

// setSection writes (or overwrites) a [section] with the given body.
// Pure write, no tracking. If absent, appended at end with a leading
// blank-line separator.
func setSection(content, sectionName, body string) string {
	header := "[" + sectionName + "]"
	lines := strings.Split(content, "\n")
	startIdx := -1
	for i, line := range lines {
		if strings.TrimSpace(line) == header {
			startIdx = i
			break
		}
	}
	if startIdx == -1 {
		var sb strings.Builder
		sb.WriteString(header + "\n")
		sb.WriteString(body)
		if !strings.HasSuffix(content, "\n") && content != "" {
			content += "\n"
		}
		return content + "\n" + sb.String()
	}
	endIdx := len(lines)
	for i := startIdx + 1; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i])
		if strings.HasPrefix(trimmed, "[") && !strings.HasPrefix(trimmed, "[[") {
			endIdx = i
			break
		}
	}
	var replacement []string
	replacement = append(replacement, header)
	for _, line := range strings.Split(body, "\n") {
		if line != "" {
			replacement = append(replacement, line)
		}
	}
	newLines := make([]string, 0, len(lines))
	newLines = append(newLines, lines[:startIdx]...)
	newLines = append(newLines, replacement...)
	newLines = append(newLines, lines[endIdx:]...)
	return strings.Join(newLines, "\n")
}

// findRootProfileValue returns the value of a root `profile = "X"` key, or "".
func findRootProfileValue(content string) string {
	if content == "" {
		return ""
	}
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if isRootKey(trimmed, "profile") && !inAnySection(lines, i) {
			parts := strings.SplitN(trimmed, "=", 2)
			if len(parts) == 2 {
				return strings.Trim(strings.TrimSpace(parts[1]), `"`)
			}
		}
	}
	return ""
}

// findRootModelInString returns the value of a root-level `model = ...`
// line (not inside any section). Returns "" if not found.
func findRootModelInString(content string) string {
	if content == "" {
		return ""
	}
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if isRootKey(trimmed, "model") && !inAnySection(lines, i) {
			parts := strings.SplitN(trimmed, "=", 2)
			if len(parts) == 2 {
				val := strings.TrimSpace(parts[1])
				val = strings.Trim(val, `"`)
				return val
			}
		}
	}
	return ""
}

// findModelInSectionString looks for a `model = ...` line inside a named
// section, returning the *value*. Empty if not found.
func findModelInSectionString(content, sectionName string) string {
	if content == "" {
		return ""
	}
	header := "[" + sectionName + "]"
	lines := strings.Split(content, "\n")
	inSection := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == header {
			inSection = true
			continue
		}
		if inSection {
			if strings.HasPrefix(trimmed, "[") {
				break
			}
			if strings.HasPrefix(trimmed, "model") && strings.Contains(trimmed, "=") {
				// Distinguish "model" from "model_provider"
				afterKey := strings.TrimPrefix(trimmed, "model")
				if len(afterKey) > 0 && (afterKey[0] == ' ' || afterKey[0] == '=') {
					parts := strings.SplitN(trimmed, "=", 2)
					if len(parts) == 2 {
						val := strings.TrimSpace(parts[1])
						val = strings.Trim(val, `"`)
						return val
					}
				}
			}
		}
	}
	return ""
}

// isRootKey checks if a trimmed line is a root-level assignment for the given key.
func isRootKey(trimmed, key string) bool {
	return strings.HasPrefix(trimmed, key+" ") || strings.HasPrefix(trimmed, key+"=")
}

// inAnySection returns true if line at idx is inside a [section].
func inAnySection(lines []string, idx int) bool {
	for i := idx - 1; i >= 0; i-- {
		trimmed := strings.TrimSpace(lines[i])
		if strings.HasPrefix(trimmed, "[") && !strings.HasPrefix(trimmed, "[[") {
			return true
		}
	}
	return false
}

// insertAt inserts a string at the given index in a slice.
func insertAt(lines []string, idx int, s string) []string {
	if idx >= len(lines) {
		return append(lines, s)
	}
	lines = append(lines, "")
	copy(lines[idx+1:], lines[idx:])
	lines[idx] = s
	return lines
}

// removeAt removes the element at idx from a slice.
func removeAt(lines []string, idx int) []string {
	return append(lines[:idx], lines[idx+1:]...)
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
