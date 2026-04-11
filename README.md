# databricks-codex

> **Disclaimer:** This is an unofficial, community-built workaround to enable Databricks OAuth SSO authentication with this AI coding tool. It is not supported, endorsed, or recognized by Databricks. Use at your own risk.


Transparent wrapper for the OpenAI Codex CLI that runs a local proxy backed by Databricks OAuth — so you never manually paste or refresh a token again.

## The Problem

Databricks AI Gateway uses short-lived OAuth tokens. Without this tool, you'd need to manually refresh a token, point Codex at the right Databricks endpoint, and keep that token fresh for the duration of a session.

## How It Works

`databricks-codex` wraps the `codex` binary. It:

1. Fetches a fresh Databricks OAuth token via `databricks auth token`
2. Discovers your workspace host from `databricks auth env`
3. Resolves your workspace ID via the SCIM API
4. Constructs the Databricks AI Gateway URL
5. Binds a local proxy on `127.0.0.1:49154` (fixed port — shared across concurrent sessions) that forwards traffic upstream and refreshes the Databricks token automatically
6. Writes `~/.codex/config.toml` once to point at the proxy (idempotent — no restore on exit)
7. Exec's `codex` with your args — fully transparent

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
databricks-codex --upstream https://1234567890123456.ai-gateway.cloud.databricks.com/openai/v1 "summarize this PR"
```

## Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--verbose`, `-v` | `false` | Enable debug logging to stderr |
| `--log-file` | | Write debug logs to a file (combinable with `--verbose`) |
| `--print-env` | | Print resolved configuration (token redacted) and exit |
| `--otel` | `true` | Enable OpenTelemetry logs export |
| `--no-otel` | | Disable OpenTelemetry for this session |
| `--otel-logs-table` | `main.codex_telemetry.codex_otel_logs` | Unity Catalog table for OpenTelemetry logs |
| `--profile` | saved/`DEFAULT` | Databricks CLI profile (saved to state file; `--profile` flag writes it once) |
| `--model` | `databricks-gpt-5-4` | Model to use (saved for future sessions) |
| `--port` | `49154` | Proxy listen port (saved for future sessions) |
| `--upstream` | auto-discovered | Override the upstream inference URL the local proxy forwards to |
| `--proxy-api-key` | disabled | Require this API key on all local proxy requests |
| `--tls-cert` | | TLS certificate file for the local proxy (requires `--tls-key`) |
| `--tls-key` | | TLS private key file for the local proxy (requires `--tls-cert`) |
| `--headless` | `false` | Start proxy without launching codex (for IDE extensions or hooks) |
| `--idle-timeout` | `30m` | Idle timeout for headless mode (`0` disables; bare number = minutes) |
| `--install-hooks` | | Install SessionStart hook into `~/.codex/hooks.json` |
| `--uninstall-hooks` | | Remove databricks-codex hooks from `~/.codex/hooks.json` |
| `--version` | | Print version and exit |
| `--help`, `-h` | | Print wrapper flags and the full `codex --help` output, then exit |

All other flags and args are forwarded to `codex`.

## Auto-Discovery

On startup, `databricks-codex` auto-discovers:

- Your workspace host from `databricks auth env`
- Your workspace ID via the SCIM API (`x-databricks-org-id` header)
- Constructs the AI Gateway URL: `https://<workspaceId>.ai-gateway.cloud.databricks.com/openai/v1`

If workspace ID resolution fails, it falls back to `<host>/serving-endpoints/codex/openai/v1`.

## Debugging

### Verify your resolved configuration

Run `--print-env` to print the resolved profile, Databricks host, upstream base URL, redacted token placeholder, OpenTelemetry logs table, and detected Codex binary path, then exit without launching Codex.

```bash
databricks-codex --print-env
```

Example output:

```
databricks-codex configuration:
  Profile:           DEFAULT
  DATABRICKS_HOST:   https://adb-1234567890123456.7.azuredatabricks.net
  OPENAI_BASE_URL:   https://1234567890123456.ai-gateway.cloud.databricks.com/openai/v1
  Auth Token:        dapi-***
  OpenTelemetry Logs Table:   main.codex_telemetry.codex_otel_logs
  Codex binary:      /usr/local/bin/codex
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

### View full usage

`databricks-codex --help` (or `-h`) prints the wrapper's own flags followed by the complete `codex --help` output.

## Session Hooks (automatic proxy lifecycle)

Install hooks so every Codex session auto-starts the proxy on startup — no manual `--headless` needed.

> **First-time setup:** Run `databricks-codex` at least once before installing hooks. This writes `~/.codex/config.toml` so the proxy is used for all Codex sessions.

### Install

```bash
databricks-codex --install-hooks
```

This merges a **SessionStart** hook into `~/.codex/hooks.json` and enables the `codex_hooks` feature flag in `~/.codex/config.toml`:

- **SessionStart** (`startup`): runs `databricks-codex --headless-ensure` — starts the proxy if it isn't already running.

### Shutdown

Unlike Claude Code, the Codex CLI does not have a `SessionEnd` hook event. The proxy shuts itself down automatically after **30 minutes of inactivity** (configurable via `--idle-timeout`). You can also stop it manually with `POST /shutdown` or by sending a signal to the process.

### Uninstall

```bash
databricks-codex --uninstall-hooks
```

Removes only the databricks-codex hook entries. Other hooks in your `hooks.json` are untouched.

### Notes

- Safe to rerun `--install-hooks` after upgrades — existing hooks are replaced, not duplicated.
- Custom port settings persist automatically via the state file (`~/.codex/.databricks-codex.json`).

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

