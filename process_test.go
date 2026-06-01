package main

import (
	"context"
	"testing"
)

// TestRunCodex_BinaryName verifies RunCodex uses "codex" as the binary name.
// We don't actually run a subprocess here — we just confirm the function exists
// and returns an error when "codex" isn't on PATH.
func TestRunCodex_NotOnPath(t *testing.T) {
	// Save PATH and set to empty to ensure codex isn't found.
	t.Setenv("PATH", "")

	_, err := RunCodex(context.Background(), []string{"--help"})
	if err == nil {
		t.Error("expected error when codex binary not on PATH, got nil")
	}
}

// TestOtelKeys verifies the OTEL environment variable list is populated.
func TestOtelKeys(t *testing.T) {
	if len(otelKeys) == 0 {
		t.Error("expected otelKeys to be non-empty")
	}

	expected := map[string]bool{
		"OTEL_EXPORTER_OTLP_METRICS_ENDPOINT": true,
		"OTEL_EXPORTER_OTLP_METRICS_HEADERS":  true,
		"OTEL_METRICS_EXPORTER":               true,
		"OTEL_EXPORTER_OTLP_LOGS_ENDPOINT":    true,
		"OTEL_LOGS_EXPORTER":                  true,
	}

	found := map[string]bool{}
	for _, k := range otelKeys {
		found[k] = true
	}

	for k := range expected {
		if !found[k] {
			t.Errorf("expected otelKeys to contain %q", k)
		}
	}
}

func TestWithProfileFlag(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{
			name: "empty args get profile prepended",
			in:   nil,
			want: []string{"--profile", "databricks-proxy"},
		},
		{
			name: "regular codex args get profile prepended",
			in:   []string{"exec", "hello"},
			want: []string{"--profile", "databricks-proxy", "exec", "hello"},
		},
		{
			name: "user --profile is respected",
			in:   []string{"--profile", "work", "exec"},
			want: []string{"--profile", "work", "exec"},
		},
		{
			name: "user --profile=value is respected",
			in:   []string{"--profile=work", "exec"},
			want: []string{"--profile=work", "exec"},
		},
		{
			name: "user -p short form is respected",
			in:   []string{"-p", "work"},
			want: []string{"-p", "work"},
		},
		{
			name: "profile after -- separator does not count",
			in:   []string{"exec", "--", "--profile", "work"},
			want: []string{"--profile", "databricks-proxy", "exec", "--", "--profile", "work"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := withProfileFlag(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("len mismatch: got %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("element %d: got %q, want %q (full: %v)", i, got[i], tc.want[i], got)
				}
			}
		})
	}
}
