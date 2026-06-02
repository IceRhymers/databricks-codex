package tomlconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeConfig seeds ~/.codex/config.toml with the given content and
// returns a Manager pointed at it. No Backup() call — there is no
// Backup in the write-once model.
func writeConfig(t *testing.T, initialContent string) (*Manager, string) {
	t.Helper()
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	if initialContent != "" {
		if err := os.WriteFile(configPath, []byte(initialContent), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return NewManager(configPath), configPath
}

func readConfig(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func readSibling(t *testing.T, m *Manager) string {
	t.Helper()
	data, err := os.ReadFile(m.SiblingPath())
	if err != nil {
		t.Fatalf("read sibling file: %v", err)
	}
	return string(data)
}

// TestPatch_WritesV2Layout verifies that an empty starting config
// produces the expected profile-v2 layout: provider section in base,
// root model_provider+model override, sibling file, supports_websockets
// off, no legacy keys.
func TestPatch_WritesV2Layout(t *testing.T) {
	m, configPath := writeConfig(t, "")

	if err := m.Patch(PatchConfig{
		ProxyURL: "http://127.0.0.1:9999",
		Model:    "databricks-gpt-5-5",
	}); err != nil {
		t.Fatal(err)
	}

	content := readConfig(t, configPath)

	// Provider section + base_url.
	if !strings.Contains(content, "[model_providers.databricks-proxy]") {
		t.Errorf("expected provider section in base, got:\n%s", content)
	}
	if !strings.Contains(content, `base_url = "http://127.0.0.1:9999"`) {
		t.Errorf("expected base_url in provider section, got:\n%s", content)
	}

	// NO legacy v1 layout.
	if strings.Contains(content, `profile = "databricks-proxy"`) {
		t.Errorf("v2: base config must NOT contain legacy root profile key, got:\n%s", content)
	}
	if strings.Contains(content, "[profiles.databricks-proxy]") {
		t.Errorf("v2: base config must NOT contain legacy [profiles.databricks-proxy] table, got:\n%s", content)
	}

	// Sibling file with profile-v2 layer.
	sibling := readSibling(t, m)
	if !strings.Contains(sibling, `model_provider = "databricks-proxy"`) {
		t.Errorf("expected model_provider in sibling, got:\n%s", sibling)
	}
	if !strings.Contains(sibling, `model = "databricks-gpt-5-5"`) {
		t.Errorf("expected model in sibling, got:\n%s", sibling)
	}

	// supports_websockets opt-out (Databricks AI Gateway is SSE-only).
	if !strings.Contains(content, "supports_websockets = false") {
		t.Errorf("provider section must set supports_websockets = false, got:\n%s", content)
	}

	// Root-level model_provider + model — the GUI/raw-codex fix
	// (openai/codex#13041).
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
	m, configPath := writeConfig(t, initial)

	if err := m.Patch(PatchConfig{
		ProxyURL: "http://127.0.0.1:9999",
		Model:    "databricks-gpt-5-5",
	}); err != nil {
		t.Fatal(err)
	}

	content := readConfig(t, configPath)
	// User's profile root key (NOT databricks-proxy) must survive.
	if !strings.Contains(content, `profile = "myprofile"`) {
		t.Errorf("expected user's root profile to be preserved, got:\n%s", content)
	}
	if !strings.Contains(content, "[projects.myapp]") {
		t.Error("expected [projects.myapp] to be preserved")
	}
	if !strings.Contains(content, `sandbox_permissions = "full-auto"`) {
		t.Error("expected sandbox_permissions to be preserved")
	}
	if !strings.Contains(content, "[profiles.myprofile]") {
		t.Error("expected [profiles.myprofile] to be preserved")
	}
}

// TestPatch_OverwritesUserRootModelProvider documents the write-once
// trade-off: we DO clobber a user's root model_provider/model with our
// own values (otherwise the GUI/raw-codex fix wouldn't take effect).
// This is intentional — Restore is not part of the model — but the test
// pins the behavior so the contract is explicit.
func TestPatch_OverwritesUserRootModelProvider(t *testing.T) {
	initial := `model_provider = "openai"
model = "gpt-5"
`
	m, configPath := writeConfig(t, initial)

	if err := m.Patch(PatchConfig{
		ProxyURL: "http://127.0.0.1:9999",
		Model:    "databricks-gpt-5-5",
	}); err != nil {
		t.Fatal(err)
	}

	content := readConfig(t, configPath)
	if !strings.Contains(content, `model_provider = "databricks-proxy"`) {
		t.Errorf("expected our model_provider to win, got:\n%s", content)
	}
	if strings.Contains(content, `model_provider = "openai"`) {
		t.Errorf("expected user's openai model_provider to be overwritten, got:\n%s", content)
	}
}

// TestPatch_MigratesLegacyDatabricksProxyKeys covers the upgrade path: a
// user previously ran an old databricks-codex that left
// `profile = "databricks-proxy"` + `[profiles.databricks-proxy]` in base
// config.toml. The new Patch strips both (so Codex profile-v2 accepts
// the config) AND carries the model value forward into the sibling.
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
	m, configPath := writeConfig(t, initial)

	if err := m.Patch(PatchConfig{
		ProxyURL:      "http://127.0.0.1:9999",
		Model:         "databricks-gpt-5-5",
		ModelExplicit: false,
	}); err != nil {
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

	// Sibling: carries forward the legacy model value (preserve-if-present).
	sibling := readSibling(t, m)
	if !strings.Contains(sibling, `model = "custom-user-model"`) {
		t.Errorf("expected legacy model to be migrated into sibling file, got:\n%s", sibling)
	}
}

func TestPatch_OverridesModelWhenExplicit(t *testing.T) {
	initial := `profile = "databricks-proxy"

[profiles.databricks-proxy]
model_provider = "databricks-proxy"
model = "custom-user-model"
`
	m, _ := writeConfig(t, initial)

	if err := m.Patch(PatchConfig{
		ProxyURL:      "http://127.0.0.1:9999",
		Model:         "databricks-gpt-5-4-mini",
		ModelExplicit: true,
	}); err != nil {
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

// TestPatch_PreservesExistingSiblingModel: if a sibling file already
// exists, its model value wins over the resolved fallback (ModelExplicit
// = false).
func TestPatch_PreservesExistingSiblingModel(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	siblingPath := filepath.Join(dir, SiblingProfileName+".config.toml")

	if err := os.WriteFile(siblingPath, []byte(`model_provider = "databricks-proxy"
model = "user-picked-model"
`), 0o600); err != nil {
		t.Fatal(err)
	}

	m := NewManager(configPath)
	if err := m.Patch(PatchConfig{
		ProxyURL:      "http://127.0.0.1:9999",
		Model:         "databricks-gpt-5-5",
		ModelExplicit: false,
	}); err != nil {
		t.Fatal(err)
	}

	sibling := readSibling(t, m)
	if !strings.Contains(sibling, `model = "user-picked-model"`) {
		t.Errorf("expected existing sibling model to be preserved, got:\n%s", sibling)
	}
}

func TestPatch_RespectsRootLevelModel(t *testing.T) {
	initial := `model = "databricks-gpt-5-3"

[projects."/Users/me/myproject"]
trust_level = "trusted"
`
	m, _ := writeConfig(t, initial)

	if err := m.Patch(PatchConfig{
		ProxyURL:      "http://127.0.0.1:9999",
		Model:         "databricks-gpt-5-5",
		ModelExplicit: false,
	}); err != nil {
		t.Fatal(err)
	}

	sibling := readSibling(t, m)
	if !strings.Contains(sibling, `model = "databricks-gpt-5-3"`) {
		t.Errorf("expected root-level model to be carried into sibling, got:\n%s", sibling)
	}
}

func TestPatch_RootLevelModelOverriddenByExplicitFlag(t *testing.T) {
	initial := `model = "databricks-gpt-5-3"
`
	m, _ := writeConfig(t, initial)

	if err := m.Patch(PatchConfig{
		ProxyURL:      "http://127.0.0.1:9999",
		Model:         "databricks-gpt-5-4-mini",
		ModelExplicit: true,
	}); err != nil {
		t.Fatal(err)
	}

	sibling := readSibling(t, m)
	if !strings.Contains(sibling, `model = "databricks-gpt-5-4-mini"`) {
		t.Errorf("expected explicit --model to win over root-level model in sibling, got:\n%s", sibling)
	}
}

func TestPatch_WithOTEL(t *testing.T) {
	m, configPath := writeConfig(t, "")
	if err := m.Patch(PatchConfig{
		ProxyURL:         "http://127.0.0.1:9999",
		Model:            "databricks-gpt-5-5",
		OTELLogsEndpoint: "http://127.0.0.1:9999/otel/v1/logs",
	}); err != nil {
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
	m, configPath := writeConfig(t, "")
	if err := m.Patch(PatchConfig{
		ProxyURL:            "http://127.0.0.1:9999",
		Model:               "databricks-gpt-5-5",
		OTELMetricsEndpoint: "http://127.0.0.1:9999/otel/v1/metrics",
	}); err != nil {
		t.Fatal(err)
	}
	content := readConfig(t, configPath)
	if !strings.Contains(content, `metrics_exporter = { otlp-http = { endpoint = "http://127.0.0.1:9999/otel/v1/metrics"`) {
		t.Errorf("expected metrics_exporter key, got:\n%s", content)
	}
	if strings.Contains(content, `endpoint = "http://127.0.0.1:9999/otel/v1/logs"`) {
		t.Errorf("expected no logs exporter when only metrics endpoint provided, got:\n%s", content)
	}
}

func TestPatch_WithBothOTELExporters(t *testing.T) {
	m, configPath := writeConfig(t, "")
	if err := m.Patch(PatchConfig{
		ProxyURL:            "http://127.0.0.1:9999",
		Model:               "databricks-gpt-5-5",
		OTELLogsEndpoint:    "http://127.0.0.1:9999/otel/v1/logs",
		OTELMetricsEndpoint: "http://127.0.0.1:9999/otel/v1/metrics",
	}); err != nil {
		t.Fatal(err)
	}
	content := readConfig(t, configPath)
	if !strings.Contains(content, `endpoint = "http://127.0.0.1:9999/otel/v1/logs"`) {
		t.Errorf("expected logs endpoint, got:\n%s", content)
	}
	if !strings.Contains(content, `endpoint = "http://127.0.0.1:9999/otel/v1/metrics"`) {
		t.Errorf("expected metrics endpoint, got:\n%s", content)
	}
}

func TestPatch_NoOTELSectionWhenBothEmpty(t *testing.T) {
	m, configPath := writeConfig(t, "")
	if err := m.Patch(PatchConfig{
		ProxyURL: "http://127.0.0.1:9999",
		Model:    "databricks-gpt-5-5",
	}); err != nil {
		t.Fatal(err)
	}
	content := readConfig(t, configPath)
	if strings.Contains(content, "[otel]") {
		t.Errorf("expected no [otel] section when both endpoints empty, got:\n%s", content)
	}
}

func TestPatch_OTELReWriteAddsMetricsExporter(t *testing.T) {
	m, configPath := writeConfig(t, "")
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
	m, configPath := writeConfig(t, "")
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
	if !strings.Contains(after, "[model_providers.databricks-proxy]") {
		t.Errorf("expected provider section to survive [otel] removal, got:\n%s", after)
	}
}

func TestUpdateProxyURL(t *testing.T) {
	m, configPath := writeConfig(t, "")
	if err := m.Patch(PatchConfig{
		ProxyURL: "http://127.0.0.1:9999",
		Model:    "databricks-gpt-5-5",
	}); err != nil {
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
