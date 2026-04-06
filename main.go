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
	"syscall"
)

// Version is set at build time via -ldflags.
var Version = "dev"

func main() {
	verbose, version, showHelp, printEnv, noOtel, otelTable, otelTableSet, upstream, logFile, codexArgs := parseArgs(os.Args[1:])

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

	// --- Seed token cache ---
	tp := NewTokenProvider("")
	initialToken, err := tp.Token(context.Background())
	if err != nil {
		log.Fatalf("databricks-codex: failed to fetch initial token: %v", err)
	}

	// --- Discover host + construct gateway URL ---
	host, err := DiscoverHost("")
	if err != nil {
		log.Fatalf("databricks-codex: failed to discover host: %v\nRun 'databricks auth login' first", err)
	}
	log.Printf("databricks-codex: discovered host: %s", host)

	gatewayURL := upstream
	if gatewayURL == "" {
		gatewayURL = ConstructGatewayURL(host, initialToken)
	}
	log.Printf("databricks-codex: gateway URL: %s", gatewayURL)

	// --- OTEL table ---
	otelMetricsTable := "main.codex_telemetry.codex_otel_metrics"
	if otelTableSet {
		otelMetricsTable = otelTable
	}
	_ = noOtel
	_ = otelMetricsTable

	// --- Print env and exit if requested ---
	if printEnv {
		handlePrintEnv(host, gatewayURL, initialToken)
		os.Exit(0)
	}

	// --- Resolve codex binary ---
	codexBin := "codex"
	if p, err := exec.LookPath("codex"); err == nil {
		codexBin = p
	} else {
		log.Fatalf("databricks-codex: codex binary not found on PATH — install from https://openai.com/codex")
	}

	// --- Build child environment: inherit all env vars + inject OPENAI_* ---
	env := os.Environ()
	env = setEnv(env, "OPENAI_BASE_URL", gatewayURL)
	env = setEnv(env, "OPENAI_API_KEY", initialToken)

	log.Printf("databricks-codex: launching %s with OPENAI_BASE_URL=%s", codexBin, gatewayURL)

	// --- Exec codex (replaces this process) ---
	args := append([]string{codexBin}, codexArgs...)
	if err := syscall.Exec(codexBin, args, env); err != nil {
		log.Fatalf("databricks-codex: exec failed: %v", err)
	}
}

// setEnv sets or replaces an environment variable in a []string slice.
func setEnv(env []string, key, value string) []string {
	prefix := key + "="
	for i, e := range env {
		if strings.HasPrefix(e, prefix) {
			env[i] = prefix + value
			return env
		}
	}
	return append(env, prefix+value)
}

// parseArgs separates databricks-codex flags from codex flags.
func parseArgs(args []string) (verbose bool, version bool, showHelp bool, printEnv bool, noOtel bool, otelTable string, otelTableSet bool, upstream string, logFile string, codexArgs []string) {
	otelTable = "main.codex_telemetry.codex_otel_metrics" // default

	knownFlags := map[string]bool{
		"--verbose":    true,
		"--version":    true,
		"--help":       true,
		"--print-env":  true,
		"--no-otel":    true,
		"--otel-table": true,
		"--upstream":   true,
		"--log-file":   true,
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
				case "--verbose":
					verbose = true
				case "--version":
					version = true
				case "--help":
					showHelp = true
				case "--print-env":
					printEnv = true
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

Injects OPENAI_BASE_URL and OPENAI_API_KEY so the Codex CLI authenticates
through a Databricks AI Gateway endpoint.

Usage:
  databricks-codex [databricks-codex flags] [codex flags] [codex args]

Databricks-Codex Flags:
  --upstream string     Override the AI Gateway URL (default: auto-discovered)
  --print-env           Print resolved configuration and exit (token redacted)
  --verbose, -v         Enable debug logging to stderr
  --log-file string     Write debug logs to a file (combinable with --verbose)
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
func handlePrintEnv(databricksHost, openaiBaseURL, token string) {
	redacted := "**** (redacted)"
	if strings.HasPrefix(token, "dapi-") {
		redacted = "dapi-***"
	}

	codexPath := "(not found)"
	if p, err := exec.LookPath("codex"); err == nil {
		codexPath = p
	}

	fmt.Printf(`databricks-codex configuration:
  DATABRICKS_HOST:   %s
  OPENAI_BASE_URL:   %s
  OPENAI_API_KEY:    %s
  Codex binary:      %s
`, databricksHost, openaiBaseURL, redacted, codexPath)
}
