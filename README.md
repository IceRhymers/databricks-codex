# databricks-codex

> **Disclaimer:** This is an unofficial, community-built workaround to enable Databricks OAuth SSO authentication with this AI coding tool. It is not supported, endorsed, or recognized by Databricks. Use at your own risk.


Transparent wrapper for the OpenAI Codex CLI that runs a local proxy backed by Databricks OAuth — so you never manually paste or refresh a token again.

## The Problem

Databricks AI Gateway uses short-lived OAuth tokens. Without this tool, you'd need to manually refresh a token, point Codex at the right Databricks endpoint, and keep that token fresh for the duration of a session.

## How It Works

`databricks-codex` wraps the `codex` binary. It:

1. Fetches a fresh Databricks OAuth token via `databricks auth token`
2. Discovers your workspace host from `databricks auth env`
3. Constructs the Databricks AI Gateway URL (`{host}/ai-gateway/openai/v1`)
4. Binds a local proxy on `127.0.0.1:49154` (fixed port — shared across concurrent sessions) that forwards traffic upstream and refreshes the Databricks token automatically
5. Writes `~/.codex/config.toml` once to point at the proxy (idempotent — no restore on exit)
6. Exec's `codex` with your args — fully transparent

You use it exactly like `codex`. Every flag and argument is forwarded.

## Installation

### Via Homebrew (recommended)

```bash
brew tap IceRhymers/tap
brew install databricks-codex
```

### Via Scoop (Windows)

```powershell
scoop bucket add icerhymers https://github.com/IceRhymers/scoop-bucket
scoop install databricks-codex
```

### Direct binary (Windows)

Download the latest release from the [releases page](https://github.com/IceRhymers/databricks-codex/releases), pick `databricks-codex-windows-amd64.exe` (or `arm64`), rename it to `databricks-codex.exe`, and place it somewhere on your `PATH`.

### From source

```bash
go install github.com/IceRhymers/databricks-codex@latest
```

### Alias (optional but recommended)

```bash
echo 'alias codex="databricks-codex"' >> ~/.zshrc  # or ~/.bashrc
```

## Prerequisites

- Go 1.22+
- [Databricks CLI](https://docs.databricks.com/dev-tools/cli/databricks-cli.html) installed and authenticated (`databricks auth login`)
- [OpenAI Codex CLI](https://github.com/openai/codex) installed
- A Databricks Model Serving endpoint with [AI Gateway](https://docs.databricks.com/aws/en/ai-gateway/) enabled (currently in public Beta)

## Usage

```bash
# Use exactly like codex:
databricks-codex "explain this codebase"

# Verbose logging (debug output to stderr):
databricks-codex --verbose "fix the bug in main.go"

# Log to file:
databricks-codex --log-file /tmp/dc.log "fix the bug in main.go"

# Both stderr and file:
databricks-codex -v --log-file /tmp/dc.log "fix the bug in main.go"

# Override upstream URL for the local proxy:
databricks-codex --upstream https://adb-123456789.azuredatabricks.net/ai-gateway/openai/v1 "summarize this PR"
```

## Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--verbose`, `-v` | `false` | Enable debug logging to stderr |
| `--log-file` | | Write debug logs to a file (combinable with `--verbose`) |
| `--profile` | saved/`DEFAULT` | Databricks CLI profile (saved to state file; `--profile` flag writes it once) |
| `--model` | `databricks-gpt-5-5` | Model to use (saved for future sessions) |
| `--port` | `49154` | Proxy listen port (saved for future sessions) |
| `--upstream` | auto-discovered | Override the upstream inference URL the local proxy forwards to |
| `--proxy-api-key` | disabled | Require this API key on all local proxy requests |
| `--tls-cert` | | TLS certificate file for the local proxy (requires `--tls-key`) |
| `--tls-key` | | TLS private key file for the local proxy (requires `--tls-cert`) |
| `--version` | | Print version and exit |
| `--help`, `-h` | | Print wrapper flags and exit. To see codex's own help, use `databricks-codex -- --help` (the `--` separator forwards everything after it to codex unchanged) |

Headless / idle-timeout knobs live under `databricks-codex serve` (see [`serve` Subcommand](#serve-subcommand)).
Hook installation lives under `databricks-codex hooks` (see [`hooks` Subcommand](#hooks-subcommand)).

All other flags and args are forwarded to `codex`.

> **Breaking in v0.12.0:** the persistent-config root flags `--otel`,
> `--no-otel`, `--no-otel-metrics`, `--no-otel-logs`, `--otel-metrics-table`,
> `--otel-logs-table`, and `--print-env` are gone. They moved under
> `databricks-codex config` — see the [config Subcommand](#config-subcommand)
> section below.

> **Breaking in v0.13.0:** the session-mode root flags `--headless` and
> `--idle-timeout` are gone. They moved under `databricks-codex serve` —
> see the [serve Subcommand](#serve-subcommand) section below. The
> SessionStart hook installed by `databricks-codex hooks install`
> continues to work transparently — it spawns `databricks-codex serve`
> internally now.

## config Subcommand

`databricks-codex config <subcommand>` is the persistent-config editor.
Mutations are written to `~/.codex/.databricks-codex.json` (the state file)
so they take effect on the **next** `databricks-codex` invocation; the
session you're currently in is unaffected. `~/.codex/config.toml` is not
touched by `config.*` commands — it is owned by the proxy lifecycle and
re-emitted at every session start based on the state file.

| Command | Replaces | Description |
|---------|----------|-------------|
| `config otel enable [--metrics-table T] [--logs-table T] [--profile P]` | `--otel`, `--otel-metrics-table`, `--otel-logs-table` | Persist OTEL table preferences. Logs table derives from metrics table when only `--metrics-table` is given. With no flags and no saved state, applies the legacy default `main.codex_telemetry.codex_otel_metrics`. |
| `config otel disable [--metrics] [--logs]` | `--no-otel`, `--no-otel-metrics`, `--no-otel-logs` | Mark OTEL signals off in state. With no flags, both signals disabled (legacy `--no-otel` semantics). Table-name preferences are **preserved** — a future `config otel enable` restores them without re-typing. |
| `config show` | `--print-env` | Print the resolved configuration (token redacted) and exit. Read-only. |

```bash
# Enable with custom metrics + logs tables — table names persist to state:
databricks-codex config otel enable \
  --metrics-table main.codex_telemetry.codex_otel_metrics \
  --logs-table   main.codex_telemetry.codex_otel_logs

# Disable just metrics; logs keep exporting on the next session:
databricks-codex config otel disable --metrics

# Disable both signals (legacy --no-otel) — table names remain in state:
databricks-codex config otel disable

# Re-enable later — saved tables resurface, no re-typing:
databricks-codex config otel enable
```

## Auto-Discovery

On startup, `databricks-codex` auto-discovers:

- Your workspace host from `databricks auth env`
- Constructs the AI Gateway URL: `{host}/ai-gateway/openai/v1`

## Debugging

### Verify your resolved configuration

Run `config show` to print the resolved profile, Databricks host, upstream base URL, redacted token placeholder, OpenTelemetry metrics and logs tables, and detected Codex binary path, then exit without launching Codex.

```bash
databricks-codex config show
```

Example output:

```
databricks-codex configuration:
  Profile:             DEFAULT
  DATABRICKS_HOST:     https://adb-1234567890123456.7.azuredatabricks.net
  OPENAI_BASE_URL:     https://adb-1234567890123456.7.azuredatabricks.net/ai-gateway/openai/v1
  Auth Token:          **** (redacted)
  OTEL Metrics Table:  main.codex_telemetry.codex_otel_metrics
  OTEL Logs Table:     main.codex_telemetry.codex_otel_logs
  Codex binary:        /usr/local/bin/codex
```

Notes:

- `OPENAI_BASE_URL` is the resolved upstream Databricks endpoint, not the localhost proxy address
- `Auth Token` is always redacted in this output
- `Codex binary` shows `(not found)` if `codex` is not on your `PATH`

If the profile, host, or URL looks wrong, check your Databricks CLI setup with `databricks auth env` and `databricks auth token`.

## Proxy behavior

`databricks-codex` does not rely on exporting environment variables. Instead, it binds a fixed local proxy on `127.0.0.1:49154` and writes `~/.codex/config.toml` once to point Codex at that proxy (including a placeholder `api_key` — the proxy injects the real Databricks token per-request).

This lets the wrapper:

- Refresh Databricks OAuth tokens automatically during long Codex sessions
- Keep Codex pointed at a stable local endpoint while upstream credentials rotate
- Support multiple concurrent sessions — first session owns the port, others join; last session out closes the listener

### Transport: SSE only, no WebSocket

Codex's model client prefers a **WebSocket** transport for the Responses API and only falls back to HTTP/SSE when the WebSocket handshake fails or the provider opts out. The decision is controlled by a per-provider boolean flag (`supports_websockets`, default `false`):

- [`codex-rs/core/src/client.rs:1601-1640`](https://github.com/openai/codex/blob/main/codex-rs/core/src/client.rs#L1601-L1640) — `stream` method: tries WebSocket first when enabled, falls back to `stream_responses_api` (SSE) on `FallbackToHttp`.
- [`codex-rs/core/src/client.rs:798-806`](https://github.com/openai/codex/blob/main/codex-rs/core/src/client.rs#L798-L806) — `responses_websocket_enabled` gate.
- [`codex-rs/model-provider-info/src/lib.rs:134-136`](https://github.com/openai/codex/blob/main/codex-rs/model-provider-info/src/lib.rs#L134-L136) — the `supports_websockets` field on `ModelProviderInfo`.

The Databricks AI Gateway speaks **SSE** over `POST /v1/responses` and does **not** accept WebSocket upgrade requests — a Codex client that tries to upgrade will get HTTP 400 from upstream. `databricks-codex` explicitly writes `supports_websockets = false` into `[model_providers.databricks-proxy]` to keep Codex on the SSE path. **Do not flip this to `true`** in your own edits to `~/.codex/config.toml` — the wrapper rewrites the section idempotently at every session start, but any hand-edits in between will break the next session until then.

This is the same code path for **TUI, GUI, and IDE extensions** — all three frontends run `codex-core`'s `ModelClient` and respect `supports_websockets`. The decision is per-provider, not per-frontend.

### Root-level `model_provider` override (GUI / raw `codex` fix)

The Codex GUI and certain IDE extensions don't honor profile-v2 layering — they read `model_provider` and `model` from the **root** of `~/.codex/config.toml`, not from the active profile. Without a root-level override they fall through to the built-in `openai` provider (where `supports_websockets = true` is hardcoded) and attempt a WebSocket upgrade against whatever base URL is configured, yielding HTTP 400 from Databricks.

This is confirmed by upstream [openai/codex#13041](https://github.com/openai/codex/issues/13041) — the recommended workaround is exactly a root-level provider override. `databricks-codex` now writes both:

```toml
# Root of ~/.codex/config.toml
model_provider = "databricks-proxy"
model          = "<resolved model>"

[model_providers.databricks-proxy]
name                = "Databricks Proxy"
base_url            = "http://127.0.0.1:49154"
api_key             = "databricks-proxy"
wire_api            = "responses"
supports_websockets = false
```

Plus the profile-v2 sibling file at `~/.codex/databricks-proxy.config.toml` for the transparent-wrapper path that passes `--profile databricks-proxy`. Both paths converge on the same provider; the root keys exist so the GUI / raw `codex` (no profile) also routes through the proxy. Your original `model_provider` and `model` values are saved on Backup and restored on session exit.

### View full usage

`databricks-codex --help` (or `-h`) prints the wrapper's own flags and exits. It does **not** append `codex --help` — mixing the two made it impossible to tell which flags belong to the wrapper vs the agent. To see codex's own help, use the `--` separator:

```sh
databricks-codex -- --help                # forwards --help to codex unchanged
databricks-codex -- --model o3 -p "hi"    # forwards extra flags to codex
```

Anything after `--` is passed to `codex` verbatim.

## serve Subcommand

`databricks-codex serve` runs the proxy in headless mode without launching `codex`. Useful when an IDE extension or other tool needs the proxy up but doesn't want a child `codex` process. Replaces the legacy root flags `--headless` and `--idle-timeout`, which were removed in v0.13.

| Flag | Default | Description |
| --- | --- | --- |
| `--idle-timeout` | `30m` | Shut the proxy down after this much idle time. `0` disables (long-running IDE sessions). Accepts Go duration strings: `30s`, `5m`, `1h`, `2h30m`. Bare integers (e.g. `30`) are rejected. |
| `--profile` | state file > `DEFAULT` | Databricks CLI profile. |
| `--port` | state file > `49154` | Proxy listen port. |
| `--model` | saved | Model name (saved to state file when supplied). |
| `--upstream` | auto-discovered | Override the AI Gateway URL. |
| `--log-file` | | Write debug logs to this file (combinable with `--verbose`). |
| `--verbose`, `-v` | `false` | Enable debug logging to stderr. |
| `--proxy-api-key` | disabled | Require this API key on all proxy requests. |
| `--tls-cert` | | TLS certificate file (requires `--tls-key`). |
| `--tls-key` | | TLS private key file (requires `--tls-cert`). |
| `--no-update-check` | | Skip the automatic update check on startup. |
| `--help`, `-h` | | Show help and exit. |

The proxy prints `PROXY_URL=http://127.0.0.1:<port>` (or `https://...` with TLS) to stdout once bound. It exits when:

- `SIGINT` or `SIGTERM` is received.
- `POST /shutdown` is hit on the proxy URL.
- `--idle-timeout` elapses with zero in-flight requests.

```bash
# Default 30-minute idle timeout:
databricks-codex serve

# Tight timeout for tests / CI:
databricks-codex serve --idle-timeout 5m

# Disable idle shutdown for a long-running IDE session:
databricks-codex serve --idle-timeout 0
```

The SessionStart hook installed by `databricks-codex hooks install` spawns `databricks-codex serve` under the hood — you don't need to invoke this manually for the hooks path.

### Using your own codex client against `serve`

When you launch `codex` (TUI, GUI, or IDE extension) yourself against a `serve`-mode proxy — instead of going through the `databricks-codex` transparent wrapper — **you must tell codex to use the `databricks-proxy` profile** so it picks up the sibling config layer (`~/.codex/databricks-proxy.config.toml`) that points at the local proxy:

```bash
# TUI / one-shot exec
codex --profile databricks-proxy "explain this codebase"
codex exec --profile databricks-proxy "fix the bug in main.go"

# Persist as the default profile so you don't have to type it every time
codex config set profile databricks-proxy
```

For the **Codex GUI / IDE extension**, set the active profile to `databricks-proxy` in its settings — the wrapper `databricks-codex` (transparent mode) handles this for you automatically by prepending `--profile databricks-proxy`, but standalone `codex` invocations against a `serve`-mode proxy don't.

> **Why:** Codex's profile-v2 layout (released 2026) requires the proxy config to live in a sibling file (`<profile>.config.toml`) selected by `--profile`, rather than as a root `profile = "..."` key in base `config.toml`. The transparent wrapper injects this flag; `serve` mode cannot, because it doesn't launch codex.

## `hooks` Subcommand

Install hooks so every Codex session auto-starts the proxy on startup — no manual `databricks-codex serve` needed. Replaces the legacy root flags (`--install-hooks` / `--uninstall-hooks` / `--headless-ensure`), which were removed in v0.12.

> **First-time setup:** Run `databricks-codex` at least once before installing hooks. This writes `~/.codex/config.toml` so the proxy is used for all Codex sessions.

| Subcommand | Purpose |
| --- | --- |
| `databricks-codex hooks install` | Install SessionStart hook into `~/.codex/hooks.json` |
| `databricks-codex hooks uninstall` | Remove databricks-codex hooks from `~/.codex/hooks.json` |
| `databricks-codex hooks session-start` | Hook-invoked internal — start proxy if not running |

### Install

```bash
databricks-codex hooks install
```

This merges a **SessionStart** hook into `~/.codex/hooks.json` and enables the `hooks` feature flag in `~/.codex/config.toml`:

- **SessionStart** (`startup`): runs `databricks-codex hooks session-start` — starts the proxy if it isn't already running.

### Shutdown

Unlike Claude Code, the Codex CLI does not have a `SessionEnd` hook event. The proxy shuts itself down automatically after **30 minutes of inactivity** (configurable via `databricks-codex serve --idle-timeout <dur>` for direct invocations; the SessionStart hook spawns the proxy with the default 30-minute timeout). You can also stop it manually with `POST /shutdown` or by sending a signal to the process.

### Uninstall

```bash
databricks-codex hooks uninstall
```

Removes only the databricks-codex hook entries. Other hooks in your `hooks.json` are untouched.

### Notes

- Safe to rerun `hooks install` after upgrades — existing hooks are replaced, not duplicated.
- Custom port settings persist automatically via the state file (`~/.codex/.databricks-codex.json`).
- `hooks session-start` is wired by `hooks install` for you; running it manually is a no-op when the proxy is already up.
- **Codex profile selection:** the SessionStart hook only spawns the proxy — your codex client (TUI/GUI/extension) still needs to use the `databricks-proxy` profile to route through it. See [Using your own codex client against `serve`](#using-your-own-codex-client-against-serve) above. The simplest setup is `codex config set profile databricks-proxy` once, then plain `codex` always uses the proxy.

## Shell Tab Completions

`databricks-codex` can generate shell completion scripts for bash, zsh, and fish. Completions are derived from the binary's own flag metadata and stay in sync automatically.

### Install (one-time)

**bash** — add to `~/.bashrc`:
```bash
eval "$(databricks-codex completion bash)"
```

**zsh** — add to `~/.zshrc`:
```zsh
eval "$(databricks-codex completion zsh)"
```

**fish** — add to `~/.config/fish/config.fish`:
```fish
databricks-codex completion fish | source
```

### Homebrew

If installed via `brew install IceRhymers/tap/databricks-codex`, completions are installed automatically — no extra setup needed.

### What completes

- `--profile <TAB>` — lists profiles from `~/.databrickscfg` (updated live)
- `--log-file`, `--tls-cert`, `--tls-key`, `--upstream <TAB>` — file path completion
- All other flags — name completion when you type `-`

## Development

```bash
git clone https://github.com/IceRhymers/databricks-codex
cd databricks-codex
make test
make build
```

## Automatic Update Check

`databricks-codex` checks for newer releases on startup (once every 24 hours) and prints a one-line notice to stderr when an update is available. The check is synchronous with a 2-second timeout — if GitHub is unreachable it silently skips.

### Update notification

When a newer version exists you'll see:

```
# Direct install
databricks-codex: update available (v0.8.0). Run: databricks-codex update

# Homebrew install
databricks-codex: update available (v0.8.0). Run: brew upgrade databricks-codex
```

### `update` subcommand

```bash
databricks-codex update
```

Force-checks GitHub for the latest release (bypasses the 24-hour cache) and prints upgrade instructions:

| Install method | Output |
|---|---|
| Already latest | `databricks-codex v0.7.1 is already the latest version` |
| Direct install | `Update available: v0.8.0. Download from: https://github.com/...` |
| Homebrew | `Update available: v0.8.0. Run: brew upgrade databricks-codex` |

No binary is replaced — the command prints instructions only. In-place self-update is planned for a future release.

### Opt out

```bash
# Per-invocation flag
databricks-codex --no-update-check

# Per-session or permanent (add to shell profile)
export DATABRICKS_NO_UPDATE_CHECK=1
```

Both suppress the startup check and disable the `update` subcommand.

## License

MIT

