<!-- Parent: ../AGENTS.md -->
<!-- Generated: 2026-04-06 | Updated: 2026-04-06 -->

# tomlconfig

## Purpose
String-based surgical TOML manipulation for `~/.codex/config.toml` + the profile-v2 sibling file (`~/.codex/databricks-proxy.config.toml`). Avoids any TOML parser dependency by operating on raw lines. **Write-once model**: `Patch` is idempotent and final — there is no Backup/Restore round-trip. The "surgical" approach means only managed keys and sections are modified — all other user content is preserved byte-for-byte. Atomic writes via temp + rename. Multi-session proxy URL handoff via `UpdateProxyURL`.

## Key Files

| File | Description |
|------|-------------|
| `tomlconfig.go` | `Manager` struct with `Patch` (write-once base + sibling), `UpdateProxyURL` (proxy handoff), and pure string helpers |
| `tomlconfig_test.go` | Tests for the v2 layout: provider section, root model_provider/model override, sibling file, supports_websockets opt-out, legacy migration, OTEL section, proxy URL handoff |

## For AI Agents

### Working In This Directory
- **No external dependencies** — this package must stay zero-dependency; use only stdlib
- **Write-once model** — no `Backup`, no `Restore`, no `RestoreFromBackup`. Patch is final. Restore-on-exit was unreliable (os.Exit skips defers, panics leave stale state) and is intentionally removed. Do not add it back.
- Managed root keys: `model_provider`, `model`. Managed sections in base: `model_providers.databricks-proxy`, `otel`. Profile-v2 sibling file is fully owned by us.
- Legacy v1 layout (`profile = "databricks-proxy"` root key, `[profiles.databricks-proxy]` section) is **stripped one-way** on Patch when present, migrating any model value forward into the sibling file. Stripping is irreversible — that's the whole point of moving to profile-v2.
- Root `profile = "..."` keys belonging to OTHER profile names (e.g. user's `profile = "myprofile"`) are left alone — Codex profile-v2 rejects them, but that's a pre-existing user condition, not our concern.

### Testing Requirements
- Run with `go test ./pkg/tomlconfig/... -v`
- Tests use in-memory TOML strings and temp files — no mocking needed
- Cover: v2 layout assertions (root keys, provider section, sibling file, supports_websockets), legacy migration (strip + carry forward), surgical preservation (non-managed keys untouched), OTEL section round-trip, proxy URL handoff

### Common Patterns
- `atomicWrite`: write to `.config-*.tmp`, chmod 0600, then `os.Rename` into place
- Section boundary detection: next line starting with `[` (but not `[[`) ends the current section
- Pure string helpers (`setRootKey`, `stripRootKey`, `setSection`, `stripSection`) take no Manager state — easier to reason about, easier to test

## Dependencies

### Internal
- None (standalone package)

### External
- None (stdlib only: `fmt`, `log`, `os`, `path/filepath`, `strings`)

<!-- MANUAL: Any manually added notes below this line are preserved on regeneration -->
