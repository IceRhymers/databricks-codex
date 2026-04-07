<!-- Parent: ../AGENTS.md -->
<!-- Generated: 2026-04-06 | Updated: 2026-04-06 -->

# pkg

## Purpose
Container for local packages. Only `tomlconfig` lives here — all other shared packages (`proxy`, `tokencache`, `childproc`, `filelock`, `registry`, `authcheck`) come from the sister project `github.com/IceRhymers/databricks-claude` and are not duplicated here.

## Subdirectories

| Directory | Purpose |
|-----------|---------|
| `tomlconfig/` | String-based TOML manipulation for patching `~/.codex/config.toml` (see `tomlconfig/AGENTS.md`) |

## For AI Agents

### Working In This Directory
- Do not add new packages here unless the logic is truly specific to databricks-codex and unsuitable for databricks-claude
- New shared utilities should go in databricks-claude's `pkg/` and be imported from there

<!-- MANUAL: Any manually added notes below this line are preserved on regeneration -->
