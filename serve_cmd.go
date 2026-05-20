package main

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/IceRhymers/databricks-codex/internal/cmd"
)

// runServeCommand dispatches `databricks-codex serve [flags]`. args is
// everything after the literal "serve" token (e.g. ["--idle-timeout", "5m"]).
//
// Mirrors hooksCommand's runHooksCommand shape but is a leaf — there are no
// sub-subcommands. databricks-claude #174 has install/uninstall/status here
// for daemon-mode OS service registration; codex has no daemon mode (no
// LaunchAgent / systemd-user / schtasks equivalent yet) so those are
// deferred.
//
// Flag parsing is driven by serveCommand.Parse so the tree is the single
// source of truth for the flag set. The runner constructs an Args struct
// with Headless=true and the parsed IdleTimeout, then calls runProxyMode —
// the same launcher the legacy `databricks-codex --headless` path used. The
// Args struct (declared in main.go) keeps Headless/IdleTimeout fields for
// exactly this reason; parseArgs no longer populates them after #89.
func runServeCommand(args []string) error {
	parsed, err := serveCommand.Parse(args)
	if err != nil {
		return fmt.Errorf("serve: %w", err)
	}
	if parsed.Bools["help"] {
		_ = cmd.Render(os.Stdout, serveCommand, nil)
		return nil
	}

	a, err := buildServeArgs(parsed)
	if err != nil {
		return err
	}

	runServeProxy(a)
	return nil
}

// runServeProxy is the proxy-launch hook that runServeCommand invokes after
// flag parsing. Replaceable from tests so serve_cmd_test.go can spy on the
// Args struct without actually starting the proxy.
//
// In production this points at runProxyMode (defined in main.go), which is
// the same launcher the transparent-wrapper path uses. The branch inside
// runProxyMode that wraps the handler with /shutdown + idle-timeout fires
// when a.Headless is true — i.e. exactly what the serve dispatcher sets it
// to.
var runServeProxy = func(a *Args) {
	runProxyMode(a)
}

// buildServeArgs maps a parsed cmd.ParseResult into the Args struct that
// runProxyMode consumes. The mapping is exhaustive over the flags declared
// on serveCommand in commands.go; a flag-set parity test
// (serve_cmd_test.go) catches drift in either direction.
//
// --idle-timeout is the only flag with strict parsing: an invalid duration
// is returned as a fail-loud error (matching the legacy --idle-timeout root
// flag's behaviour, including rejection of bare ints like "30" and empty
// values like "--idle-timeout=").
func buildServeArgs(r *cmd.ParseResult) (*Args, error) {
	a := &Args{
		Headless:    true,            // serve always runs headless
		IdleTimeout: 30 * time.Minute, // matches the pre-#89 default
	}

	if r.Set["idle-timeout"] {
		raw := r.Strings["idle-timeout"]
		d, err := time.ParseDuration(raw)
		if err != nil {
			return nil, fmt.Errorf("serve: --idle-timeout: %q is not a valid duration (use e.g. 30s, 5m, 1h)", raw)
		}
		a.IdleTimeout = d
	}

	a.Profile = r.Strings["profile"]
	a.Verbose = r.Bools["verbose"]
	a.LogFile = r.Strings["log-file"]
	a.Upstream = r.Strings["upstream"]
	a.ProxyAPIKey = r.Strings["proxy-api-key"]
	a.TLSCert = r.Strings["tls-cert"]
	a.TLSKey = r.Strings["tls-key"]
	a.NoUpdateCheck = r.Bools["no-update-check"]

	if r.Set["model"] {
		a.Model = r.Strings["model"]
		a.ModelSet = true
	}

	if r.Set["port"] {
		raw := r.Strings["port"]
		if raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil {
				return nil, fmt.Errorf("serve: --port: %q is not an integer", raw)
			}
			a.PortFlag = n
		}
	}

	return a, nil
}
