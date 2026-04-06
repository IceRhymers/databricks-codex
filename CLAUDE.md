# databricks-codex

Transparent wrapper for the OpenAI Codex CLI that auto-injects Databricks OAuth tokens via the Databricks CLI. Manages `~/.codex/config.toml` and runs a local proxy with live token refresh — the Codex equivalent of `databricks-claude`.

## Architecture

Main-package source files plus shared packages in `pkg/`.

- **main.go** — CLI entry point: parses flags (`--profile`, `--otel`, etc.), seeds the token cache, discovers the AI Gateway URL, starts the local proxy, patches `~/.codex/config.toml` to point Codex at the proxy, runs codex as a child process, restores config on exit.
- **token.go** — `TokenProvider` fetches and caches Databricks access tokens by shelling out to `databricks auth token --profile <profile>`. Also contains `DiscoverHost`, `ResolveWorkspaceID`, and `ConstructGatewayURL` for auto-discovery.
- **config.go** — `ConfigManager` coordinates config.toml patching, file locking, and multi-session registration. Equivalent of databricks-claude's `SettingsManager`.
- **process.go** — `RunCodex` launches codex as a child process via `pkg/childproc`. `ForwardSignals` relays SIGINT/SIGTERM. Also declares `otelKeys` for OTEL env var injection.
- **proxy.go** — Thin shim over `pkg/proxy`: `ProxyConfig`, `NewProxyServer`, `StartProxy`.
- **lock.go** — Re-exports `pkg/filelock.FileLock`.
- **registry.go** — Re-exports `pkg/registry.SessionRegistry`.

### Shared packages (pkg/)

- **pkg/tokencache** — Thread-safe token cache with 5-minute refresh buffer.
- **pkg/childproc** — Child process runner with signal forwarding and exit code propagation.
- **pkg/proxy** — HTTP reverse proxy with two routes: `/` for inference (with WebSocket upgrade support for the Responses API), `/otel/` for telemetry. Injects Bearer tokens per-request. WebSocket connections are proxied via HTTP hijacking.
- **pkg/tomlconfig** — Reads, patches, and restores Codex's `config.toml`. Simple string-based manipulation (no TOML parser dependency). Atomic writes via temp file + rename.
- **pkg/filelock** — Exclusive file locking via `syscall.Flock` for inter-process coordination.
- **pkg/registry** — Multi-session coordination. Tracks live sessions in `~/.codex/.sessions.json`, auto-prunes stale PIDs, supports handoff to surviving sessions on exit.

## Key Design Decisions

- **Proxy-based with config.toml management** — Like `databricks-claude` (which patches `settings.json`), `databricks-codex` patches `~/.codex/config.toml` to point Codex at a local proxy. The proxy injects live Bearer tokens on every request/WebSocket connection. Config is restored on exit.
- **WebSocket proxy** — Codex v0.118.0+ uses WebSocket connections to `/responses` (the Responses API). The proxy detects `Upgrade: websocket` headers and uses HTTP hijacking + bidirectional piping instead of `httputil.ReverseProxy`.
- **Multi-session coordination** — A session registry tracks live processes. On exit, the last session restores the original config; earlier sessions hand off to the most recent survivor.
- **Profile resolution chain** — `--profile` flag → `DATABRICKS_CONFIG_PROFILE` env var → `"DEFAULT"`.
- **Zero external Go dependencies** — pure stdlib only. Shared packages are vendored in `pkg/`.
- **Token cache** — mutex-guarded `TokenProvider` in `pkg/tokencache`. Tokens are cached and refreshed 5 minutes before expiry.
- **Gateway URL format** — `https://<workspaceId>.ai-gateway.cloud.databricks.com/openai/v1`. Falls back to `<host>/serving-endpoints/codex/openai/v1` if workspace ID resolution fails.

## Testing

- Tests use **helper binaries compiled at test time** to mock the `databricks` CLI (see `buildHelperBinary`, `buildSlowBinary` in `token_test.go`).
- `warmToken()` in `proxy_test.go` pre-loads the token cache to avoid subprocess calls during proxy tests.
- `process_test.go` tests `RunCodex` error handling and verifies OTEL key declarations.
- Run: `make test` or `go test ./... -v`

## Build

- `make build` — produces `./databricks-codex`
- `make install` — installs to `$GOPATH/bin`
- `make dist` — cross-compiles for darwin/linux/windows amd64+arm64
- `make lint` — runs `go vet`
