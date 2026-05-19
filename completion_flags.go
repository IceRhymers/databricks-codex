package main

import "github.com/IceRhymers/databricks-claude/pkg/completion"

// flagDefs is the ordered list of root flags fed to pkg/completion for
// shell-script generation. Order is curated here (not implied by the tree)
// because bash/zsh/fish completion output is order-sensitive — preserving
// byte-equivalence with the pre-tree binary requires the original ordering
// (--profile first, --port between --tls-key and --headless, etc.). Each
// entry is derived from rootCommand so the tree remains the single source
// of truth for which flags exist and what their descriptions / completers /
// arg semantics are.
//
// Adding a new root flag requires:
//  1. Append a FlagDef to rootCommand.Flags (or .Persistent) in commands.go.
//  2. Insert the new flag's name into the order slice below at the desired
//     position in the completion output.
// The init-time panic and the parity tests in main_test.go fail loudly if
// step 2 is forgotten or if a name in `order` doesn't appear on rootCommand.
var flagDefs = func() []completion.FlagDef {
	// Original flagDefs ordering, preserved verbatim for byte-equivalent
	// completion script generation. Two flags now live on rootCommand.Persistent
	// (--profile, --port); the rest are on rootCommand.Flags. Their ordering
	// here is independent of that partitioning.
	order := []string{
		"profile",
		"verbose",
		"version",
		"help",
		"print-env",
		"otel",
		"no-otel",
		"no-otel-metrics",
		"no-otel-logs",
		"otel-metrics-table",
		"otel-logs-table",
		"model",
		"upstream",
		"log-file",
		"proxy-api-key",
		"tls-cert",
		"tls-key",
		"port",
		"headless",
		"idle-timeout",
		"no-update-check",
	}
	byName := map[string]completion.FlagDef{}
	for _, f := range rootCommand.AllFlags() {
		byName[f.Name] = f.ToCompletion()
	}
	out := make([]completion.FlagDef, 0, len(order))
	for _, name := range order {
		f, ok := byName[name]
		if !ok {
			panic("completion_flags: order entry " + name + " not declared on rootCommand")
		}
		out = append(out, f)
	}
	return out
}()

// knownFlags is the set of flag names (with "--" prefix) that databricks-codex
// owns. Anything not in this set is forwarded to the codex binary. Derived
// directly from rootCommand so it can never drift from the tree — the tree
// is the source of truth for which flags the binary recognises.
var knownFlags = rootCommand.KnownFlags()

// knownSubcommands is the recursive shell-completion subcommand tree fed
// to pkg/completion so `databricks-codex <TAB>` offers completion / update
// / hooks, and `databricks-codex hooks <TAB>` offers install / uninstall
// / session-start. #88 lifts this from rootCommand alongside the dep bump
// to databricks-claude v1.0.2 (which exports SubcommandDef); see the
// doc-comment in internal/cmd/cmd.go for the prior #86 omission.
var knownSubcommands = rootCommand.CompletionSubcommands()
