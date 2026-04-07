package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/IceRhymers/databricks-claude/pkg/authcheck"
	"github.com/IceRhymers/databricks-claude/pkg/portbind"
	"github.com/IceRhymers/databricks-claude/pkg/proxy"
	"github.com/IceRhymers/databricks-claude/pkg/refcount"
)

// Version is set at build time via -ldflags.
var Version = "dev"

func main() {
	verbose, version, showHelp, printEnv, noOtel, otelLogsTable, otelLogsTableSet, upstream, logFile, profile, otel, proxyAPIKey, tlsCert, tlsKey, model, modelSet, portFlag, headless, codexArgs := parseArgs(os.Args[1:])

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
		f, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
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
	// Resolution chain: --profile flag → saved state → "DEFAULT".
	// The env var DATABRICKS_CONFIG_PROFILE is intentionally NOT checked here;
	// injected env vars (e.g. from Claude's settings.json) would silently
	// override the user's saved proxy profile, routing to the wrong workspace.
	// When --profile is explicitly passed, save it for future sessions.
	profileExplicit := profile != ""
	profile = resolveProfile(profile, loadState().Profile)
	if profileExplicit {
		saved := loadState()
		saved.Profile = profile
		if err := saveState(saved); err != nil {
			log.Printf("databricks-codex: failed to save profile: %v", err)
		} else {
			log.Printf("databricks-codex: saved profile %q for future sessions", profile)
		}
	}
	log.Printf("databricks-codex: using profile: %s", profile)

	// --- Resolve model ---
	// Resolution chain: --model flag → saved state → "databricks-gpt-5-4".
	modelExplicit := modelSet
	if model == "" {
		if saved := loadState(); saved.Model != "" {
			model = saved.Model
			log.Printf("databricks-codex: using saved model: %s", model)
		}
	}
	if model == "" {
		model = "databricks-gpt-5-4"
	}
	if modelExplicit {
		saved := loadState()
		saved.Model = model
		if err := saveState(saved); err != nil {
			log.Printf("databricks-codex: failed to save model: %v", err)
		} else {
			log.Printf("databricks-codex: saved model %q for future sessions", model)
		}
	}
	log.Printf("databricks-codex: using model: %s", model)

	// --- Ensure the user is authenticated before proceeding ---
	if err := authcheck.EnsureAuthenticated(profile); err != nil {
		log.Fatalf("databricks-codex: auth failed: %v", err)
	}

	// --- Load state and resolve port ---
	state := loadState()
	port := resolvePort(portFlag, state)
	if portFlag > 0 {
		state.Port = port
		if err := saveState(state); err != nil {
			log.Printf("databricks-codex: failed to save port: %v", err)
		} else {
			log.Printf("databricks-codex: saved port %d for future sessions", port)
		}
	}
	log.Printf("databricks-codex: using port: %d", port)

	// --- TLS validation ---
	if err := proxy.ValidateTLSConfig(tlsCert, tlsKey); err != nil {
		log.Fatalf("databricks-codex: %v", err)
	}

	// --- Startup security checks ---
	for _, w := range proxy.SecurityChecks() {
		fmt.Fprintln(os.Stderr, w)
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

	// --- OTEL logs table ---
	// Resolution chain: --otel-logs-table flag → saved state → default.
	otelLogsTableExplicit := otelLogsTableSet
	otelLogsTable = resolveOtelLogsTable(otelLogsTable, otelLogsTableSet, loadState().OtelLogsTable)
	if !otelLogsTableExplicit && otelLogsTable != "main.codex_telemetry.codex_otel_logs" {
		log.Printf("databricks-codex: using saved otel-logs-table: %s", otelLogsTable)
	}
	if otelLogsTableExplicit {
		saved := loadState()
		saved.OtelLogsTable = otelLogsTable
		if err := saveState(saved); err != nil {
			log.Printf("databricks-codex: failed to save otel-logs-table: %v", err)
		} else {
			log.Printf("databricks-codex: saved otel-logs-table %q for future sessions", otelLogsTable)
		}
	}
	if noOtel {
		otel = false
	}

	// --- Print env and exit if requested ---
	if printEnv {
		handlePrintEnv(host, gatewayURL, initialToken, profile, model, otelLogsTable)
		os.Exit(0)
	}

	// Verify codex is on PATH before starting proxy (skip in headless mode).
	if !headless {
		if _, err := exec.LookPath("codex"); err != nil {
			log.Fatalf("databricks-codex: codex binary not found on PATH — install from https://openai.com/codex")
		}
	}

	// --- Determine OTEL upstream ---
	otelUpstream := ""
	if otel {
		otelUpstream = host + "/api/2.0/otel"
		log.Printf("databricks-codex: OTEL enabled, upstream: %s", otelUpstream)
	}

	// --- Bind proxy port ---
	listener, isOwner, err := portbind.Bind("databricks-codex", port)
	if err != nil {
		log.Fatalf("databricks-codex: %v", err)
	}

	scheme := "http"
	if tlsCert != "" && tlsKey != "" {
		scheme = "https"
		fmt.Fprintln(os.Stderr, "databricks-codex: TLS enabled")
	}
	proxyURL := fmt.Sprintf("%s://127.0.0.1:%d", scheme, listenerPort(listener, port))

	// --- Proxy handler (needed by owner and recovery goroutine) ---
	if proxyAPIKey != "" {
		fmt.Fprintln(os.Stderr, "databricks-codex: proxy API key authentication enabled")
	}
	proxyHandler := NewProxyServer(&ProxyConfig{
		InferenceUpstream: gatewayURL,
		OTELUpstream:      otelUpstream,
		UCLogsTable:       otelLogsTable,
		TokenProvider:     tp,
		Verbose:           verbose,
		APIKey:            proxyAPIKey,
		TLSCertFile:       tlsCert,
		TLSKeyFile:        tlsKey,
		ToolName:          "databricks-codex",
		Version:           Version,
	})

	// --- Start proxy if we own the port ---
	if isOwner {
		servedLn, err := proxy.Serve(listener, proxyHandler, tlsCert, tlsKey)
		if err != nil {
			log.Fatalf("databricks-codex: failed to start proxy: %v", err)
		}
		listener = servedLn
		log.Printf("databricks-codex: proxy owner on :%d", port)
	} else {
		log.Printf("databricks-codex: joining existing proxy on :%d", port)
		// Watch for owner death and take over the proxy if needed.
		go watchProxy(port, proxyHandler, tlsCert, tlsKey)
	}
	log.Printf("databricks-codex: proxy on %s (owner=%v)", proxyURL, isOwner)

	// --- Reference counting ---
	refcountPath := filepath.Join(os.TempDir(), fmt.Sprintf(".databricks-codex-sessions-%d", port))
	if err := refcount.Acquire(refcountPath); err != nil {
		log.Printf("databricks-codex: refcount acquire warning: %v", err)
	}

	// --- Write config once (idempotent) ---
	otelConfigEndpoint := ""
	if otel {
		otelConfigEndpoint = proxyURL + "/otel/v1/logs"
	}

	cm := NewConfigManager()
	if err := cm.EnsureConfig(proxyURL, model, modelExplicit, otelConfigEndpoint); err != nil {
		if headless {
			log.Printf("databricks-codex: warning: failed to write config.toml: %v", err)
		} else {
			log.Fatalf("databricks-codex: failed to write config.toml: %v", err)
		}
	}

	// --- Headless mode: print proxy URL and wait for signal ---
	if headless {
		runHeadless(proxyURL, listener, isOwner, refcountPath)
		return
	}

	if otel {
		log.Printf("databricks-codex: OTEL enabled — logs=%s", otelLogsTable)
	}

	log.Printf("databricks-codex: launching codex")

	// --- Run codex as a child process (parent stays alive to serve the proxy) ---
	exitCode, runErr := RunCodex(context.Background(), codexArgs)

	// --- Release refcount; if last session and owner, close listener ---
	remaining, err := refcount.Release(refcountPath)
	if err != nil {
		log.Printf("databricks-codex: refcount release warning: %v", err)
	}
	if remaining == 0 && isOwner {
		listener.Close()
		log.Printf("databricks-codex: last session, proxy shut down")
	}

	if runErr != nil {
		log.Printf("databricks-codex: codex error: %v", runErr)
	}
	os.Exit(exitCode)
}

// runHeadless runs the proxy in headless mode: prints PROXY_URL to stdout,
// then blocks until SIGINT or SIGTERM. Intended for IDE extensions that
// manage their own codex process.
func runHeadless(proxyURL string, ln net.Listener, isOwner bool, refcountPath string) {
	if !isOwner {
		// Non-owner: watch for proxy death and take over if needed.
		// watchProxy is already defined in this file.
		// We don't have the handler/TLS args here, so the non-owner
		// headless case simply watches — recovery requires the proxy
		// config which only the owner path sets up.
	}
	fmt.Printf("PROXY_URL=%s\n", proxyURL)
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
	signal.Stop(sigCh)
	n, _ := refcount.Release(refcountPath)
	if n == 0 && isOwner {
		ln.Close()
	}
}

// listenerPort extracts the port from a net.Listener, falling back to the
// configured port if the listener is nil (e.g., non-owner case).
func listenerPort(ln net.Listener, fallback int) int {
	if ln == nil {
		return fallback
	}
	if addr, ok := ln.Addr().(*net.TCPAddr); ok {
		return addr.Port
	}
	return fallback
}

// proxyHealthy returns true if the proxy on the given port responds to /health.
func proxyHealthy(port int, scheme string) bool {
	client := &http.Client{Timeout: 500 * time.Millisecond}
	if scheme == "https" {
		client.Transport = &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		}
	}
	resp, err := client.Get(fmt.Sprintf("%s://127.0.0.1:%d/health", scheme, port))
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// watchProxy polls the proxy health endpoint and takes over the port if the
// owner process dies. Runs as a goroutine for non-owner sessions.
func watchProxy(port int, handler http.Handler, tlsCert, tlsKey string) {
	scheme := "http"
	if tlsCert != "" && tlsKey != "" {
		scheme = "https"
	}

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		if proxyHealthy(port, scheme) {
			continue
		}

		// Proxy is unreachable — try to bind the port and take over.
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			continue // another session grabbed it first
		}
		if _, err := proxy.Serve(ln, handler, tlsCert, tlsKey); err != nil {
			ln.Close()
			continue
		}
		log.Printf("databricks-codex: proxy owner died, took over on :%d", port)
		return
	}
}

// parseArgs separates databricks-codex flags from codex flags.
func parseArgs(args []string) (verbose bool, version bool, showHelp bool, printEnv bool, noOtel bool, otelLogsTable string, otelLogsTableSet bool, upstream string, logFile string, profile string, otel bool, proxyAPIKey string, tlsCert string, tlsKey string, model string, modelSet bool, portFlag int, headless bool, codexArgs []string) {
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
		"--proxy-api-key":   true,
		"--tls-cert":        true,
		"--tls-key":         true,
		"--model":           true,
		"--port":            true,
		"--headless":        true,
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
				case "--proxy-api-key":
					if value != "" {
						proxyAPIKey = value
					} else if i+1 < len(args) {
						i++
						proxyAPIKey = args[i]
					}
				case "--tls-cert":
					if value != "" {
						tlsCert = value
					} else if i+1 < len(args) {
						i++
						tlsCert = args[i]
					}
				case "--tls-key":
					if value != "" {
						tlsKey = value
					} else if i+1 < len(args) {
						i++
						tlsKey = args[i]
					}
				case "--model":
					if value != "" {
						model = value
						modelSet = true
					} else if i+1 < len(args) {
						i++
						model = args[i]
						modelSet = true
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
				case "--port":
					if value != "" {
						portFlag, _ = strconv.Atoi(value)
					} else if i+1 < len(args) {
						i++
						portFlag, _ = strconv.Atoi(args[i])
					}
				case "--headless":
					headless = true
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
  --model string        Model name (saved for future sessions; default: "databricks-gpt-5-4")
  --upstream string     Override the AI Gateway URL (default: auto-discovered)
  --print-env           Print resolved configuration and exit (token redacted)
  --verbose, -v         Enable debug logging to stderr
  --log-file string     Write debug logs to a file (combinable with --verbose)
  --otel                    Enable OpenTelemetry log export
  --no-otel                 Disable OpenTelemetry for this session
  --otel-logs-table string  Unity Catalog table for OTEL logs (saved for future sessions; default: main.codex_telemetry.codex_otel_logs)
  --proxy-api-key string    Require this API key on all proxy requests (default: disabled)
  --tls-cert string         Path to TLS certificate file (requires --tls-key)
  --tls-key string          Path to TLS private key file (requires --tls-cert)
  --port int                Fixed proxy port (default: 49154, saved to state)
  --headless                Start proxy without launching codex (for IDE extensions)
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
func handlePrintEnv(databricksHost, openaiBaseURL, token, profile, model, otelLogsTable string) {
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
  Model:             %s
  DATABRICKS_HOST:   %s
  OPENAI_BASE_URL:   %s
  Auth Token:        %s
  OTEL Logs Table:   %s
  Codex binary:      %s
`, profile, model, databricksHost, openaiBaseURL, redacted, otelLogsTable, codexPath)
}

// resolveProfile returns the Databricks CLI profile using the resolution chain:
// --profile flag → saved state → "DEFAULT".
// The env var DATABRICKS_CONFIG_PROFILE is intentionally skipped; injected env
// vars would silently override the user's saved proxy profile.
func resolveProfile(flagValue string, savedValue string) string {
	if flagValue != "" {
		return flagValue
	}
	if savedValue != "" {
		return savedValue
	}
	return "DEFAULT"
}

// resolveOtelLogsTable returns the OTEL logs table using the resolution chain:
// explicit flag → saved state → default.
func resolveOtelLogsTable(flagValue string, flagSet bool, savedValue string) string {
	if flagSet && flagValue != "" {
		return flagValue
	}
	if savedValue != "" {
		return savedValue
	}
	return "main.codex_telemetry.codex_otel_logs"
}

