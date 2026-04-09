package main

import (
	"context"
	"os"
	"os/exec"

	"github.com/IceRhymers/databricks-claude/pkg/childproc"
)

// OTEL environment variable keys for Codex telemetry. These are set as env
// vars by main.go (not written to settings.json like databricks-claude).
var otelKeys = []string{
	"OTEL_EXPORTER_OTLP_METRICS_ENDPOINT",
	"OTEL_EXPORTER_OTLP_METRICS_HEADERS",
	"OTEL_METRICS_EXPORTER",
	"OTEL_EXPORTER_OTLP_METRICS_PROTOCOL",
	"OTEL_METRIC_EXPORT_INTERVAL",
	"OTEL_EXPORTER_OTLP_LOGS_ENDPOINT",
	"OTEL_EXPORTER_OTLP_LOGS_HEADERS",
	"OTEL_EXPORTER_OTLP_LOGS_PROTOCOL",
	"OTEL_LOGS_EXPORTER",
	"OTEL_LOGS_EXPORT_INTERVAL",
}

// RunCodex starts the codex CLI as a child process with the supplied arguments
// and waits for it to exit. OTEL environment variables are expected to be set
// on os.Environ by main.go before calling this function. The API key and base
// URL are configured via config.toml (not environment variables).
// DATABRICKS_CODEX_MANAGED=1 is injected so that child codex sessions skip
// headless-ensure/release hooks (prevents double-firing).
func RunCodex(ctx context.Context, args []string) (int, error) {
	os.Setenv("DATABRICKS_CODEX_MANAGED", "1")
	return childproc.Run(ctx, childproc.Config{
		BinaryName: "codex",
		Args:       args,
	})
}

// ForwardSignals sets up SIGINT/SIGTERM forwarding from the parent to cmd's
// process. The returned cancel function stops the forwarding goroutine.
func ForwardSignals(cmd *exec.Cmd) (cancel func()) {
	return childproc.ForwardSignals(cmd)
}
