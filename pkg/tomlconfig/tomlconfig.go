// Package tomlconfig manages the Codex CLI config.toml file.
// It uses simple string-based manipulation rather than a full TOML parser,
// keeping the zero-external-dependency constraint.
//
// Layout (Codex profile-v2):
//
//   ~/.codex/config.toml              — base user config. We own
//                                       [model_providers.databricks-proxy]
//                                       and [otel] here. Anything else
//                                       (including a user-owned root
//                                       `profile` key) is preserved
//                                       byte-for-byte across Restore.
//   ~/.codex/databricks-proxy.config.toml — profile-v2 sibling file. We
//                                       own this file entirely. Contains
//                                       `model_provider` and (when set)
//                                       `model`. Codex is launched with
//                                       `--profile databricks-proxy` so
//                                       this layer is merged on top of
//                                       base.
//
// Pre-v0.7 layouts that wrote `profile = "databricks-proxy"` and
// `[profiles.databricks-proxy]` into base config.toml are rejected by
// current Codex (see https://developers.openai.com/codex/config-advanced#profiles).
// Patch() strips any leftover legacy keys from the user's base config and
// migrates the model value forward into the sibling file. Restore() puts
// the original legacy keys back so a session run by an older
// databricks-codex still finds what it expects.
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

// sentinel is stored in originals when a key/section was absent before patching.
const sentinel = "\x00nil"

// Manager reads, patches, and restores the Codex config.toml file.
type Manager struct {
	configPath  string
	backupPath  string
	siblingPath string // ~/.codex/databricks-proxy.config.toml
	siblingBak  string // sibling backup path for crash recovery
	original    []byte // saved original config.toml content for crash-recovery backup

	// Surgical restore state: tracks what we changed so Restore only undoes
	// what we touched. Keys map to original line/block content, or sentinel
	// if the key/section was absent before patching.
	origRootKeys    map[string]string // root key name -> original line or sentinel
	origSections    map[string]string // section header (e.g. "profiles.databricks-proxy") -> original block or sentinel
	patchedModelVal string            // model value we wrote into the sibling file

	// Sibling-file restore state.
	siblingOriginal []byte // saved content if sibling file existed pre-patch
	siblingExisted  bool   // true if the sibling file existed pre-patch
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
	siblingPath := filepath.Join(filepath.Dir(configPath), SiblingProfileName+".config.toml")
	return &Manager{
		configPath:   configPath,
		backupPath:   configPath + ".databricks-codex-backup",
		siblingPath:  siblingPath,
		siblingBak:   siblingPath + ".databricks-codex-backup",
		origRootKeys: make(map[string]string),
		origSections: make(map[string]string),
	}
}

// ConfigPath returns the path to config.toml.
func (m *Manager) ConfigPath() string {
	return m.configPath
}

// SiblingPath returns the path to the profile-v2 sibling file.
func (m *Manager) SiblingPath() string {
	return m.siblingPath
}

// Backup reads the current config.toml (and the sibling file, if any) and
// saves the original content both in memory and to backup files for crash
// recovery.
func (m *Manager) Backup() error {
	data, err := os.ReadFile(m.configPath)
	if err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("read config.toml: %w", err)
		}
		m.original = nil
	} else {
		m.original = data
		if err := atomicWrite(m.backupPath, data); err != nil {
			return fmt.Errorf("write backup: %w", err)
		}
	}

	sdata, err := os.ReadFile(m.siblingPath)
	if err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("read sibling config: %w", err)
		}
		m.siblingOriginal = nil
		m.siblingExisted = false
	} else {
		m.siblingOriginal = sdata
		m.siblingExisted = true
		if err := atomicWrite(m.siblingBak, sdata); err != nil {
			return fmt.Errorf("write sibling backup: %w", err)
		}
	}
	return nil
}

// managedSections lists section headers we manage in base config.toml.
// [profiles.databricks-proxy] is intentionally NOT here: under Codex
// profile-v2 it lives in the sibling file. We still STRIP a legacy
// [profiles.databricks-proxy] from base config (recording the original
// for Restore) so old installs migrate forward cleanly.
var managedSections = []string{
	"model_providers.databricks-proxy",
	"otel",
}

// Patch performs surgical patching of config.toml: it reads the existing file,
// saves originals for keys/sections it will touch, then injects/updates only
// managed keys and sections. All non-managed content is preserved byte-for-byte.
//
// It also writes (overwriting any existing content) the profile-v2 sibling
// file with model_provider + model.
func (m *Manager) Patch(cfg PatchConfig) error {
	content := ""
	if m.original != nil {
		content = string(m.original)
	} else if data, err := os.ReadFile(m.configPath); err == nil {
		content = string(data)
	}

	// --- Strip legacy v1 leftovers from base config.toml ---
	//
	// Codex profile-v2 rejects ANY root `profile = ...` key whenever the
	// merged config has one (codex-rs/core/src/config/mod.rs:2606), and
	// also rejects a [profiles.X] table in base config when --profile X is
	// active (codex-rs/config/src/loader/mod.rs:240). Strip both here so
	// that launching codex with --profile databricks-proxy succeeds. The
	// originals are recorded via the existing tracking so Restore() puts
	// them back when our session ends.
	rootProfileVal := findRootProfileValue(content)
	if rootProfileVal == SiblingProfileName {
		// Only strip our own legacy key. A user's unrelated
		// `profile = "myprofile"` is left alone — codex profile-v2
		// rejects it independently and that's a pre-existing
		// condition, not something we should silently overwrite.
		content = m.stripRootKey(content, "profile")
	}
	content = m.stripSection(content, "profiles.databricks-proxy")

	// --- Sections we still own in base config.toml ---
	content = m.patchSection(content, "model_providers.databricks-proxy",
		m.buildProviderSection(cfg))

	// Always handle the [otel] section: when both endpoints are set, patch
	// it; when both are empty, remove it if it exists. This makes --no-otel
	// (or --no-otel-metrics/--no-otel-logs that clears the last remaining
	// signal) actually erase the section from config.toml — not just leave
	// stale exporter lines behind.
	if cfg.OTELLogsEndpoint != "" || cfg.OTELMetricsEndpoint != "" {
		content = m.patchSection(content, "otel",
			m.buildOTELSection(cfg))
	} else {
		content = m.removeSection(content, "otel")
	}

	if err := atomicWrite(m.configPath, []byte(content)); err != nil {
		return fmt.Errorf("write patched config.toml: %w", err)
	}

	// --- Write the profile-v2 sibling file ---
	siblingContent := m.buildSiblingFile(cfg, rootProfileVal)
	if err := atomicWrite(m.siblingPath, []byte(siblingContent)); err != nil {
		return fmt.Errorf("write sibling config: %w", err)
	}
	return nil
}

// buildSiblingFile renders the profile-v2 layer file. Resolution chain for
// the `model` value (mirrors pre-v0.7 buildProfileSection semantics, just
// targeted at the sibling file):
//
//  1. existing sibling file's `model = ...` (preserve user-set value)
//  2. legacy `[profiles.databricks-proxy] model = ...` from base config
//     (migrate forward when upgrading from pre-v0.7)
//  3. explicit --model flag value
//  4. root-level `model = ...` in the original base config
//  5. resolved fallback (cfg.Model) — only if non-empty
//
// legacyRootProfile is the value of the legacy root `profile = "X"` key in
// base config.toml at Backup time, if any. Today only legacyRootProfile ==
// "databricks-proxy" is interesting (it means a prior databricks-codex run
// wrote it and we should ignore any cargo model lookup that depends on it).
func (m *Manager) buildSiblingFile(cfg PatchConfig, legacyRootProfile string) string {
	var b strings.Builder
	b.WriteString("# Managed by databricks-codex. Do not edit by hand.\n")
	b.WriteString("# See https://developers.openai.com/codex/config-advanced#profiles\n")
	b.WriteString("model_provider = \"databricks-proxy\"\n")

	model := m.resolveSiblingModel(cfg, legacyRootProfile)
	if model != "" {
		b.WriteString(fmt.Sprintf("model = %q\n", model))
		m.patchedModelVal = model
	}
	return b.String()
}

// resolveSiblingModel applies the model-value resolution chain. Pure func
// (apart from reading m.siblingOriginal / m.original which are captured at
// Backup time).
func (m *Manager) resolveSiblingModel(cfg PatchConfig, legacyRootProfile string) string {
	_ = legacyRootProfile // reserved for future migration heuristics

	if cfg.ModelExplicit {
		return cfg.Model
	}

	// 1. Preserve a model already in the sibling file (user may have
	//    edited it, or a prior session wrote it).
	if existing := findRootModelInString(string(m.siblingOriginal)); existing != "" {
		return existing
	}

	// 2. Migrate a model from the legacy [profiles.databricks-proxy]
	//    section in base config.toml, if a prior databricks-codex left
	//    one behind.
	if legacyModel := findModelInSectionString(string(m.original), "profiles.databricks-proxy"); legacyModel != "" {
		return legacyModel
	}

	// 3. Carry forward a root-level model from base config.toml. Under
	//    profile-v2 layering this would already be visible without us
	//    writing it, but echoing it into the sibling makes the active
	//    layer self-describing and matches pre-v0.7 behaviour.
	if rootModel := findRootModelInString(string(m.original)); rootModel != "" {
		return rootModel
	}

	// 4. Fall back to the resolved value (saved state or built-in default).
	return cfg.Model
}

// buildProviderSection builds the [model_providers.databricks-proxy] section body.
func (m *Manager) buildProviderSection(cfg PatchConfig) string {
	var b strings.Builder
	b.WriteString("name = \"Databricks Proxy\"\n")
	b.WriteString(fmt.Sprintf("base_url = %q\n", cfg.ProxyURL))
	b.WriteString("api_key = \"databricks-proxy\"\n")
	b.WriteString("wire_api = \"responses\"\n")
	return b.String()
}

// buildOTELSection builds the [otel] section body.
// Emits `exporter` (logs) and/or `metrics_exporter` for whichever endpoints
// are non-empty. Both can coexist in the same [otel] block.
//
// Note: Codex's upstream `metrics_exporter` default is Statsig
// (https://ab.chatgpt.com/otlp/v1/metrics). We do NOT defensively rewrite
// this at the proxy layer; setting `metrics_exporter` here is the user's
// explicit opt-in to route metrics through Databricks instead.
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

// stripRootKey removes a root-level key if present, recording the original
// line so Restore can put it back. If the key is absent, no tracking and
// no mutation.
func (m *Manager) stripRootKey(content, key string) string {
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if isRootKey(trimmed, key) && !inAnySection(lines, i) {
			m.origRootKeys[key] = line
			return strings.Join(removeAt(lines, i), "\n")
		}
	}
	return content
}

// stripSection removes a [section] from content if present, recording the
// original block so Restore can put it back. If the section is absent, no
// tracking and no mutation.
func (m *Manager) stripSection(content, sectionName string) string {
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
	m.origSections[sectionName] = strings.Join(lines[startIdx:endIdx], "\n")

	// Drop a trailing blank line that typically separates sections so
	// removing a mid-file section doesn't leave a double-blank gap.
	if endIdx < len(lines) && strings.TrimSpace(lines[endIdx-1]) == "" {
		// blank already part of the section we're dropping
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

// removeSection finds a [section] in the content and removes it entirely
// (header + body up to the next section header or EOF). The original block
// is recorded in origSections so Restore() can put it back.
//
// If the section is not present, this is a no-op — and crucially, we do
// NOT record a sentinel, because there's nothing to undo on Restore().
func (m *Manager) removeSection(content, sectionName string) string {
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
		// Section absent — nothing to remove, nothing to track.
		return content
	}

	// Find section end (next section header or EOF).
	endIdx := len(lines)
	for i := startIdx + 1; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i])
		if strings.HasPrefix(trimmed, "[") && !strings.HasPrefix(trimmed, "[[") {
			endIdx = i
			break
		}
	}

	// Save original block so Restore() can put it back if needed.
	m.origSections[sectionName] = strings.Join(lines[startIdx:endIdx], "\n")

	// Also drop the trailing blank line that typically separates sections,
	// so removing [otel] doesn't leave a double-blank gap behind. Only do
	// this if endIdx is followed by a blank line (i.e. we're removing a
	// mid-file section, not a trailing one).
	if endIdx < len(lines) && strings.TrimSpace(lines[endIdx-1]) == "" {
		// endIdx-1 is already a blank line inside the section we're
		// removing — nothing extra to do.
	} else if endIdx < len(lines) && strings.TrimSpace(lines[endIdx]) == "" {
		// The line right after the section is blank — consume it.
		endIdx++
	}
	// Also drop a blank line immediately BEFORE the section if present,
	// so we don't leave a dangling separator.
	if startIdx > 0 && strings.TrimSpace(lines[startIdx-1]) == "" {
		startIdx--
	}

	newLines := make([]string, 0, len(lines))
	newLines = append(newLines, lines[:startIdx]...)
	newLines = append(newLines, lines[endIdx:]...)

	return strings.Join(newLines, "\n")
}

// patchSection finds a [section] in the content, saves its original block,
// and replaces or appends the managed section.
func (m *Manager) patchSection(content, sectionName, body string) string {
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
		// Section absent — record sentinel, append.
		m.origSections[sectionName] = sentinel
		var sb strings.Builder
		sb.WriteString(header + "\n")
		sb.WriteString(body)
		// Ensure content ends with newline before appending.
		if !strings.HasSuffix(content, "\n") && content != "" {
			content += "\n"
		}
		content += "\n" + sb.String()
		return content
	}

	// Find section end (next section header or EOF).
	endIdx := len(lines)
	for i := startIdx + 1; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i])
		if strings.HasPrefix(trimmed, "[") && !strings.HasPrefix(trimmed, "[[") {
			endIdx = i
			break
		}
	}

	// Save original block.
	origBlock := strings.Join(lines[startIdx:endIdx], "\n")
	m.origSections[sectionName] = origBlock

	// Build replacement.
	var replacement []string
	replacement = append(replacement, header)
	for _, line := range strings.Split(body, "\n") {
		if line != "" {
			replacement = append(replacement, line)
		}
	}

	// Replace the section block.
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
// line (not inside any section). Returns empty string if not found.
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
// section, returning the *value* (not the full line). Empty if not found.
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

// Restore performs surgical restoration: only removes/restores keys and sections
// that we patched. Non-managed content is untouched.
func (m *Manager) Restore() error {
	// Restore the sibling file first — independent of base config.toml.
	if err := m.restoreSiblingFile(); err != nil {
		return err
	}

	// If we never had an original file and we added everything, remove the file.
	if m.original == nil && allSentinels(m.origRootKeys) && allSentinels(m.origSections) {
		os.Remove(m.configPath)
		os.Remove(m.backupPath)
		return nil
	}

	data, err := os.ReadFile(m.configPath)
	if err != nil {
		if os.IsNotExist(err) {
			os.Remove(m.backupPath)
			return nil
		}
		return fmt.Errorf("read config.toml for restore: %w", err)
	}
	content := string(data)

	// Restore root keys.
	for key, orig := range m.origRootKeys {
		content = m.restoreRootKey(content, key, orig)
	}

	// Restore sections.
	for sectionName, orig := range m.origSections {
		content = m.restoreSection(content, sectionName, orig)
	}

	// Clean up trailing whitespace.
	content = strings.TrimRight(content, "\n") + "\n"

	if err := atomicWrite(m.configPath, []byte(content)); err != nil {
		return fmt.Errorf("restore config.toml: %w", err)
	}
	os.Remove(m.backupPath)
	return nil
}

// restoreSiblingFile restores the sibling profile-v2 file to its pre-patch
// state: either rewriting the original contents, or removing the file we
// created.
func (m *Manager) restoreSiblingFile() error {
	if m.siblingExisted {
		if err := atomicWrite(m.siblingPath, m.siblingOriginal); err != nil {
			return fmt.Errorf("restore sibling config: %w", err)
		}
	} else {
		if err := os.Remove(m.siblingPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove sibling config: %w", err)
		}
	}
	if err := os.Remove(m.siblingBak); err != nil && !os.IsNotExist(err) {
		// Backup cleanup failure isn't fatal — log and continue.
		log.Printf("databricks-codex: failed to remove sibling backup: %v", err)
	}
	return nil
}

// restoreRootKey restores a single root key to its original state.
func (m *Manager) restoreRootKey(content, key, orig string) string {
	if orig == sentinel {
		// Was absent — strip whatever's there now.
		lines := strings.Split(content, "\n")
		for i, line := range lines {
			trimmed := strings.TrimSpace(line)
			if isRootKey(trimmed, key) && !inAnySection(lines, i) {
				return strings.Join(removeAt(lines, i), "\n")
			}
		}
		return content
	}

	// Was present pre-patch — put the original line back. Two cases:
	//   (a) the current content still has the key (e.g. an unrelated edit
	//       left it intact): replace in place.
	//   (b) the current content does NOT have the key (we stripped it in
	//       Patch): re-insert near the top.
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if isRootKey(trimmed, key) && !inAnySection(lines, i) {
			lines[i] = orig
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
	lines = insertAt(lines, insertIdx, orig)
	return strings.Join(lines, "\n")
}

// restoreSection restores a section to its original state.
func (m *Manager) restoreSection(content, sectionName, orig string) string {
	header := "[" + sectionName + "]"
	lines := strings.Split(content, "\n")

	startIdx := -1
	for i, line := range lines {
		if strings.TrimSpace(line) == header {
			startIdx = i
			break
		}
	}

	// Helper: rebuild the lines slice with `replacement` (slice or nil for
	// pure deletion) replacing [startIdx..endIdx).
	splice := func(start, end int, replacement []string) string {
		newLines := make([]string, 0, len(lines)-(end-start)+len(replacement))
		newLines = append(newLines, lines[:start]...)
		newLines = append(newLines, replacement...)
		newLines = append(newLines, lines[end:]...)
		return strings.Join(newLines, "\n")
	}

	if startIdx == -1 {
		// Section is absent from current content. If orig was a sentinel
		// (it didn't exist pre-patch either), nothing to do. Otherwise we
		// need to re-add it (we stripped it during Patch). Append at end.
		if orig == sentinel {
			return content
		}
		// Ensure trailing newline before append.
		if !strings.HasSuffix(content, "\n") && content != "" {
			content += "\n"
		}
		return content + "\n" + orig + "\n"
	}

	// Find section end.
	endIdx := len(lines)
	for i := startIdx + 1; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i])
		if strings.HasPrefix(trimmed, "[") && !strings.HasPrefix(trimmed, "[[") {
			endIdx = i
			break
		}
	}

	if orig == sentinel {
		// Section was absent — remove the entire block.
		// Also remove a preceding blank line if present.
		removeStart := startIdx
		if removeStart > 0 && strings.TrimSpace(lines[removeStart-1]) == "" {
			removeStart--
		}
		return splice(removeStart, endIdx, nil)
	}

	// Restore original block.
	return splice(startIdx, endIdx, strings.Split(orig, "\n"))
}

// UpdateProxyURL updates only the base_url in the provider section.
// Used for multi-session handoff when an owner session ends and a survivor
// rebinds the port: the proxy URL changes, but everything else stays.
func (m *Manager) UpdateProxyURL(newURL string) error {
	data, err := os.ReadFile(m.configPath)
	if err != nil {
		return fmt.Errorf("read config.toml: %w", err)
	}
	content := string(data)

	lines := strings.Split(content, "\n")
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

// RestoreFromBackup restores config.toml + sibling file from their backup
// files if backups exist. Returns true if any restore was performed. Used
// for crash recovery on next startup when a prior session left backups
// behind without running Restore().
func (m *Manager) RestoreFromBackup() bool {
	restored := false

	if data, err := os.ReadFile(m.backupPath); err == nil {
		if err := atomicWrite(m.configPath, data); err == nil {
			os.Remove(m.backupPath)
			restored = true
		}
	}

	if data, err := os.ReadFile(m.siblingBak); err == nil {
		if err := atomicWrite(m.siblingPath, data); err == nil {
			os.Remove(m.siblingBak)
			restored = true
		}
	} else if os.IsNotExist(err) {
		// No backup → sibling file is ours from this aborted session;
		// remove it if present so it doesn't linger.
		if _, statErr := os.Stat(m.siblingPath); statErr == nil {
			if rerr := os.Remove(m.siblingPath); rerr == nil {
				restored = true
			}
		}
	}

	return restored
}

// --- Helpers ---

// isRootKey checks if a trimmed line is a root-level assignment for the given key.
func isRootKey(trimmed, key string) bool {
	return strings.HasPrefix(trimmed, key+" ") || strings.HasPrefix(trimmed, key+"=")
}

// inAnySection returns true if line at idx is inside a [section] (i.e., there's
// a section header somewhere above it with no intervening root-level context).
func inAnySection(lines []string, idx int) bool {
	for i := idx - 1; i >= 0; i-- {
		trimmed := strings.TrimSpace(lines[i])
		if strings.HasPrefix(trimmed, "[") && !strings.HasPrefix(trimmed, "[[") {
			return true
		}
	}
	return false
}

// allSentinels returns true if all values in the map are sentinel.
func allSentinels(m map[string]string) bool {
	for _, v := range m {
		if v != sentinel {
			return false
		}
	}
	return true
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
