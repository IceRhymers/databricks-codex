package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strings"
)

// Version is set at build time via -ldflags.
var Version = "dev"

func main() {
	verbose, version, showHelp, printEnv, noOtel, otelTable, otelTableSet, upstream, logFile, profile, otel, codexArgs := parseArgs(os.Args[1:])

	if showHelp {
		handleHelp(upstream)
		os.Exit(0)
	}

	if version {
		fmt.Printf("databricks-codex %s\n", Version)
		os.Exit(0)
	}

	// Default: discard all logs (silent wrapper).
	log.SetOutput(io.Discard)

	if verbose {
		log.SetOutput(os.Stderr)
	}
	if logFile != "" {
		f, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
		if err != nil {
			log.SetOutput(os.Stderr)
			log.Fatalf("databricks-codex: cannot open log file %q: %v", logFile, err)
		}
		defer f.Close()
		if verbose {
			log.SetOutput(io.MultiWriter(os.Stderr, f))
		} else {
			log.SetOutput(f)
		}
	}

	// --- Resolve profile ---
	if profile == "" {
		profile = os.Getenv("DATABRICKS_CONFIG_PROFILE")
	}
	if profile == "" {
		profile = "DEFAULT"
	}
	log.Printf("databricks-codex: using profile: %s", profile)

	// --- Seed token cache ---
	tp := NewTokenProvider("", profile)
	initialToken, err := tp.Token(context.Background())
	if err != nil {
		log.Fatalf("databricks-codex: failed to fetch initial token: %v", err)
	}

	// --- Discover host + construct gateway URL ---
	host, err := DiscoverHost("", profile)
	if err != nil {
		log.Fatalf("databricks-codex: failed to discover host: %v\nRun 'databricks auth login' first", err)
	}
	log.Printf("databricks-codex: discovered host: %s", host)

	gatewayURL := upstream
	if gatewayURL == "" {
		gatewayURL = ConstructGatewayURL(host, initialToken)
	}
	log.Printf("databricks-codex: gateway URL: %s", gatewayURL)

	// --- OTEL tables ---
	otelMetricsTable := "main.codex_telemetry.codex_otel_metrics"
	if otelTableSet {
		otelMetricsTable = otelTable
	}
	otelLogsTable := deriveLogsTable(otelMetricsTable)
	if noOtel {
		otel = false
	}

	// --- Print env and exit if requested ---
	if printEnv {
		handlePrintEnv(host, gatewayURL, initialToken, profile)
		os.Exit(0)
	}

	// Verify codex is on PATH before starting proxy.
	if _, err := exec.LookPath("codex"); err != nil {
		log.Fatalf("databricks-codex: codex binary not found on PATH — install from https://openai.com/codex")
	}

	// --- Determine OTEL upstream ---
	otelUpstream := ""
	if otel {
		otelUpstream = host
		log.Printf("databricks-codex: OTEL enabled, upstream: %s", otelUpstream)
	}

	// --- Start local proxy so the token stays fresh for the entire session ---
	// The proxy uses tokencache to refresh the Databricks OAuth token automatically
	// (5-min buffer before expiry). Codex talks to the proxy via config.toml;
	// the proxy injects a fresh Bearer token on every outbound request/WebSocket
	// connection to the AI Gateway.
	proxyHandler := NewProxyServer(&ProxyConfig{
		InferenceUpstream: gatewayURL,
		OTELUpstream:      otelUpstream,
		UCMetricsTable:    otelMetricsTable,
		UCLogsTable:       otelLogsTable,
		TokenProvider:     tp,
		Verbose:           verbose,
	})
	listener, err := StartProxy(proxyHandler)
	if err != nil {
		log.Fatalf("databricks-codex: failed to start proxy: %v", err)
	}
	defer listener.Close()
	proxyAddr := "http://" + listener.Addr().String()
	log.Printf("databricks-codex: local proxy %s -> %s", proxyAddr, gatewayURL)

	// --- Patch config.toml to point Codex at the local proxy ---
	// This is the Codex equivalent of databricks-claude patching settings.json.
	// The proxy URL is written as a model_provider in config.toml with
	// wire_api = "responses" so Codex uses the Responses API via WebSocket.
	cm := NewConfigManager()
	if err := cm.Setup(proxyAddr, "databricks-gpt-5-4"); err != nil {
		log.Fatalf("databricks-codex: failed to patch config.toml: %v", err)
	}

	// Set OPENAI_API_KEY as a placeholder — the proxy overwrites the
	// Authorization header with a live Databricks token per request.
	os.Setenv("OPENAI_API_KEY", "databricks-proxy")

	// --- Inject OTEL env vars when enabled ---
	if otel {
		otelBase := proxyAddr + "/otel"
		os.Setenv("OTEL_EXPORTER_OTLP_METRICS_ENDPOINT", otelBase+"/v1/metrics")
		os.Setenv("OTEL_EXPORTER_OTLP_METRICS_HEADERS", "content-type=application/x-protobuf")
		os.Setenv("OTEL_METRICS_EXPORTER", "otlp")
		os.Setenv("OTEL_EXPORTER_OTLP_METRICS_PROTOCOL", "http/protobuf")
		os.Setenv("OTEL_METRIC_EXPORT_INTERVAL", "10000")
		os.Setenv("OTEL_EXPORTER_OTLP_LOGS_ENDPOINT", otelBase+"/v1/logs")
		os.Setenv("OTEL_EXPORTER_OTLP_LOGS_HEADERS", "content-type=application/x-protobuf")
		os.Setenv("OTEL_EXPORTER_OTLP_LOGS_PROTOCOL", "http/protobuf")
		os.Setenv("OTEL_LOGS_EXPORTER", "otlp")
		os.Setenv("OTEL_LOGS_EXPORT_INTERVAL", "5000")
		log.Printf("databricks-codex: OTEL env vars set (metrics table: %s, logs table: %s)", otelMetricsTable, otelLogsTable)
	}

	log.Printf("databricks-codex: launching codex")

	// --- Run codex as a child process (parent stays alive to serve the proxy) ---
	exitCode, err := RunCodex(context.Background(), codexArgs)

	// Explicitly restore config.toml before exiting. This is NOT deferred
	// because os.Exit() skips deferred functions — we must restore before
	// exit to avoid leaving config.toml pointing at a dead proxy.
	cm.Restore()

	if err != nil {
		log.Fatalf("databricks-codex: codex failed: %v", err)
	}
	os.Exit(exitCode)
}

// parseArgs separates databricks-codex flags from codex flags.
func parseArgs(args []string) (verbose bool, version bool, showHelp bool, printEnv bool, noOtel bool, otelTable string, otelTableSet bool, upstream string, logFile string, profile string, otel bool, codexArgs []string) {
	otelTable = "main.codex_telemetry.codex_otel_metrics" // default

	knownFlags := map[string]bool{
		"--verbose":    true,
		"--version":    true,
		"--help":       true,
		"--print-env":  true,
		"--no-otel":    true,
		"--otel":       true,
		"--otel-table": true,
		"--upstream":   true,
		"--log-file":   true,
		"--profile":    true,
	}

	i := 0
	for i < len(args) {
		arg := args[i]

		// Explicit separator: everything after "--" goes to codex.
		if arg == "--" {
			codexArgs = append(codexArgs, args[i+1:]...)
			return
		}

		if arg == "-h" {
			showHelp = true
			i++
			continue
		}
		if arg == "-v" {
			verbose = true
			i++
			continue
		}

		if strings.HasPrefix(arg, "--") {
			name := arg
			value := ""
			if eqIdx := strings.Index(arg, "="); eqIdx >= 0 {
				name = arg[:eqIdx]
				value = arg[eqIdx+1:]
			}

			if knownFlags[name] {
				switch name {
				case "--otel-table":
					if value != "" {
						otelTable = value
						otelTableSet = true
					} else if i+1 < len(args) {
						i++
						otelTable = args[i]
						otelTableSet = true
					}
				case "--upstream":
					if value != "" {
						upstream = value
					} else if i+1 < len(args) {
						i++
						upstream = args[i]
					}
				case "--log-file":
					if value != "" {
						logFile = value
					} else if i+1 < len(args) {
						i++
						logFile = args[i]
					}
				case "--profile":
					if value != "" {
						profile = value
					} else if i+1 < len(args) {
						i++
						profile = args[i]
					}
				case "--verbose":
					verbose = true
				case "--version":
					version = true
				case "--help":
					showHelp = true
				case "--print-env":
					printEnv = true
				case "--otel":
					otel = true
				case "--no-otel":
					noOtel = true
				}
				i++
				continue
			}
		}

		// Not a known flag — pass through to codex.
		codexArgs = append(codexArgs, arg)
		i++
	}
	return
}

// handleHelp prints the databricks-codex help section, then execs codex --help.
func handleHelp(upstreamBinary string) {
	fmt.Printf(`databricks-codex v%s — Databricks AI Gateway wrapper for OpenAI Codex CLI

Patches ~/.codex/config.toml and runs a local proxy so the Codex CLI
authenticates through a Databricks AI Gateway endpoint with live token refresh.

Usage:
  databricks-codex [databricks-codex flags] [codex flags] [codex args]

Databricks-Codex Flags:
  --profile string      Databricks CLI profile (default: DATABRICKS_CONFIG_PROFILE env or "DEFAULT")
  --upstream string     Override the AI Gateway URL (default: auto-discovered)
  --print-env           Print resolved configuration and exit (token redacted)
  --verbose, -v         Enable debug logging to stderr
  --log-file string     Write debug logs to a file (combinable with --verbose)
  --otel                Enable OpenTelemetry telemetry proxying
  --no-otel             Disable OpenTelemetry for this session
  --otel-table string   Unity Catalog table for OTEL metrics (default: main.codex_telemetry.codex_otel_metrics)
  --version             Print version and exit
  --help, -h            Show this help message

────────────────────────────────────────────────────────────────────────────────
Codex CLI Options:
`, Version)

	claudeBin := upstreamBinary
	if claudeBin == "" {
		if p, err := exec.LookPath("codex"); err == nil {
			claudeBin = p
		}
	}

	if claudeBin == "" {
		fmt.Println("(codex binary not found on PATH — install from https://openai.com/codex)")
		return
	}

	var buf bytes.Buffer
	cmd := exec.Command(claudeBin, "--help")
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	_ = cmd.Run()
	fmt.Print(buf.String())
}

// handlePrintEnv prints resolved configuration with the token redacted.
func handlePrintEnv(databricksHost, openaiBaseURL, token, profile string) {
	redacted := "**** (redacted)"
	if strings.HasPrefix(token, "dapi-") {
		redacted = "dapi-***"
	}

	codexPath := "(not found)"
	if p, err := exec.LookPath("codex"); err == nil {
		codexPath = p
	}

	fmt.Printf(`databricks-codex configuration:
  Profile:           %s
  DATABRICKS_HOST:   %s
  OPENAI_BASE_URL:   %s
  OPENAI_API_KEY:    %s
  Codex binary:      %s
`, profile, databricksHost, openaiBaseURL, redacted, codexPath)
}

// deriveLogsTable derives the OTEL logs table name from the metrics table name.
// If the metrics table ends with "_otel_metrics", it replaces that suffix with
// "_otel_logs"; otherwise it appends "_otel_logs".
func deriveLogsTable(metricsTable string) string {
	if strings.HasSuffix(metricsTable, "_otel_metrics") {
		return strings.TrimSuffix(metricsTable, "_otel_metrics") + "_otel_logs"
	}
	return metricsTable + "_otel_logs"
}
