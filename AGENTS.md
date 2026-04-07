<!-- Generated: 2026-04-06 | Updated: 2026-04-06 -->

# databricks-codex

## Purpose
Transparent wrapper for the OpenAI Codex CLI that auto-injects Databricks OAuth tokens. Patches `~/.codex/config.toml` to point Codex at a local HTTP proxy, which injects live Bearer tokens per-request. Config is restored on exit. Supports multi-session coordination so concurrent instances hand off to each other gracefully.

## Key Files

| File | Description |
|------|-------------|
| `main.go` | CLI entry point: flag parsing, resolution chains for profile/model/otel-logs-table, proxy startup, config.toml patching, and Codex child process lifecycle |
| `token.go` | `databricksFetcher` implements `tokencache.TokenFetcher` via `databricks auth token`. Also `DiscoverHost` (via `databricks auth env`) and `ConstructGatewayURL` (SCIM `/Me` for workspace ID) |
| `config.go` | `ConfigManager` coordinates tomlconfig, filelock, and session registry. `Setup()` backs up + patches; `Restore()` handles multi-session handoff or full restore |
| `state.go` | `persistentState` JSON at `~/.codex/.databricks-codex.json` — persists profile, model, and otel-logs-table across sessions |
| `process.go` | Thin shim: `RunCodex` delegates to `childproc.Run`. Declares `otelKeys` for OTEL env var injection |
| `proxy.go` | Thin shim: maps local `ProxyConfig` to `proxy.Config` from databricks-claude; `StartProxy` binds `127.0.0.1:0` |
| `lock.go` | Re-exports `filelock.FileLock` from databricks-claude |
| `registry.go` | Re-exports `Session` and `SessionRegistry` from databricks-claude |
| `main_test.go` | Tests for `parseArgs`, `resolveOtelLogsTable`, `handlePrintEnv`, and flag parsing edge cases |
| `token_test.go` | Tests for `databricksFetcher`, `DiscoverHost`, `ConstructGatewayURL` using compiled helper binaries |
| `state_test.go` | Tests for `loadState`/`saveState` with temp-dir override of `statePath` |
| `proxy_test.go` | Integration tests for proxy startup and request routing |
| `process_test.go` | Tests for `RunCodex` child process behavior |
| `go.mod` | Single external dependency: `github.com/IceRhymers/databricks-claude v0.5.0` |
| `Makefile` | Build targets: `build`, `test`, `lint`, `install`, `dist` (cross-compile) |

## Subdirectories

| Directory | Purpose |
|-----------|---------|
| `pkg/` | Local packages (only `tomlconfig` lives here; see `pkg/AGENTS.md`) |

## For AI Agents

### Working In This Directory
- All Go files are `package main` — the entire root is a single binary
- Shim files (`lock.go`, `registry.go`, `process.go`, `proxy.go`) re-export or thin-wrap types from databricks-claude; keep them minimal
- The resolution chain pattern (flag → env → saved state → default) is used for `profile`, `model`, and `otel-logs-table` — follow it precisely when adding new persistent flags
- `modelSet bool` / `otelLogsTableSet bool` patterns distinguish explicit flags from defaults; new persistent flags need an analogous `*Set` variable
- Never defer `cm.Restore()` — `os.Exit()` skips deferred calls; always call it explicitly before exit

### Testing Requirements
- `make test` runs `go test ./... -v`
- Helper binaries are compiled at test time (`buildHelperBinary`, `buildSlowBinary` in `token_test.go`) to mock the `databricks` CLI
- `state_test.go` overrides `statePath` to use temp dirs — follow this pattern for any test touching persistent files
- `proxy_test.go` uses `warmToken()` to pre-load the token cache and avoid subprocess calls

### Common Patterns
- Atomic writes via temp file + rename (see `state.go:saveState`, `tomlconfig.atomicWrite`)
- Flag parsing is manual (no `flag` stdlib package) to allow pass-through of unknown flags to Codex
- `--` separator explicitly passes remaining args to Codex

## Dependencies

### Internal
- `pkg/tomlconfig` — string-based TOML manipulation for config.toml patching

### External (from `github.com/IceRhymers/databricks-claude`)
- `pkg/proxy` — local HTTP/WebSocket proxy server with token injection
- `pkg/tokencache` — token provider with auto-refresh (5-min buffer before expiry)
- `pkg/childproc` — child process lifecycle with signal forwarding
- `pkg/filelock` — file-based locking for config.toml coordination
- `pkg/registry` — session registry tracking live PIDs in `.sessions.json`
- `pkg/authcheck` — pre-flight authentication check

<!-- MANUAL: Any manually added notes below this line are preserved on regeneration -->
