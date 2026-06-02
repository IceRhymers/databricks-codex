package tomlconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setup(t *testing.T, initialContent string) (*Manager, string) {
	t.Helper()
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	if initialContent != "" {
		if err := os.WriteFile(configPath, []byte(initialContent), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	m := NewManager(configPath)
	if err := m.Backup(); err != nil {
		t.Fatal(err)
	}
	return m, configPath
}

func readConfig(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// readSibling returns the contents of the profile-v2 sibling file.
func readSibling(t *testing.T, m *Manager) string {
	t.Helper()
	data, err := os.ReadFile(m.SiblingPath())
	if err != nil {
		t.Fatalf("read sibling file: %v", err)
	}
	return string(data)
}

// TestPatch_WritesV2Layout verifies that an empty starting config produces
// the expected profile-v2 layout: provider section in base, profile fields
// in sibling, NO legacy `profile = ...` root key or `[profiles.X]` table.
func TestPatch_WritesV2Layout(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	m := NewManager(configPath)
	if err := m.Backup(); err != nil {
		t.Fatal(err)
	}

	err := m.Patch(PatchConfig{
		ProxyURL: "http://127.0.0.1:9999",
		Model:    "databricks-gpt-5-5",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Base config: provider section present, NO legacy layout.
	content := readConfig(t, configPath)
	if !strings.Contains(content, "[model_providers.databricks-proxy]") {
		t.Errorf("expected provider section in base, got:\n%s", content)
	}
	if !strings.Contains(content, `base_url = "http://127.0.0.1:9999"`) {
		t.Errorf("expected base_url in provider section, got:\n%s", content)
	}
	if strings.Contains(content, `profile = "databricks-proxy"`) {
		t.Errorf("v2: base config must NOT contain legacy root profile key, got:\n%s", content)
	}
	if strings.Contains(content, "[profiles.databricks-proxy]") {
		t.Errorf("v2: base config must NOT contain legacy [profiles.databricks-proxy] table, got:\n%s", content)
	}

	// Sibling file: profile-v2 layer with model_provider + model.
	sibling := readSibling(t, m)
	if !strings.Contains(sibling, `model_provider = "databricks-proxy"`) {
		t.Errorf("expected model_provider in sibling, got:\n%s", sibling)
	}
	if !strings.Contains(sibling, `model = "databricks-gpt-5-5"`) {
		t.Errorf("expected model in sibling, got:\n%s", sibling)
	}

	// Provider must opt OUT of WebSocket transport — Databricks AI
	// Gateway is SSE-only over /v1/responses, no WebSocket upgrade.
	// See buildProviderSection doc comment for the upstream code refs.
	if !strings.Contains(content, "supports_websockets = false") {
		t.Errorf("provider section must set supports_websockets = false (Databricks AI Gateway has no WebSocket transport), got:\n%s", content)
	}

	// Root-level model_provider + model must point at our proxy so that
	// the Codex GUI and raw `codex` invocations (which ignore profile-v2
	// layering — confirmed by openai/codex#13041) route through us.
	if !strings.Contains(content, `model_provider = "databricks-proxy"`) {
		t.Errorf("expected root-level model_provider override in base config, got:\n%s", content)
	}
	if !strings.Contains(content, `model = "databricks-gpt-5-5"`) {
		t.Errorf("expected root-level model in base config, got:\n%s", content)
	}
}

func TestPatch_PreservesUserSections(t *testing.T) {
	initial := `profile = "myprofile"

[projects.myapp]
sandbox_permissions = "full-auto"

[profiles.myprofile]
model_provider = "openai"
model = "gpt-4"
`
	m, configPath := setup(t, initial)

	err := m.Patch(PatchConfig{
		ProxyURL: "http://127.0.0.1:9999",
		Model:    "databricks-gpt-5-5",
	})
	if err != nil {
		t.Fatal(err)
	}

	content := readConfig(t, configPath)
	// User's profile root key (NOT databricks-proxy) must survive.
	if !strings.Contains(content, `profile = "myprofile"`) {
		t.Errorf("expected user's root profile to be preserved, got:\n%s", content)
	}
	// User's project section must survive.
	if !strings.Contains(content, "[projects.myapp]") {
		t.Error("expected [projects.myapp] to be preserved")
	}
	if !strings.Contains(content, `sandbox_permissions = "full-auto"`) {
		t.Error("expected sandbox_permissions to be preserved")
	}
	// User's other profile must survive.
	if !strings.Contains(content, "[profiles.myprofile]") {
		t.Error("expected [profiles.myprofile] to be preserved")
	}
}

// TestPatch_MigratesLegacyDatabricksProxyKeys covers the upgrade path: a
// user previously ran an old databricks-codex that left
// `profile = "databricks-proxy"` + `[profiles.databricks-proxy]` in base
// config.toml. The new Patch must strip those (so Codex profile-v2 doesn't
// reject the config) AND carry the model value forward into the sibling
// file.
func TestPatch_MigratesLegacyDatabricksProxyKeys(t *testing.T) {
	initial := `profile = "databricks-proxy"

[profiles.databricks-proxy]
model_provider = "databricks-proxy"
model = "custom-user-model"

[model_providers.databricks-proxy]
name = "Databricks Proxy"
base_url = "http://old-proxy:1234"
api_key = "databricks-proxy"
wire_api = "responses"

[projects.myapp]
trust_level = "trusted"
`
	m, configPath := setup(t, initial)

	err := m.Patch(PatchConfig{
		ProxyURL:      "http://127.0.0.1:9999",
		Model:         "databricks-gpt-5-5",
		ModelExplicit: false,
	})
	if err != nil {
		t.Fatal(err)
	}

	content := readConfig(t, configPath)
	if strings.Contains(content, `profile = "databricks-proxy"`) {
		t.Errorf("legacy root profile key must be stripped, got:\n%s", content)
	}
	if strings.Contains(content, "[profiles.databricks-proxy]") {
		t.Errorf("legacy [profiles.databricks-proxy] section must be stripped, got:\n%s", content)
	}
	if !strings.Contains(content, "[projects.myapp]") {
		t.Errorf("unrelated user section must survive migration, got:\n%s", content)
	}

	// Sibling: carries forward the legacy model value.
	sibling := readSibling(t, m)
	if !strings.Contains(sibling, `model = "custom-user-model"`) {
		t.Errorf("expected legacy model to be migrated into sibling file, got:\n%s", sibling)
	}
}

func TestPatch_PreservesUserModel(t *testing.T) {
	// Same flow as TestPatch_MigratesLegacyDatabricksProxyKeys but
	// asserts the preserve-if-present semantics from the user's angle: a
	// custom model in the legacy section migrates into the sibling
	// without being overwritten by the resolved fallback.
	initial := `profile = "databricks-proxy"

[profiles.databricks-proxy]
model_provider = "databricks-proxy"
model = "custom-user-model"
`
	m, _ := setup(t, initial)

	err := m.Patch(PatchConfig{
		ProxyURL:      "http://127.0.0.1:9999",
		Model:         "databricks-gpt-5-5",
		ModelExplicit: false,
	})
	if err != nil {
		t.Fatal(err)
	}

	sibling := readSibling(t, m)
	if !strings.Contains(sibling, `model = "custom-user-model"`) {
		t.Errorf("expected user model to be preserved in sibling, got:\n%s", sibling)
	}
}

func TestPatch_OverridesModelWhenExplicit(t *testing.T) {
	initial := `profile = "databricks-proxy"

[profiles.databricks-proxy]
model_provider = "databricks-proxy"
model = "custom-user-model"
`
	m, _ := setup(t, initial)

	err := m.Patch(PatchConfig{
		ProxyURL:      "http://127.0.0.1:9999",
		Model:         "databricks-gpt-5-4-mini",
		ModelExplicit: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	sibling := readSibling(t, m)
	if !strings.Contains(sibling, `model = "databricks-gpt-5-4-mini"`) {
		t.Errorf("expected explicit --model to win in sibling, got:\n%s", sibling)
	}
	if strings.Contains(sibling, `model = "custom-user-model"`) {
		t.Errorf("expected custom-user-model to be replaced in sibling, got:\n%s", sibling)
	}
}

// TestPatch_PreservesExistingSiblingModel verifies that if a sibling file
// already exists (user edited it, or a prior session wrote it), its model
// value is preserved when ModelExplicit=false.
func TestPatch_PreservesExistingSiblingModel(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	siblingPath := filepath.Join(dir, SiblingProfileName+".config.toml")

	// Seed sibling with a user-set model.
	if err := os.WriteFile(siblingPath, []byte(`model_provider = "databricks-proxy"
model = "user-picked-model"
`), 0o600); err != nil {
		t.Fatal(err)
	}

	m := NewManager(configPath)
	if err := m.Backup(); err != nil {
		t.Fatal(err)
	}

	err := m.Patch(PatchConfig{
		ProxyURL:      "http://127.0.0.1:9999",
		Model:         "databricks-gpt-5-5",
		ModelExplicit: false,
	})
	if err != nil {
		t.Fatal(err)
	}

	sibling := readSibling(t, m)
	if !strings.Contains(sibling, `model = "user-picked-model"`) {
		t.Errorf("expected existing sibling model to be preserved, got:\n%s", sibling)
	}
}

func TestRestore_RemovesAddedSections(t *testing.T) {
	// Start with a config that has NO databricks-proxy sections AND no
	// sibling file. Restore must remove everything we added.
	initial := `[projects.myapp]
sandbox_permissions = "full-auto"
`
	m, configPath := setup(t, initial)

	err := m.Patch(PatchConfig{
		ProxyURL: "http://127.0.0.1:9999",
		Model:    "databricks-gpt-5-5",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Verify patch added the provider section + sibling file.
	content := readConfig(t, configPath)
	if !strings.Contains(content, "[model_providers.databricks-proxy]") {
		t.Fatal("patch should have added provider section")
	}
	if _, err := os.Stat(m.SiblingPath()); err != nil {
		t.Fatalf("patch should have created sibling file: %v", err)
	}

	if err := m.Restore(); err != nil {
		t.Fatal(err)
	}

	content = readConfig(t, configPath)
	if strings.Contains(content, "[model_providers.databricks-proxy]") {
		t.Error("expected [model_providers.databricks-proxy] to be removed after restore")
	}
	// User section must survive.
	if !strings.Contains(content, "[projects.myapp]") {
		t.Error("expected [projects.myapp] to survive restore")
	}
	// Sibling file must be gone (it didn't exist pre-patch).
	if _, err := os.Stat(m.SiblingPath()); !os.IsNotExist(err) {
		t.Error("expected sibling file to be removed after restore")
	}
}

// TestRestore_PutsBackLegacyKeys verifies that when Patch strips legacy
// `profile = "databricks-proxy"` and `[profiles.databricks-proxy]` from
// base config, Restore puts them back. Symmetric round-trip — important
// when a pre-v0.7 databricks-codex left state behind and the user is just
// running a single session of the new wrapper.
func TestRestore_PutsBackLegacyKeys(t *testing.T) {
	initial := `profile = "databricks-proxy"

[profiles.databricks-proxy]
model_provider = "databricks-proxy"
model = "original-model"

[model_providers.databricks-proxy]
name = "Databricks Proxy"
base_url = "http://old-proxy:1234"
api_key = "databricks-proxy"
wire_api = "responses"

[projects.myapp]
sandbox_permissions = "full-auto"
`
	m, configPath := setup(t, initial)

	err := m.Patch(PatchConfig{
		ProxyURL:      "http://127.0.0.1:9999",
		Model:         "new-model",
		ModelExplicit: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := m.Restore(); err != nil {
		t.Fatal(err)
	}

	content := readConfig(t, configPath)
	if !strings.Contains(content, `profile = "databricks-proxy"`) {
		t.Errorf("expected legacy root profile to be restored, got:\n%s", content)
	}
	if !strings.Contains(content, "[profiles.databricks-proxy]") {
		t.Errorf("expected legacy [profiles.databricks-proxy] to be restored, got:\n%s", content)
	}
	if !strings.Contains(content, `model = "original-model"`) {
		t.Errorf("expected original model to be restored inside legacy section, got:\n%s", content)
	}
	if !strings.Contains(content, "[projects.myapp]") {
		t.Errorf("expected user section preserved, got:\n%s", content)
	}
	// Sibling file must be gone (it didn't exist pre-patch).
	if _, err := os.Stat(m.SiblingPath()); !os.IsNotExist(err) {
		t.Error("expected sibling file to be removed after restore")
	}
}

func TestRestore_PreservesUnmanagedContent(t *testing.T) {
	initial := `custom_key = "custom_value"

[projects.myapp]
sandbox_permissions = "full-auto"

[notice]
shown = true
`
	m, configPath := setup(t, initial)

	err := m.Patch(PatchConfig{
		ProxyURL:         "http://127.0.0.1:9999",
		Model:            "databricks-gpt-5-5",
		OTELLogsEndpoint: "http://127.0.0.1:9999/otel/v1/logs",
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := m.Restore(); err != nil {
		t.Fatal(err)
	}

	content := readConfig(t, configPath)
	if !strings.Contains(content, `custom_key = "custom_value"`) {
		t.Errorf("expected custom_key to survive, got:\n%s", content)
	}
	if !strings.Contains(content, "[projects.myapp]") {
		t.Errorf("expected [projects.myapp] to survive, got:\n%s", content)
	}
	if !strings.Contains(content, "[notice]") {
		t.Errorf("expected [notice] to survive, got:\n%s", content)
	}
	// OTEL section should be removed (it was absent before).
	if strings.Contains(content, "[otel]") {
		t.Errorf("expected [otel] to be removed after restore, got:\n%s", content)
	}
}

// TestRestore_PreservesExistingSibling verifies that if a sibling file
// existed pre-patch (e.g. user maintains one), Restore puts its original
// content back byte-for-byte, not just deletes it.
func TestRestore_PreservesExistingSibling(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	siblingPath := filepath.Join(dir, SiblingProfileName+".config.toml")
	originalSibling := `model_provider = "databricks-proxy"
model = "user-handcrafted"
# user comment
`
	if err := os.WriteFile(siblingPath, []byte(originalSibling), 0o600); err != nil {
		t.Fatal(err)
	}

	m := NewManager(configPath)
	if err := m.Backup(); err != nil {
		t.Fatal(err)
	}

	if err := m.Patch(PatchConfig{
		ProxyURL: "http://127.0.0.1:9999",
		Model:    "databricks-gpt-5-5",
	}); err != nil {
		t.Fatal(err)
	}

	if err := m.Restore(); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(siblingPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != originalSibling {
		t.Errorf("sibling restore did not preserve content byte-for-byte\nwant:\n%s\ngot:\n%s", originalSibling, string(got))
	}
}

func TestRestore_NoOriginalFile(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	m := NewManager(configPath)
	if err := m.Backup(); err != nil {
		t.Fatal(err)
	}

	err := m.Patch(PatchConfig{
		ProxyURL: "http://127.0.0.1:9999",
		Model:    "databricks-gpt-5-5",
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := m.Restore(); err != nil {
		t.Fatal(err)
	}

	// File should be removed since it didn't exist before.
	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		t.Error("expected config.toml to be removed after restore when it didn't exist before")
	}
	if _, err := os.Stat(m.SiblingPath()); !os.IsNotExist(err) {
		t.Error("expected sibling file to be removed after restore when it didn't exist before")
	}
}

func TestPatch_RespectsRootLevelModel(t *testing.T) {
	initial := `model = "databricks-gpt-5-3"

[projects."/Users/me/myproject"]
trust_level = "trusted"
`
	m, _ := setup(t, initial)

	// No --model flag (ModelExplicit=false), no model in sibling.
	// Should pick up the root-level model and write it into sibling.
	err := m.Patch(PatchConfig{
		ProxyURL:      "http://127.0.0.1:9999",
		Model:         "databricks-gpt-5-5",
		ModelExplicit: false,
	})
	if err != nil {
		t.Fatal(err)
	}

	sibling := readSibling(t, m)
	if !strings.Contains(sibling, `model = "databricks-gpt-5-3"`) {
		t.Errorf("expected root-level model to be carried into sibling, got:\n%s", sibling)
	}
	if strings.Contains(sibling, `model = "databricks-gpt-5-5"`) {
		t.Errorf("expected fallback model NOT to be used when root-level model exists, got:\n%s", sibling)
	}
}

func TestPatch_RootLevelModelOverriddenByExplicitFlag(t *testing.T) {
	initial := `model = "databricks-gpt-5-3"

[projects."/Users/me/myproject"]
trust_level = "trusted"
`
	m, _ := setup(t, initial)

	err := m.Patch(PatchConfig{
		ProxyURL:      "http://127.0.0.1:9999",
		Model:         "databricks-gpt-5-4-mini",
		ModelExplicit: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	sibling := readSibling(t, m)
	if !strings.Contains(sibling, `model = "databricks-gpt-5-4-mini"`) {
		t.Errorf("expected explicit --model to win over root-level model in sibling, got:\n%s", sibling)
	}
}

func TestPatch_WithOTEL(t *testing.T) {
	m, configPath := setup(t, "")

	err := m.Patch(PatchConfig{
		ProxyURL:         "http://127.0.0.1:9999",
		Model:            "databricks-gpt-5-5",
		OTELLogsEndpoint: "http://127.0.0.1:9999/otel/v1/logs",
	})
	if err != nil {
		t.Fatal(err)
	}

	content := readConfig(t, configPath)
	if !strings.Contains(content, "[otel]") {
		t.Error("expected [otel] section")
	}
	if !strings.Contains(content, `endpoint = "http://127.0.0.1:9999/otel/v1/logs"`) {
		t.Error("expected OTEL endpoint in config")
	}
}

func TestPatch_WithOTELMetricsOnly(t *testing.T) {
	m, configPath := setup(t, "")

	err := m.Patch(PatchConfig{
		ProxyURL:            "http://127.0.0.1:9999",
		Model:               "databricks-gpt-5-5",
		OTELMetricsEndpoint: "http://127.0.0.1:9999/otel/v1/metrics",
	})
	if err != nil {
		t.Fatal(err)
	}

	content := readConfig(t, configPath)
	if !strings.Contains(content, "[otel]") {
		t.Error("expected [otel] section")
	}
	if !strings.Contains(content, `metrics_exporter = { otlp-http = { endpoint = "http://127.0.0.1:9999/otel/v1/metrics"`) {
		t.Errorf("expected metrics_exporter key in [otel] section, got:\n%s", content)
	}
	if strings.Contains(content, `endpoint = "http://127.0.0.1:9999/otel/v1/logs"`) {
		t.Errorf("expected no logs exporter when only metrics endpoint provided, got:\n%s", content)
	}
}

func TestPatch_WithBothOTELExporters(t *testing.T) {
	m, configPath := setup(t, "")

	err := m.Patch(PatchConfig{
		ProxyURL:            "http://127.0.0.1:9999",
		Model:               "databricks-gpt-5-5",
		OTELLogsEndpoint:    "http://127.0.0.1:9999/otel/v1/logs",
		OTELMetricsEndpoint: "http://127.0.0.1:9999/otel/v1/metrics",
	})
	if err != nil {
		t.Fatal(err)
	}

	content := readConfig(t, configPath)
	if !strings.Contains(content, `endpoint = "http://127.0.0.1:9999/otel/v1/logs"`) {
		t.Errorf("expected logs endpoint in [otel] section, got:\n%s", content)
	}
	if !strings.Contains(content, `endpoint = "http://127.0.0.1:9999/otel/v1/metrics"`) {
		t.Errorf("expected metrics endpoint in [otel] section, got:\n%s", content)
	}
}

func TestPatch_NoOTELSectionWhenBothEmpty(t *testing.T) {
	m, configPath := setup(t, "")

	err := m.Patch(PatchConfig{
		ProxyURL: "http://127.0.0.1:9999",
		Model:    "databricks-gpt-5-5",
	})
	if err != nil {
		t.Fatal(err)
	}

	content := readConfig(t, configPath)
	if strings.Contains(content, "[otel]") {
		t.Errorf("expected no [otel] section when both endpoints empty, got:\n%s", content)
	}
}

func TestPatch_OTELReWriteAddsMetricsExporter(t *testing.T) {
	m, configPath := setup(t, "")

	if err := m.Patch(PatchConfig{
		ProxyURL:         "http://127.0.0.1:9999",
		Model:            "databricks-gpt-5-5",
		OTELLogsEndpoint: "http://127.0.0.1:9999/otel/v1/logs",
	}); err != nil {
		t.Fatal(err)
	}

	if err := m.Patch(PatchConfig{
		ProxyURL:            "http://127.0.0.1:9999",
		Model:               "databricks-gpt-5-5",
		OTELLogsEndpoint:    "http://127.0.0.1:9999/otel/v1/logs",
		OTELMetricsEndpoint: "http://127.0.0.1:9999/otel/v1/metrics",
	}); err != nil {
		t.Fatal(err)
	}

	content := readConfig(t, configPath)
	if !strings.Contains(content, `metrics_exporter = { otlp-http = { endpoint = "http://127.0.0.1:9999/otel/v1/metrics"`) {
		t.Errorf("expected metrics_exporter after re-patch, got:\n%s", content)
	}
	if !strings.Contains(content, `endpoint = "http://127.0.0.1:9999/otel/v1/logs"`) {
		t.Errorf("expected logs endpoint to still be present after re-patch, got:\n%s", content)
	}
}

func TestPatch_RemovesOTELSectionWhenEndpointsEmpty(t *testing.T) {
	m, configPath := setup(t, "")

	if err := m.Patch(PatchConfig{
		ProxyURL:            "http://127.0.0.1:9999",
		Model:               "databricks-gpt-5-5",
		OTELLogsEndpoint:    "http://127.0.0.1:9999/otel/v1/logs",
		OTELMetricsEndpoint: "http://127.0.0.1:9999/otel/v1/metrics",
	}); err != nil {
		t.Fatal(err)
	}

	before := readConfig(t, configPath)
	if !strings.Contains(before, "[otel]") {
		t.Fatalf("setup error: expected [otel] section after first patch, got:\n%s", before)
	}

	if err := m.Patch(PatchConfig{
		ProxyURL: "http://127.0.0.1:9999",
		Model:    "databricks-gpt-5-5",
	}); err != nil {
		t.Fatal(err)
	}

	after := readConfig(t, configPath)
	if strings.Contains(after, "[otel]") {
		t.Errorf("expected [otel] section to be removed after empty-endpoints patch, got:\n%s", after)
	}
	if strings.Contains(after, "exporter = { otlp-http") {
		t.Errorf("expected no stale exporter line after removal, got:\n%s", after)
	}
	if strings.Contains(after, "metrics_exporter") {
		t.Errorf("expected no stale metrics_exporter line after removal, got:\n%s", after)
	}
	// Provider section should survive.
	if !strings.Contains(after, "[model_providers.databricks-proxy]") {
		t.Errorf("expected [model_providers.databricks-proxy] to survive [otel] removal, got:\n%s", after)
	}
}

func TestUpdateProxyURL(t *testing.T) {
	m, configPath := setup(t, "")

	err := m.Patch(PatchConfig{
		ProxyURL: "http://127.0.0.1:9999",
		Model:    "databricks-gpt-5-5",
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := m.UpdateProxyURL("http://127.0.0.1:8888"); err != nil {
		t.Fatal(err)
	}

	content := readConfig(t, configPath)
	if !strings.Contains(content, `base_url = "http://127.0.0.1:8888"`) {
		t.Errorf("expected updated base_url, got:\n%s", content)
	}
}

func TestRestoreFromBackup(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	backupPath := configPath + ".databricks-codex-backup"

	original := `profile = "myprofile"
`
	if err := os.WriteFile(backupPath, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(`profile = "databricks-proxy"`), 0o600); err != nil {
		t.Fatal(err)
	}

	m := NewManager(configPath)
	restored := m.RestoreFromBackup()
	if !restored {
		t.Error("expected RestoreFromBackup to return true")
	}

	content := readConfig(t, configPath)
	if !strings.Contains(content, `profile = "myprofile"`) {
		t.Errorf("expected original content restored from backup, got:\n%s", content)
	}

	if _, err := os.Stat(backupPath); !os.IsNotExist(err) {
		t.Error("expected backup file to be removed after restore")
	}
}

// TestRestore_PutsBackUserRootModelProvider verifies that a user's
// pre-existing root `model_provider` and `model` keys are restored after
// our session ends — the GUI/raw-codex fix overwrites them at session
// start, so Restore must put them back.
func TestRestore_PutsBackUserRootModelProvider(t *testing.T) {
	initial := `model_provider = "openai"
model = "gpt-5"

[projects.myapp]
trust_level = "trusted"
`
	m, configPath := setup(t, initial)

	if err := m.Patch(PatchConfig{
		ProxyURL: "http://127.0.0.1:9999",
		Model:    "databricks-gpt-5-5",
	}); err != nil {
		t.Fatal(err)
	}

	// During the session: our values are active.
	mid := readConfig(t, configPath)
	if !strings.Contains(mid, `model_provider = "databricks-proxy"`) {
		t.Fatalf("expected our model_provider mid-session, got:\n%s", mid)
	}

	if err := m.Restore(); err != nil {
		t.Fatal(err)
	}

	content := readConfig(t, configPath)
	if !strings.Contains(content, `model_provider = "openai"`) {
		t.Errorf("expected user's model_provider to be restored, got:\n%s", content)
	}
	if !strings.Contains(content, `model = "gpt-5"`) {
		t.Errorf("expected user's model to be restored, got:\n%s", content)
	}
	if !strings.Contains(content, "[projects.myapp]") {
		t.Errorf("expected user section to survive, got:\n%s", content)
	}
}

// TestRestore_RemovesAddedRootKeys verifies that root keys we added (no
// originals) are fully removed on Restore.
func TestRestore_RemovesAddedRootKeys(t *testing.T) {
	initial := `[projects.myapp]
trust_level = "trusted"
`
	m, configPath := setup(t, initial)

	if err := m.Patch(PatchConfig{
		ProxyURL: "http://127.0.0.1:9999",
		Model:    "databricks-gpt-5-5",
	}); err != nil {
		t.Fatal(err)
	}

	if err := m.Restore(); err != nil {
		t.Fatal(err)
	}

	content := readConfig(t, configPath)
	if strings.Contains(content, "model_provider") {
		t.Errorf("expected model_provider to be removed (was absent pre-patch), got:\n%s", content)
	}
	if strings.Contains(content, "model = ") {
		t.Errorf("expected model to be removed (was absent pre-patch), got:\n%s", content)
	}
}

// TestRestoreFromBackup_RemovesOrphanSibling verifies that crash recovery
// also removes a sibling file that the crashed session wrote (no backup
// means the sibling wasn't there before).
func TestRestoreFromBackup_RemovesOrphanSibling(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	siblingPath := filepath.Join(dir, SiblingProfileName+".config.toml")
	backupPath := configPath + ".databricks-codex-backup"

	// Crashed session left: original backed up, sibling written, no sibling backup.
	if err := os.WriteFile(backupPath, []byte(`# original
`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(`# patched
`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(siblingPath, []byte(`model_provider = "databricks-proxy"
`), 0o600); err != nil {
		t.Fatal(err)
	}

	m := NewManager(configPath)
	if !m.RestoreFromBackup() {
		t.Error("expected RestoreFromBackup to return true")
	}
	if _, err := os.Stat(siblingPath); !os.IsNotExist(err) {
		t.Error("expected orphan sibling file to be removed during crash recovery")
	}
}
