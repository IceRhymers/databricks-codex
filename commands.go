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
// #86 migrates only the *root* command. config / hooks / serve continue
// to live as root flags + their existing dispatch in main.go; their
// migration onto Subcommands is tracked in #87/#88/#89. --profile and
// --port are already declared as Persistent so subcommand inheritance
// works out of the box once those migrations land.
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
		{Name: "install-hooks", Description: "Install SessionStart hook into ~/.codex/hooks.json"},
		{Name: "uninstall-hooks", Description: "Remove databricks-codex hooks from ~/.codex/hooks.json"},
		{Name: "headless-ensure", Description: "Start proxy if not running — called by the SessionStart hook"},
		{Name: "no-update-check", Description: "Skip the automatic update check on startup", EnvVar: "DATABRICKS_NO_UPDATE_CHECK"},
	},

	// Subcommands declared on the root. completion and update dispatch
	// from main.go directly today; config/hooks/serve children are not
	// yet on the tree (they're still root flags — see #87/#88/#89).
	Subcommands: []cmd.Command{
		completionCommand,
		updateCommand,
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
