package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/IceRhymers/databricks-claude/pkg/authcheck"
	"github.com/IceRhymers/databricks-claude/pkg/completion"
	"github.com/IceRhymers/databricks-claude/pkg/health"
	"github.com/IceRhymers/databricks-claude/pkg/lifecycle"
	"github.com/IceRhymers/databricks-claude/pkg/portbind"
	"github.com/IceRhymers/databricks-claude/pkg/proxy"
	"github.com/IceRhymers/databricks-claude/pkg/refcount"
	"github.com/IceRhymers/databricks-claude/pkg/updater"
)

// Version is set at build time via -ldflags.
var Version = "dev"

func main() {
	// completion <shell> — must be the very first check, before any flag parsing,
	// auth, or state loading. Safe to call in the Homebrew install sandbox.
	if len(os.Args) >= 2 && os.Args[1] == "completion" {
		completion.Run(os.Args[2:], flagDefs, "databricks-codex", knownSubcommands...)
		os.Exit(0)
	}

	// hooks <subcommand> — handled before auth/state setup since
	// session-start is hot-path (called by every codex SessionStart) and
	// install/uninstall must work in environments where the proxy is not
	// yet configured. The dispatcher in hooks_cmd.go owns flag parsing.
	if len(os.Args) >= 2 && os.Args[1] == "hooks" {
		if err := runHooksCommand(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "databricks-codex:", err)
			os.Exit(1)
		}
		os.Exit(0)
	}

	// update — force-check for a newer release and print instructions.
	if len(os.Args) >= 2 && os.Args[1] == "update" {
		if os.Getenv("DATABRICKS_NO_UPDATE_CHECK") == "1" {
			fmt.Fprintln(os.Stderr, "databricks-codex: update check disabled via DATABRICKS_NO_UPDATE_CHECK")
			os.Exit(0)
		}
		cfg := buildUpdaterConfig()
		cfg.CacheTTL = 0 // force fresh check
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		r, err := updater.Check(ctx, cfg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "databricks-codex: update check failed: %v\n", err)
			os.Exit(1)
		}
		if !r.UpdateAvailable {
			fmt.Fprintf(os.Stderr, "databricks-codex v%s is already the latest version\n", Version)
			os.Exit(0)
		}
		if r.IsHomebrew {
			fmt.Fprintf(os.Stderr, "Update available: v%s. Run: brew upgrade databricks-codex\n", r.LatestVersion)
		} else {
			fmt.Fprintf(os.Stderr, "Update available: v%s. Download from: %s\n", r.LatestVersion, r.ReleaseURL)
		}
		os.Exit(0)
	}

	a, err := parseArgs(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "databricks-codex:", err)
		os.Exit(1)
	}

	if a.ShowHelp {
		handleHelp(a.Upstream)
		os.Exit(0)
	}

	if a.Version {
		fmt.Printf("databricks-codex %s\n", Version)
		os.Exit(0)
	}

	// Default: discard all logs (silent wrapper).
	log.SetOutput(io.Discard)

	if a.Verbose {
		log.SetOutput(os.Stderr)
	}
	if a.LogFile != "" {
		f, err := os.OpenFile(a.LogFile, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			log.SetOutput(os.Stderr)
			log.Fatalf("databricks-codex: cannot open log file %q: %v", a.LogFile, err)
		}
		defer f.Close()
		if a.Verbose {
			log.SetOutput(io.MultiWriter(os.Stderr, f))
		} else {
			log.SetOutput(f)
		}
	}

	// --- Resolve profile ---
	profileExplicit := a.Profile != ""
	profile := resolveProfile(a.Profile, loadState().Profile)
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
	modelExplicit := a.ModelSet
	savedForModel := loadState()
	model := resolveModel(a.Model, savedForModel.Model)
	switch {
	case a.Model != "":
		// flag-supplied; logged below
	case savedForModel.Model != "":
		log.Printf("databricks-codex: using saved model: %s", savedForModel.Model)
	}
	if modelExplicit {
		savedForModel.Model = model
		if err := saveState(savedForModel); err != nil {
			log.Printf("databricks-codex: failed to save model: %v", err)
		} else {
			log.Printf("databricks-codex: saved model %q for future sessions", model)
		}
	}
	log.Printf("databricks-codex: using model: %s", model)

	// --- Ensure the user is authenticated before proceeding ---
	if err := authcheck.EnsureAuthenticated(profile, ""); err != nil {
		log.Fatalf("databricks-codex: auth failed: %v", err)
	}

	// --- Load state and resolve port ---
	state := loadState()
	port := resolvePort(a.PortFlag, state)
	if a.PortFlag > 0 {
		state.Port = port
		if err := saveState(state); err != nil {
			log.Printf("databricks-codex: failed to save port: %v", err)
		} else {
			log.Printf("databricks-codex: saved port %d for future sessions", port)
		}
	}
	log.Printf("databricks-codex: using port: %d", port)

	// --- TLS validation ---
	if err := proxy.ValidateTLSConfig(a.TLSCert, a.TLSKey); err != nil {
		log.Fatalf("databricks-codex: %v", err)
	}

	// --- Save TLS config to state so headless-ensure can use the right scheme ---
	{
		s := loadState()
		if s.TLSCert != a.TLSCert || s.TLSKey != a.TLSKey {
			s.TLSCert = a.TLSCert
			s.TLSKey = a.TLSKey
			if err := saveState(s); err != nil {
				log.Printf("databricks-codex: failed to save TLS config: %v", err)
			}
		}
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

	gatewayURL := a.Upstream
	if gatewayURL == "" {
		gatewayURL = ConstructGatewayURL(host)
	}
	log.Printf("databricks-codex: gateway URL: %s", gatewayURL)

	// --- OTEL tables ---
	// Compute the final (otel, metricsTable, logsTable) tuple from flags + state.
	// Saved state is read once and passed in so the helper is pure & testable.
	saved := loadState()
	otel, otelMetricsTable, otelLogsTable := resolveOtel(a, saved)

	// Log informational lines and persist any explicit table flags.
	if !a.OtelMetricsTableSet && otelMetricsTable != "" && otelMetricsTable != "main.codex_telemetry.codex_otel_metrics" {
		log.Printf("databricks-codex: using saved otel-metrics-table: %s", otelMetricsTable)
	}
	if a.OtelMetricsTableSet {
		s := loadState()
		s.OtelMetricsTable = otelMetricsTable
		if err := saveState(s); err != nil {
			log.Printf("databricks-codex: failed to save otel-metrics-table: %v", err)
		} else {
			log.Printf("databricks-codex: saved otel-metrics-table %q for future sessions", otelMetricsTable)
		}
	}
	if !a.OtelLogsTableSet && otelLogsTable != "" && otelLogsTable != "main.codex_telemetry.codex_otel_logs" {
		log.Printf("databricks-codex: using saved otel-logs-table: %s", otelLogsTable)
	}
	if a.OtelLogsTableSet {
		s := loadState()
		s.OtelLogsTable = otelLogsTable
		if err := saveState(s); err != nil {
			log.Printf("databricks-codex: failed to save otel-logs-table: %v", err)
		} else {
			log.Printf("databricks-codex: saved otel-logs-table %q for future sessions", otelLogsTable)
		}
	}

	// --- Print env and exit if requested ---
	if a.PrintEnv {
		handlePrintEnv(host, gatewayURL, initialToken, profile, model, otelMetricsTable, otelLogsTable)
		os.Exit(0)
	}

	// Verify codex is on PATH before starting proxy (skip in headless mode).
	if !a.Headless {
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
	if a.TLSCert != "" && a.TLSKey != "" {
		scheme = "https"
		fmt.Fprintln(os.Stderr, "databricks-codex: TLS enabled")
	}
	proxyURL := fmt.Sprintf("%s://127.0.0.1:%d", scheme, portbind.ListenerPort(listener, port))

	// --- Proxy handler (needed by owner and recovery goroutine) ---
	if a.ProxyAPIKey != "" {
		fmt.Fprintln(os.Stderr, "databricks-codex: proxy API key authentication enabled")
	}
	proxyHandler, err := NewProxyServer(&ProxyConfig{
		InferenceUpstream: gatewayURL,
		OTELUpstream:      otelUpstream,
		UCMetricsTable:    otelMetricsTable,
		UCLogsTable:       otelLogsTable,
		TokenProvider:     tp,
		Verbose:           a.Verbose,
		APIKey:            a.ProxyAPIKey,
		TLSCertFile:       a.TLSCert,
		TLSKeyFile:        a.TLSKey,
		ToolName:          "databricks-codex",
		Version:           Version,
	})
	if err != nil {
		log.Fatalf("databricks-codex: failed to create proxy: %v", err)
	}

	// --- Reference counting ---
	refcountPath := refcount.PathForPort(".databricks-codex-sessions", port)
	if !a.Headless {
		if err := refcount.Acquire(refcountPath); err != nil {
			log.Printf("databricks-codex: refcount acquire warning: %v", err)
		}
	}

	// In headless mode, wrap handler with /shutdown endpoint and idle timeout.
	var doneCh chan struct{}
	if a.Headless {
		doneCh = make(chan struct{})
		proxyHandler = lifecycle.WrapWithLifecycle(lifecycle.Config{
			Inner:        proxyHandler,
			RefcountPath: refcountPath,
			IsOwner:      isOwner,
			IdleTimeout:  a.IdleTimeout,
			APIKey:       a.ProxyAPIKey,
			DoneCh:       doneCh,
			LogPrefix:    "databricks-codex",
		})
	}

	// --- Start proxy if we own the port ---
	if isOwner {
		servedLn, err := proxy.Serve(listener, proxyHandler, a.TLSCert, a.TLSKey)
		if err != nil {
			log.Fatalf("databricks-codex: failed to start proxy: %v", err)
		}
		listener = servedLn
		log.Printf("databricks-codex: proxy owner on :%d", port)
	} else {
		log.Printf("databricks-codex: joining existing proxy on :%d", port)
		// Watch for owner death and take over the proxy if needed.
		go health.WatchProxy(port, proxyHandler, a.TLSCert, a.TLSKey, "databricks-codex", nil)
	}
	log.Printf("databricks-codex: proxy on %s (owner=%v)", proxyURL, isOwner)

	// --- Write config once (idempotent) ---
	otelLogsConfigEndpoint := ""
	otelMetricsConfigEndpoint := ""
	if otel {
		if otelLogsTable != "" {
			otelLogsConfigEndpoint = proxyURL + "/otel/v1/logs"
		}
		if otelMetricsTable != "" {
			otelMetricsConfigEndpoint = proxyURL + "/otel/v1/metrics"
		}
	}

	cm := NewConfigManager()
	if err := cm.EnsureConfig(proxyURL, model, modelExplicit, otelLogsConfigEndpoint, otelMetricsConfigEndpoint); err != nil {
		if a.Headless {
			log.Printf("databricks-codex: warning: failed to write config.toml: %v", err)
		} else {
			log.Fatalf("databricks-codex: failed to write config.toml: %v", err)
		}
	}

	// --- Headless mode: print proxy URL and wait for signal ---
	if a.Headless {
		runHeadless(proxyURL, listener, isOwner, refcountPath, doneCh)
		return
	}

	if otel {
		var parts []string
		if otelMetricsTable != "" {
			parts = append(parts, "metrics="+otelMetricsTable)
		}
		if otelLogsTable != "" {
			parts = append(parts, "logs="+otelLogsTable)
		}
		if len(parts) > 0 {
			log.Printf("databricks-codex: OTEL enabled — %s", strings.Join(parts, ", "))
		}
	}

	// --- Synchronous update check (before child to avoid stderr interleaving) ---
	if !a.NoUpdateCheck && os.Getenv("DATABRICKS_NO_UPDATE_CHECK") != "1" {
		updater.PrintUpdateNotice(buildUpdaterConfig())
	}

	log.Printf("databricks-codex: launching codex")

	// --- Run codex as a child process (parent stays alive to serve the proxy) ---
	exitCode, runErr := RunCodex(context.Background(), a.CodexArgs)

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

// runHeadless runs the proxy without launching a codex child process.
// It prints the proxy URL to stdout, then blocks until SIGINT/SIGTERM
// or until doneCh is closed (by /shutdown or idle timeout).
// The watchProxy goroutine (for non-owner sessions) is already started
// before this function is called.
func runHeadless(proxyURL string, ln net.Listener, isOwner bool, refcountPath string, doneCh chan struct{}) {
	fmt.Printf("PROXY_URL=%s\n", proxyURL)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	select {
	case <-sigCh:
		signal.Stop(sigCh)
	case <-doneCh:
		// Triggered by /shutdown or idle timeout.
	}

	// Release refcount. If /shutdown already released, Release floors at 0.
	n, _ := refcount.Release(refcountPath)
	if n == 0 && isOwner {
		ln.Close()
	}
}

// Args holds all parsed databricks-codex flags plus the residual codex args.
type Args struct {
	Verbose             bool
	Version             bool
	ShowHelp            bool
	PrintEnv            bool
	NoOtel              bool
	NoOtelMetrics       bool
	NoOtelLogs          bool
	OtelLogsTable       string
	OtelLogsTableSet    bool
	OtelMetricsTable    string
	OtelMetricsTableSet bool
	Upstream            string
	LogFile             string
	Profile             string
	Otel                bool
	ProxyAPIKey         string
	TLSCert             string
	TLSKey              string
	Model               string
	ModelSet            bool
	PortFlag            int
	Headless            bool
	IdleTimeout         time.Duration
	NoUpdateCheck       bool
	CodexArgs           []string
}

// parseArgs separates databricks-codex flags from codex flags.
func parseArgs(args []string) (*Args, error) {
	a := &Args{
		IdleTimeout: 30 * time.Minute, // default
	}

	// knownFlags is defined at package level in completion_flags.go,
	// derived from flagDefs so completions and parsing stay in sync.

	i := 0
	for i < len(args) {
		arg := args[i]

		// Explicit separator: everything after "--" goes to codex.
		if arg == "--" {
			a.CodexArgs = append(a.CodexArgs, args[i+1:]...)
			return a, nil
		}

		if arg == "-h" {
			a.ShowHelp = true
			i++
			continue
		}
		if arg == "-v" {
			a.Verbose = true
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
						a.OtelLogsTable = value
						a.OtelLogsTableSet = true
					} else if i+1 < len(args) {
						i++
						a.OtelLogsTable = args[i]
						a.OtelLogsTableSet = true
					}
				case "--otel-metrics-table":
					if value != "" {
						a.OtelMetricsTable = value
						a.OtelMetricsTableSet = true
					} else if i+1 < len(args) {
						i++
						a.OtelMetricsTable = args[i]
						a.OtelMetricsTableSet = true
					}
				case "--upstream":
					if value != "" {
						a.Upstream = value
					} else if i+1 < len(args) {
						i++
						a.Upstream = args[i]
					}
				case "--log-file":
					if value != "" {
						a.LogFile = value
					} else if i+1 < len(args) {
						i++
						a.LogFile = args[i]
					}
				case "--profile":
					if value != "" {
						a.Profile = value
					} else if i+1 < len(args) {
						i++
						a.Profile = args[i]
					}
				case "--proxy-api-key":
					if value != "" {
						a.ProxyAPIKey = value
					} else if i+1 < len(args) {
						i++
						a.ProxyAPIKey = args[i]
					}
				case "--tls-cert":
					if value != "" {
						a.TLSCert = value
					} else if i+1 < len(args) {
						i++
						a.TLSCert = args[i]
					}
				case "--tls-key":
					if value != "" {
						a.TLSKey = value
					} else if i+1 < len(args) {
						i++
						a.TLSKey = args[i]
					}
				case "--model":
					if value != "" {
						a.Model = value
						a.ModelSet = true
					} else if i+1 < len(args) {
						i++
						a.Model = args[i]
						a.ModelSet = true
					}
				case "--verbose":
					a.Verbose = true
				case "--version":
					a.Version = true
				case "--help":
					a.ShowHelp = true
				case "--print-env":
					a.PrintEnv = true
				case "--otel":
					a.Otel = true
				case "--no-otel":
					a.NoOtel = true
				case "--no-otel-metrics":
					a.NoOtelMetrics = true
				case "--no-otel-logs":
					a.NoOtelLogs = true
				case "--port":
					if value != "" {
						a.PortFlag, _ = strconv.Atoi(value)
					} else if i+1 < len(args) {
						i++
						a.PortFlag, _ = strconv.Atoi(args[i])
					}
				case "--headless":
					a.Headless = true
				case "--idle-timeout":
					raw := value
					if raw == "" && i+1 < len(args) {
						i++
						raw = args[i]
					}
					d, err := time.ParseDuration(raw)
					if err != nil {
						return nil, fmt.Errorf("--idle-timeout: %q is not a valid duration (use e.g. 30s, 5m, 1h)", raw)
					}
					a.IdleTimeout = d
				case "--no-update-check":
					a.NoUpdateCheck = true
				default:
					// A name in knownFlags must have a corresponding case
					// above; this arm catches the case where rootCommand
					// declares a new flag but parseArgs hasn't been updated.
					// Loud failure beats silent passthrough — the bidirectional
					// parity test in main_test.go also detects this drift,
					// but a runtime check catches it for any caller path the
					// test doesn't exercise.
					return nil, fmt.Errorf("internal: %s is a known flag but parseArgs has no case for it", name)
				}
				i++
				continue
			}
		}

		// Not a known flag — pass through to codex.
		a.CodexArgs = append(a.CodexArgs, arg)
		i++
	}
	return a, nil
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
  --model string        Model name (saved for future sessions; default: "databricks-gpt-5-5")
  --upstream string     Override the AI Gateway URL (default: auto-discovered)
  --print-env           Print resolved configuration and exit (token redacted)
  --verbose, -v         Enable debug logging to stderr
  --log-file string     Write debug logs to a file (combinable with --verbose)
  --otel                       Enable OpenTelemetry export (metrics + logs)
  --no-otel                    Disable OpenTelemetry for this session (saved tables preserved)
  --otel-metrics-table string  Unity Catalog table for OTEL metrics (saved; default: main.codex_telemetry.codex_otel_metrics when --otel is set)
  --otel-logs-table string     Unity Catalog table for OTEL logs (saved; derived from metrics table when omitted)
  --no-otel-metrics            Disable metrics for this session (saved table preserved)
  --no-otel-logs               Disable logs for this session (saved table preserved)
  --proxy-api-key string    Require this API key on all proxy requests (default: disabled)
  --tls-cert string         Path to TLS certificate file (requires --tls-key)
  --tls-key string          Path to TLS private key file (requires --tls-cert)
  --port int                Fixed proxy port (default: 49154, saved to state)
  --headless                Start proxy without launching codex (for IDE extensions)
  --idle-timeout duration   Idle timeout for headless mode (default 30m, 0 disables)
  --no-update-check            Skip the automatic update check on startup
  --version             Print version and exit
  --help, -h            Show this help message

Subcommands:
  completion <shell>           Generate shell completions (bash, zsh, fish)
  update                       Check for a newer release and print upgrade instructions
  hooks <subcommand>           Install/uninstall SessionStart hooks (install, uninstall, session-start)

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

// buildUpdaterConfig returns the standard updater.Config for databricks-codex.
func buildUpdaterConfig() updater.Config {
	home, _ := os.UserHomeDir()
	return updater.Config{
		RepoSlug:       "IceRhymers/databricks-codex",
		CurrentVersion: Version,
		BinaryName:     "databricks-codex",
		CacheFile:      filepath.Join(home, ".codex", ".update-check.json"),
		CacheTTL:       24 * time.Hour,
	}
}

// handlePrintEnv prints resolved configuration with the token redacted.
// Redaction is applied unconditionally — never branch on token shape, since any
// branch leaks information about the token format.
func handlePrintEnv(databricksHost, openaiBaseURL, token, profile, model, otelMetricsTable, otelLogsTable string) {
	_ = token // intentionally unused: we never print the token
	redacted := "**** (redacted)"

	codexPath := "(not found)"
	if p, err := exec.LookPath("codex"); err == nil {
		codexPath = p
	}

	metricsLine := otelMetricsTable
	if metricsLine == "" {
		metricsLine = "(disabled)"
	}
	logsLine := otelLogsTable
	if logsLine == "" {
		logsLine = "(disabled)"
	}

	fmt.Printf(`databricks-codex configuration:
  Profile:             %s
  Model:               %s
  DATABRICKS_HOST:     %s
  OPENAI_BASE_URL:     %s
  Auth Token:          %s
  OTEL Metrics Table:  %s
  OTEL Logs Table:     %s
  Codex binary:        %s
`, profile, model, databricksHost, openaiBaseURL, redacted, metricsLine, logsLine, codexPath)
}

// defaultModel returns the built-in default model name used when nothing else
// (flag, env, saved state) is set. Centralised so tests can lock the default
// against silent drift.
func defaultModel() string { return "databricks-gpt-5-5" }

// resolveModel returns the model name using the resolution chain:
// --model flag → saved state → built-in default. The built-in default is
// the only value that changes when the project bumps its default model.
func resolveModel(flagValue string, savedValue string) string {
	if flagValue != "" {
		return flagValue
	}
	if savedValue != "" {
		return savedValue
	}
	return defaultModel()
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

// resolveOtel is the orchestration: given parsed flags + saved state, decide
// whether OTel is on for this session and which (metrics, logs) tables to use.
//
// Semantics mirror databricks-claude:
//
//	OTel is "on" if any of: --otel, --otel-metrics-table, --otel-logs-table,
//	or saved state has any table set — UNLESS --no-otel was passed, which
//	is the unconditional kill switch.
//
//	--no-otel-metrics / --no-otel-logs disable that specific signal for the
//	current session but leave OTel itself on (so the other signal keeps
//	exporting) and leave the saved table preference intact.
//
// Both returned table strings are empty when their signal is disabled.
func resolveOtel(a *Args, saved persistentState) (otel bool, metricsTable string, logsTable string) {
	otel = a.Otel
	if !otel && !a.NoOtel {
		if a.OtelMetricsTableSet || a.OtelLogsTableSet || saved.OtelMetricsTable != "" || saved.OtelLogsTable != "" {
			otel = true
		}
	}
	if a.NoOtel {
		otel = false
	}

	if !a.NoOtelMetrics {
		metricsTable = resolveOtelMetricsTable(a.OtelMetricsTable, a.OtelMetricsTableSet, saved.OtelMetricsTable, otel)
	}
	if !a.NoOtelLogs {
		logsTable = resolveOtelLogsTable(a.OtelLogsTable, a.OtelLogsTableSet, saved.OtelLogsTable, metricsTable, otel)
	}
	return otel, metricsTable, logsTable
}

// resolveOtelLogsTable returns the OTEL logs table using the resolution chain:
// explicit flag → saved state → derive-from-metrics-table → default.
// Returns empty string when otel is disabled.
func resolveOtelLogsTable(flagValue string, flagSet bool, savedValue string, metricsTable string, otel bool) string {
	if !otel {
		return ""
	}
	if flagSet && flagValue != "" {
		return flagValue
	}
	if savedValue != "" {
		return savedValue
	}
	if metricsTable != "" {
		return deriveLogsTable(metricsTable)
	}
	return "main.codex_telemetry.codex_otel_logs"
}

// resolveOtelMetricsTable returns the OTEL metrics table using the resolution chain:
// explicit flag → saved state → default. Returns empty string when otel is disabled.
func resolveOtelMetricsTable(flagValue string, flagSet bool, savedValue string, otel bool) string {
	if !otel {
		return ""
	}
	if flagSet && flagValue != "" {
		return flagValue
	}
	if savedValue != "" {
		return savedValue
	}
	return "main.codex_telemetry.codex_otel_metrics"
}

// deriveLogsTable derives the OTEL logs table name from a metrics table name.
// If the metrics table ends with "_otel_metrics", replace that suffix with "_otel_logs".
// Otherwise append "_otel_logs". Ported from databricks-claude/main.go.
func deriveLogsTable(metricsTable string) string {
	if metricsTable == "" {
		return ""
	}
	if strings.HasSuffix(metricsTable, "_otel_metrics") {
		return strings.TrimSuffix(metricsTable, "_otel_metrics") + "_otel_logs"
	}
	return metricsTable + "_otel_logs"
}
