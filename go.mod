module github.com/IceRhymers/databricks-codex

go 1.22

require github.com/IceRhymers/databricks-claude v0.3.0

// TODO: remove replace directive and update to v0.3.0 after databricks-claude#21 is merged and tagged
replace github.com/IceRhymers/databricks-claude v0.2.0 => /tmp/dc-auth/databricks-claude
