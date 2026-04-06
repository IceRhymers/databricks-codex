# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Pre-Commit Rules

Before any commit, you MUST read both `CLAUDE.md` and `README.md` to ensure:
- Changes are consistent with documented architecture and design decisions
- Any new flags, features, or behavioral changes are reflected in README.md
- CLAUDE.md is updated if architecture, packages, or key design decisions change

## Build & Test

```bash
make build          # produces ./databricks-codex
make test           # go test ./... -v
make lint           # go vet ./...
make install        # installs to $GOPATH/bin
make dist           # cross-compile darwin/linux/windows amd64+arm64
```

Run a single test:
```bash
go test -run TestParseArgs_Profile -v
```

Run tests for a specific package:
```bash
go test ./pkg/tomlconfig/... -v
```

## Architecture

Transparent wrapper for the OpenAI Codex CLI that auto-injects Databricks OAuth tokens. Patches `~/.codex/config.toml` to point Codex at a local proxy, which injects live Bearer tokens per-request. Config is restored on exit.

### Dependency Model

Go 1.22, single external module: `github.com/IceRhymers/databricks-claude` (the sister project). Most shared packages come from databricks-claude; only `pkg/tomlconfig` is local to this repo.

**Shared from databricks-claude:** `pkg/proxy`, `pkg/tokencache`, `pkg/childproc`, `pkg/filelock`, `pkg/registry`, `pkg/authcheck`

**Local:** `pkg/tomlconfig` — string-based TOML manipulation (no parser dependency), atomic writes via temp file + rename.

### Main Package Files

- **main.go** — CLI entry point: flag parsing (`parseArgs`), profile/otel-logs-table resolution chains, proxy startup, config.toml patching, codex child process lifecycle.
- **token.go** — `databricksFetcher` implements `tokencache.TokenFetcher` by shelling out to `databricks auth token`. Also `DiscoverHost` (via `databricks auth env`) and `ConstructGatewayURL` (via SCIM `/Me` for workspace ID).
- **config.go** — `ConfigManager` coordinates tomlconfig, filelock, and session registry. `Setup()` backs up + patches config.toml; `Restore()` handles multi-session handoff or full restore.
- **state.go** — `persistentState` JSON file at `~/.codex/.databricks-codex.json` for profile and otel-logs-table persistence across sessions.
- **process.go** — Thin shim: `RunCodex` delegates to `childproc.Run`. Declares `otelKeys` for OTEL env var injection.
- **proxy.go** — Thin shim: maps local `ProxyConfig` to `proxy.Config` from databricks-claude.
- **lock.go**, **registry.go** — Re-export types from databricks-claude packages.

### Key Design Decisions

- **Proxy-based architecture** — Local HTTP proxy on `127.0.0.1:0` injects fresh Bearer tokens. Codex connects via patched `config.toml` with `wire_api = "responses"` for WebSocket support.
- **Multi-session coordination** — Session registry tracks live PIDs in `~/.codex/.sessions.json`. Last session restores original config; earlier sessions hand off to the most recent survivor.
- **Resolution chains** — Profile: `--profile` flag > `DATABRICKS_CONFIG_PROFILE` env > saved state > `"DEFAULT"`. OTEL logs table: `--otel-logs-table` flag > saved state > default. Explicit flag values are persisted for future sessions.
- **Gateway URL** — `https://<workspaceId>.ai-gateway.cloud.databricks.com/openai/v1`. Falls back to `<host>/serving-endpoints/codex/openai/v1` if workspace ID resolution fails.
- **WebSocket proxy** — Codex v0.118.0+ uses WebSocket for the Responses API. The proxy (in databricks-claude's `pkg/proxy`) detects `Upgrade: websocket` headers and uses HTTP hijacking.

## Testing Patterns

- Tests use **helper binaries compiled at test time** to mock the `databricks` CLI (`buildHelperBinary`, `buildSlowBinary` in `token_test.go`).
- `warmToken()` in `proxy_test.go` pre-loads the token cache to avoid subprocess calls during proxy tests.
- `state_test.go` overrides `statePath` to use temp directories — follow this pattern for any test touching persistent state.
- Version is injected via `-ldflags "-X main.Version=$(VERSION)"` at build time.
