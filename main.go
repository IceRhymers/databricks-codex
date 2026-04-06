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

	"github.com/IceRhymers/databricks-claude/pkg/authcheck"
)

// Version is set at build time via -ldflags.
var Version = "dev"

func main() {
	verbose, version, showHelp, printEnv, noOtel, otelLogsTable, otelLogsTableSet, upstream, logFile, profile, otel, codexArgs := parseArgs(os.Args[1:])

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
	// Resolution chain: --profile flag → env var → saved state → "DEFAULT".
	// When --profile is explicitly passed, save it for future sessions.
	profileExplicit := profile != ""
	if profile == "" {
		profile = os.Getenv("DATABRICKS_CONFIG_PROFILE")
	}
	if profile == "" {
		if saved := loadState(); saved.Profile != "" {
			profile = saved.Profile
			log.Printf("databricks-codex: using saved profile: %s", profile)
		}
	}
	if profile == "" {
		profile = "DEFAULT"
	}
	if profileExplicit {
		if err := saveState(persistentState{Profile: profile}); err != nil {
			log.Printf("databricks-codex: failed to save profile: %v", err)
		} else {
			log.Printf("databricks-codex: saved profile %q for future sessions", profile)
		}
	}
	log.Printf("databricks-codex: using profile: %s", profile)

	// --- Ensure the user is authenticated before proceeding ---
	if err := authcheck.EnsureAuthenticated(profile); err != nil {
		log.Fatalf("databricks-codex: auth failed: %v", err)
	}

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
	if !otelLogsTableSet {
		otelLogsTable = "main.codex_telemetry.codex_otel_logs"
	}
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
		otelUpstream = host + "/api/2.0/otel"
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
	otelConfigEndpoint := ""
	if otel {
		otelConfigEndpoint = proxyAddr + "/otel/v1/logs"
	}

	cm := NewConfigManager()
	if err := cm.Setup(proxyAddr, "databricks-gpt-5-4", otelConfigEndpoint); err != nil {
		log.Fatalf("databricks-codex: failed to patch config.toml: %v", err)
	}

	// Set OPENAI_API_KEY as a placeholder — the proxy overwrites the
	// Authorization header with a live Databricks token per request.
	os.Setenv("OPENAI_API_KEY", "databricks-proxy")

	if otel {
		log.Printf("databricks-codex: OTEL enabled — logs=%s", otelLogsTable)
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
func parseArgs(args []string) (verbose bool, version bool, showHelp bool, printEnv bool, noOtel bool, otelLogsTable string, otelLogsTableSet bool, upstream string, logFile string, profile string, otel bool, codexArgs []string) {
	knownFlags := map[string]bool{
		"--verbose":         true,
		"--version":         true,
		"--help":            true,
		"--print-env":       true,
		"--no-otel":         true,
		"--otel":            true,
		"--otel-logs-table": true,
		"--upstream":        true,
		"--log-file":        true,
		"--profile":         true,
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
				case "--otel-logs-table":
					if value != "" {
						otelLogsTable = value
						otelLogsTableSet = true
					} else if i+1 < len(args) {
						i++
						otelLogsTable = args[i]
						otelLogsTableSet = true
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
  --profile string      Databricks CLI profile (saved for future sessions; default: env or "DEFAULT")
  --upstream string     Override the AI Gateway URL (default: auto-discovered)
  --print-env           Print resolved configuration and exit (token redacted)
  --verbose, -v         Enable debug logging to stderr
  --log-file string     Write debug logs to a file (combinable with --verbose)
  --otel                    Enable OpenTelemetry log export
  --no-otel                 Disable OpenTelemetry for this session
  --otel-logs-table string  Unity Catalog table for OTEL logs (default: main.codex_telemetry.codex_otel_logs)
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

