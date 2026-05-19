package main

import (
	"github.com/IceRhymers/databricks-codex/internal/cmd"
)

// rootCommand is the source-of-truth declaration for the databricks-codex
// CLI. It drives:
//   - parseArgs → knownFlags (the set of "--flag" names the binary owns;
//     anything else is forwarded transparently to the wrapped codex binary).
//   - flagDefs (in completion_flags.go) → the bash/zsh/fish completion
//     scripts (fed via pkg/completion).
//
// Adding a new root flag requires three edits:
//  1. Append a FlagDef to Flags (or Persistent for inherited flags) here.
//  2. Add a case to the switch in parseArgs (main.go) that wires the flag
//     into the Args struct.
//  3. Add the matching field to the Args struct.
//
// The parity tests in main_test.go (TestRootTreeFlagsAreParseRecognised,
// TestParseArgsCasesAreDeclaredInRootTree) fail loudly if step 1 and 2
// drift apart — the tree is the single source of truth.
//
// #86 migrated the *root* command. #88 lifts the hooks lifecycle flags
// (--install-hooks / --uninstall-hooks / --headless-ensure) onto a
// `hooks` subcommand; the legacy root-flag spellings are removed and any
// invocation that used them now errors (or, if completely unrecognised,
// is forwarded to codex). --profile and --port live on Persistent so
// subcommand inheritance works for the new tree. config / serve are
// still tracked under #87/#89.
var rootCommand = cmd.Command{
	Name:  "databricks-codex",
	Short: "Databricks AI Gateway wrapper for OpenAI Codex CLI",

	// Persistent flags are inherited by every subcommand once those
	// commands migrate onto the tree (#87/#88/#89). For now, declaring
	// them here is a no-op for the existing inline-handled root flags
	// but ensures the tree is shaped correctly for the follow-up. Both
	// flags also feed the resolution chain in main.go today; the
	// StateKey/EnvVar/Default fields below document that behavior so
	// later sub-issues can derive the chain from this declaration.
	Persistent: []cmd.FlagDef{
		{
			Name:        "profile",
			Description: "Databricks CLI profile (default: DEFAULT)",
			TakesArg:    true,
			Completer:   "__databricks_profiles",
			StateKey:    "profile",
			EnvVar:      "DATABRICKS_CONFIG_PROFILE",
			MDMKey:      "databricksProfile",
			Default:     "DEFAULT",
		},
		{
			Name:        "port",
			Description: "Proxy listen port (default: 49154)",
			TakesArg:    true,
			StateKey:    "port",
			Default:     "49154",
		},
	},

	// Order matches the legacy flagDefs slice so the bash/zsh/fish
	// completion output stays byte-identical with the pre-tree binary.
	// The two flags ("profile" and "port") that previously appeared in
	// the flagDefs slice now live under Persistent (which renders first
	// in AllFlags), so completion ordering needs to mirror that — see
	// completion_flags.go where flagDefs is rebuilt from rootCommand.
	Flags: []cmd.FlagDef{
		{Name: "verbose", Short: "v", Description: "Enable debug logging to stderr"},
		{Name: "version", Description: "Print version and exit"},
		{Name: "help", Short: "h", Description: "Show help message"},
		{Name: "print-env", Description: "Print resolved configuration (token redacted) and exit"},
		{Name: "otel", Description: "Enable OpenTelemetry export (metrics + logs)"},
		{Name: "no-otel", Description: "Disable OpenTelemetry for this session (saved tables preserved)"},
		{Name: "no-otel-metrics", Description: "Disable metrics for this session (saved table preserved)"},
		{Name: "no-otel-logs", Description: "Disable logs for this session (saved table preserved)"},
		{Name: "otel-metrics-table", Description: "Unity Catalog table for OTel metrics (cat.schema.table)", TakesArg: true, StateKey: "otel_metrics_table"},
		{Name: "otel-logs-table", Description: "Unity Catalog table for OTel logs (cat.schema.table)", TakesArg: true, StateKey: "otel_logs_table"},
		{Name: "model", Description: "Model to use (default: databricks-claude-sonnet-4-5)", TakesArg: true, StateKey: "model"},
		{Name: "upstream", Description: "Override upstream codex binary path", TakesArg: true, Completer: "__files"},
		{Name: "log-file", Description: "Write debug logs to file (combinable with --verbose)", TakesArg: true, Completer: "__files"},
		{Name: "proxy-api-key", Description: "Require this API key on all proxy requests", TakesArg: true},
		{Name: "tls-cert", Description: "TLS certificate file for the local proxy (requires --tls-key)", TakesArg: true, Completer: "__files"},
		{Name: "tls-key", Description: "TLS private key file for the local proxy (requires --tls-cert)", TakesArg: true, Completer: "__files"},
		{Name: "headless", Description: "Start proxy without launching codex (for IDE extensions or hooks)"},
		{Name: "idle-timeout", Description: "Idle timeout for headless mode (default: 30m; 0 disables)", TakesArg: true, Default: "30m"},
		{Name: "no-update-check", Description: "Skip the automatic update check on startup", EnvVar: "DATABRICKS_NO_UPDATE_CHECK"},
	},

	// Subcommands declared on the root. completion and update dispatch
	// from main.go directly. #88 adds `hooks` (install/uninstall/
	// session-start); config/serve are still tracked under #87/#89.
	Subcommands: []cmd.Command{
		completionCommand,
		updateCommand,
		hooksCommand,
	},
}

// completionCommand declares the `completion` subcommand and its three
// shell-target children. Dispatch lives in main.go (calls completion.Run);
// the tree exists so shell completion can offer "completion <TAB>" → bash
// / zsh / fish, and so the help renderer has a node to describe.
var completionCommand = cmd.Command{
	Name:  "completion",
	Short: "Generate shell completion scripts (bash, zsh, fish)",
	Subcommands: []cmd.Command{
		{Name: "bash", Short: "Generate bash completion script"},
		{Name: "zsh", Short: "Generate zsh completion script"},
		{Name: "fish", Short: "Generate fish completion script"},
	},
}

// updateCommand declares the `update` subcommand. Dispatch lives in main.go
// (calls updater.Check + prints upgrade instructions).
var updateCommand = cmd.Command{
	Name:  "update",
	Short: "Check for a newer release and print upgrade instructions",
}

// hooksCommand declares the `hooks` subcommand tree introduced in #88.
// Consolidates the 3 hooks-lifecycle root flags (--install-hooks,
// --uninstall-hooks, --headless-ensure) under a discoverable subcommand.
// install/uninstall manage the SessionStart entries in ~/.codex/hooks.json;
// session-start is hook-invoked refcount-managed proxy lifecycle internal
// (formerly --headless-ensure).
//
// Tree shape:
//
//	hooks
//	├── install        [--profile P] [--port N]
//	├── uninstall
//	└── session-start  [--port N]   (hook-invoked internal)
//
// The hook-install logic (installHooks/uninstallHooks in hooks.go) and the
// proxy-ensure logic (headlessEnsure) are unchanged behaviorally — they
// move behind tree commands. The detector that matches "databricks-codex
// --headless"-prefixed entries continues to recognise hooks installed by
// the legacy flag spellings, so a re-install replaces them cleanly with
// the new "databricks-codex hooks session-start" command line.
var hooksCommand = cmd.Command{
	Name:  "hooks",
	Short: "Session-hook deployment mode: install/uninstall + lifecycle internals",
	Long:  hooksHelpTemplate,
	Subcommands: []cmd.Command{
		{
			Name:  "install",
			Short: "Install SessionStart hook into ~/.codex/hooks.json",
			Long:  hooksInstallHelpTemplate,
			Flags: []cmd.FlagDef{
				{Name: "help", Short: "h", Description: "Show help message"},
			},
		},
		{
			Name:  "uninstall",
			Short: "Remove databricks-codex hooks from ~/.codex/hooks.json",
			Long:  hooksUninstallHelpTemplate,
			Flags: []cmd.FlagDef{
				{Name: "help", Short: "h", Description: "Show help message"},
			},
		},
		{
			Name:  "session-start",
			Short: "Start proxy if not running (invoked by the SessionStart hook — internal)",
			Long:  hooksSessionStartHelpTemplate,
			Flags: []cmd.FlagDef{
				{Name: "port", Description: "Proxy listen port (default: saved state > 49154)", TakesArg: true, StateKey: "port", Default: "49154"},
				{Name: "help", Short: "h", Description: "Show help message"},
			},
		},
	},
}

const hooksHelpTemplate = `Usage: databricks-codex hooks <subcommand> [flags]

Session-hook deployment mode for the OpenAI Codex CLI. Installs hook
entries into ~/.codex/hooks.json that spin a refcount-managed proxy up on
SessionStart — making 'databricks-codex' auto-launch with every codex
session without a long-lived daemon.

Subcommands:
  install        Install the SessionStart hook into ~/.codex/hooks.json
                 (idempotent). Also flips [features] hooks = true in
                 ~/.codex/config.toml so codex actually reads the file.
  uninstall      Remove databricks-codex hooks from
                 ~/.codex/hooks.json. Tolerates "not installed".
  session-start  Hook-invoked internal: starts the proxy if it isn't
                 already running. Called by the SessionStart hook JSON
                 written by 'hooks install'. Not intended to be invoked
                 directly.

Run 'databricks-codex hooks <subcommand> --help' for per-subcommand flags.

Examples:
  # First-time install on a developer machine:
  databricks-codex hooks install

  # Remove hooks (e.g. when switching back to the manual proxy mode):
  databricks-codex hooks uninstall

Exit codes:
  0   success
  1   write/discovery failure
  2   missing or unknown subcommand
`

const hooksInstallHelpTemplate = `Usage: databricks-codex hooks install [flags]

Install the SessionStart hook into ~/.codex/hooks.json so that every
codex session auto-launches a refcount-managed databricks-codex proxy
in the background. Idempotent — safe to re-run after upgrades.

Also ensures [features] hooks = true in ~/.codex/config.toml; without
that flag codex does not read hooks.json at all.

Generated hook JSON:
  SessionStart → "databricks-codex hooks session-start"

Flags:
  --help, -h    Show this help message
`

const hooksUninstallHelpTemplate = `Usage: databricks-codex hooks uninstall [flags]

Remove databricks-codex hook entries from ~/.codex/hooks.json. Other
user-authored hooks survive byte-identical. Also removes the
[features] hooks = true line databricks-codex installed; legacy
codex_hooks = true (if present) is left untouched.

If hooks.json doesn't exist, this is a no-op.

Flags:
  --help, -h    Show this help message
`

const hooksSessionStartHelpTemplate = `Usage: databricks-codex hooks session-start [flags]

Hook-invoked internal: probes the local proxy on the configured port
and, if absent, starts a detached headless databricks-codex process
that exits via idle timeout. Replaces the legacy --headless-ensure
flag. Not intended for direct invocation — the SessionStart hook
JSON written by 'hooks install' calls this command.

MUST remain fast and fail-fast: no interactive auth flow. If the user
hasn't run 'databricks auth login' yet, this command exits 0 silently
so the codex session is not blocked on a hook timeout.

Flags:
  --port int    Override saved port (default: saved state > 49154)
  --help, -h    Show this help message
`
