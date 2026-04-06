# databricks-codex

Transparent wrapper for the OpenAI Codex CLI that auto-injects Databricks OAuth tokens — so you never manually paste a token again.

## The Problem

Databricks AI Gateway uses short-lived OAuth tokens. The Codex CLI reads `OPENAI_BASE_URL` and `OPENAI_API_KEY` from the environment. Without this tool, you'd need to manually refresh and export a new token every hour.

## How It Works

`databricks-codex` wraps the `codex` binary. It:

1. Fetches a fresh Databricks OAuth token via `databricks auth token`
2. Discovers your workspace host from `databricks auth env`
3. Resolves your workspace ID via the SCIM API
4. Constructs the AI Gateway URL: `https://<host>/serving-endpoints/<workspaceId>.aws.proxy.codex/openai/v1`
5. Injects `OPENAI_BASE_URL` and `OPENAI_API_KEY` into the environment
6. Exec's `codex` with your args — fully transparent

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

# Override gateway URL:
databricks-codex --upstream https://my-host/serving-endpoints/codex/openai/v1 "summarize this PR"
```

## Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--verbose`, `-v` | `false` | Enable debug logging to stderr |
| `--log-file` | | Write debug logs to a file (combinable with `--verbose`) |
| `--print-env` | | Print resolved configuration (token redacted) and exit |
| `--no-otel` | | Disable OpenTelemetry for this session |
| `--otel-table` | `main.codex_telemetry.codex_otel_metrics` | UC table for OTEL metrics |
| `--upstream` | auto-discovered | Override the AI Gateway URL |
| `--version` | | Print version and exit |
| `--help`, `-h` | | Print wrapper flags and the full `codex --help` output, then exit |

All other flags and args are forwarded to `codex`.

## Auto-Discovery

On startup, `databricks-codex` auto-discovers:

- Your workspace host from `databricks auth env`
- Your workspace ID via the SCIM API (`x-databricks-org-id` header)
- Constructs the AI Gateway URL: `<host>/serving-endpoints/<workspaceId>.aws.proxy.codex/openai/v1`

If workspace ID resolution fails, it falls back to `<host>/serving-endpoints/codex/openai/v1`.

## Debugging

### Verify your auth setup

Run `--print-env` to see the resolved configuration without launching codex. The token is redacted so it's safe to share output for debugging.

```bash
databricks-codex --print-env
```

Example output:

```
databricks-codex configuration:
  DATABRICKS_HOST:   https://adb-1234567890123456.7.azuredatabricks.net
  OPENAI_BASE_URL:   https://adb-1234567890123456.7.azuredatabricks.net/serving-endpoints/1234567890123456.aws.proxy.codex/openai/v1
  OPENAI_API_KEY:    dapi-*** (redacted)
  Codex binary:      /usr/local/bin/codex
```

If the token shows as empty or the URL looks wrong, check your Databricks CLI with `databricks auth env`.

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
