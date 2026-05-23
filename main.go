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

	// `config` subcommand — persistent config editor (OTEL signals,
	// resolved-config diagnostic). Consolidates the 7 flags removed from the
	// root in #87 — the flags that mutate state for FUTURE runs rather than
	// affecting the current invocation. The transparent-proxy launcher path
	// below is intentionally flag-driven and bare; persistent state mutation
	// lives behind this tree.
	if len(os.Args) >= 2 && os.Args[1] == "config" {
		runConfigCommand(os.Args[2:])
		return
	}

	// `serve` subcommand — runs the proxy in headless mode. Consolidates the
	// legacy --headless and --idle-timeout root flags removed in #89.
	// Dispatched before parseArgs so the serve flag set is parsed by the
	// serve subtree (not by the root parser, which no longer recognises
	// --idle-timeout). The runner ends up calling runProxyMode with
	// Headless=true — same launcher the legacy --headless path used.
	if len(os.Args) >= 2 && os.Args[1] == "serve" {
		if err := runServeCommand(os.Args[2:]); err != nil {
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

	runProxyMode(a)
}

// runProxyMode is the shared launcher for both the transparent-wrapper path
// (databricks-codex with no subcommand → spawn codex as a child) and the
// headless path (databricks-codex serve → keep the proxy alive without a
// child). The two paths converge on the same Args struct: the serve
// dispatcher (runServeCommand) constructs Args with Headless=true and the
// resolved IdleTimeout, while the wrapper path leaves both at their zero
// values.
//
// The Headless/IdleTimeout fields are no longer set by parseArgs (the legacy
// --headless / --idle-timeout root flags were removed in #89); they are
// populated only by the serve dispatcher. Treating them as Args fields keeps
// runProxyMode's signature stable and makes the serve test surface a simple
// "spy on the Args struct that runServeCommand constructs" check (see
// serve_cmd_test.go).
//
// Exits the process directly in the codex-launch path (with codex's exit
// code); returns normally in the headless path so the serve dispatcher's
// caller can os.Exit(0).
func runProxyMode(a *Args) {
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
	if _, err := tp.Token(context.Background()); err != nil {
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
	// Compute the final (otel, metricsTable, logsTable) tuple from saved
	// state. With #87, the persistent-config editor (`databricks-codex config
	// otel enable/disable`) is the only path that mutates these — the
	// regular session flow is read-only.
	saved := loadState()
	otel, otelMetricsTable, otelLogsTable := resolveOtel(saved)

	if otelMetricsTable != "" {
		log.Printf("databricks-codex: using saved otel-metrics-table: %s", otelMetricsTable)
	}
	if otelLogsTable != "" {
		log.Printf("databricks-codex: using saved otel-logs-table: %s", otelLogsTable)
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
//
// #87 removed the persistent-config root flags (--otel, --no-otel*,
// --otel-*-table, --print-env). They live under `databricks-codex config
// otel {enable|disable}` and `databricks-codex config show` now and have no
// session-only counterparts — the persistent-config editor IS the surface.
// The OTEL state still drives the regular session: resolveOtel reads the
// state file directly to decide whether to emit OTEL endpoints into the
// proxy + config.toml.
//
// #89 removed the legacy --headless and --idle-timeout root flags. Their
// effect now lives behind the `serve` subcommand. The Headless and
// IdleTimeout fields stay on this struct because they are inputs to
// runProxyMode — set by runServeCommand (serve_cmd.go) when the user
// invokes `databricks-codex serve`. parseArgs never sets them; the
// transparent-wrapper path leaves both at zero (Headless=false,
// IdleTimeout=0) and runProxyMode skips the headless branches.
type Args struct {
	Verbose       bool
	Version       bool
	ShowHelp      bool
	Upstream      string
	LogFile       string
	Profile       string
	ProxyAPIKey   string
	TLSCert       string
	TLSKey        string
	Model         string
	ModelSet      bool
	PortFlag      int
	Headless      bool          // set by runServeCommand only (#89)
	IdleTimeout   time.Duration // set by runServeCommand only (#89)
	NoUpdateCheck bool
	CodexArgs     []string
}

// parseArgs separates databricks-codex flags from codex flags. Recognises
// only the root-flag set declared on rootCommand (commands.go); the legacy
// --headless and --idle-timeout flags removed in #89 are intentionally NOT
// recognised here — they will fall through to CodexArgs and be forwarded to
// the wrapped codex binary, where they were never valid (codex will reject
// them with its own error). Users should migrate to `databricks-codex serve
// [--idle-timeout D]`.
func parseArgs(args []string) (*Args, error) {
	a := &Args{}

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
				case "--port":
					if value != "" {
						a.PortFlag, _ = strconv.Atoi(value)
					} else if i+1 < len(args) {
						i++
						a.PortFlag, _ = strconv.Atoi(args[i])
					}
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
  --verbose, -v         Enable debug logging to stderr
  --log-file string     Write debug logs to a file (combinable with --verbose)
  --proxy-api-key string    Require this API key on all proxy requests (default: disabled)
  --tls-cert string         Path to TLS certificate file (requires --tls-key)
  --tls-key string          Path to TLS private key file (requires --tls-cert)
  --port int                Fixed proxy port (default: 49154, saved to state)
  --no-update-check         Skip the automatic update check on startup
  --version             Print version and exit
  --help, -h            Show this help message

Subcommands:
  config                       Persistent config editor (otel enable/disable, show)
  completion <shell>           Generate shell completions (bash, zsh, fish)
  update                       Check for a newer release and print upgrade instructions
  hooks <subcommand>           Install/uninstall SessionStart hooks (install, uninstall, session-start)
  serve [flags]                Run the proxy in headless mode (consolidates the deleted root flags)

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

// resolveOtel reads the persistent state and returns the (otel, metrics,
// logs) tuple the regular session path uses to drive proxy + config.toml
// patching. Pure function over state — no flag input — because #87 removed
// the session-time OTEL flags. The persistent-config editor
// (`databricks-codex config otel enable/disable`) is the only writer; the
// session is a read-only consumer.
//
// Semantics:
//
//   - A signal is on iff the corresponding *Disabled bit is unset AND the
//     corresponding table name is non-empty in state.
//   - Returned table strings are empty when their signal is off (so the
//     proxy's tomlconfig.Patch removes the [otel] section when both are
//     empty rather than leaving stale exporter lines).
//   - OTel as a whole is on iff at least one signal is on.
func resolveOtel(saved persistentState) (otel bool, metricsTable string, logsTable string) {
	if !saved.OtelMetricsDisabled && saved.OtelMetricsTable != "" {
		metricsTable = saved.OtelMetricsTable
	}
	if !saved.OtelLogsDisabled && saved.OtelLogsTable != "" {
		logsTable = saved.OtelLogsTable
	}
	otel = metricsTable != "" || logsTable != ""
	return otel, metricsTable, logsTable
}

// deriveLogsTable derives the OTEL logs table name from a metrics table name.
// If the metrics table ends with "_otel_metrics", replace that suffix with "_otel_logs".
// Otherwise append "_otel_logs". Ported from databricks-claude/main.go and
// kept exported (within-package) so cli_config.go's resolver can reuse it
// for the `config otel enable --metrics-table` derivation.
func deriveLogsTable(metricsTable string) string {
	if metricsTable == "" {
		return ""
	}
	if strings.HasSuffix(metricsTable, "_otel_metrics") {
		return strings.TrimSuffix(metricsTable, "_otel_metrics") + "_otel_logs"
	}
	return metricsTable + "_otel_logs"
}
