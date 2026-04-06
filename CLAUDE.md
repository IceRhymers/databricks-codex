# databricks-codex

Transparent wrapper for the OpenAI Codex CLI that auto-injects Databricks OAuth tokens via the Databricks CLI.

## Architecture

Three source files plus a proxy shim, all in `package main`. Shared packages live in `pkg/`.

- **main.go** — CLI entry point: parses flags, seeds the token cache, discovers the AI Gateway URL, injects `OPENAI_BASE_URL` and `OPENAI_API_KEY` into the environment, then `syscall.Exec`s `codex` (replacing the current process).
- **token.go** — `TokenProvider` fetches and caches Databricks access tokens by shelling out to `databricks auth token`. Also contains `DiscoverHost`, `ResolveWorkspaceID`, and `ConstructGatewayURL` for auto-discovery. Gateway URL uses `/openai/v1` suffix (not `/anthropic`).
- **process.go** — `RunCodex` launches codex as a child process via `pkg/childproc`. `ForwardSignals` relays SIGINT/SIGTERM. Also declares `otelKeys` for OTEL env var injection.
- **proxy.go** — Thin shim over `pkg/proxy`: `ProxyConfig`, `NewProxyServer`, `StartProxy`. Provides HTTP reverse proxy with inference and OTEL routing (available for future use but not currently wired into the main exec path).

### Shared packages (pkg/)

- **pkg/tokencache** — Thread-safe token cache with 5-minute refresh buffer. Used by `token.go`.
- **pkg/childproc** — Child process runner with signal forwarding and exit code propagation.
- **pkg/proxy** — HTTP reverse proxy with two routes: `/` for inference, `/otel/` for telemetry. Injects Bearer tokens and custom headers per-request.

## Key Design Decisions

- **Exec-based (not proxy-based)** — Unlike `databricks-claude` which runs a local HTTP proxy and patches `settings.json`, `databricks-codex` uses `syscall.Exec` to replace itself with `codex` after injecting `OPENAI_BASE_URL` and `OPENAI_API_KEY` into the environment. This is simpler because Codex reads env vars directly (no settings file to patch/restore).
- **Zero external Go dependencies** — pure stdlib only. Shared packages are vendored in `pkg/`.
- **Token cache** — mutex-guarded `TokenProvider` in `pkg/tokencache`. Tokens are cached and refreshed 5 minutes before expiry.
- **Gateway URL format** — `<host>/serving-endpoints/<workspaceId>.aws.proxy.codex/openai/v1`. Falls back to `<host>/serving-endpoints/codex/openai/v1` if workspace ID resolution fails.

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
