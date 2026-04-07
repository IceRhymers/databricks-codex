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

## Development

```bash
git clone https://github.com/IceRhymers/databricks-codex
cd databricks-codex
make test
make build
```

## License

MIT
