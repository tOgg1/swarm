# CLI Deep Review: job/trigger/mesh/node/registry/team/task/delegation (2026-02-14)

## Scope
Commits reviewed:
- `463e73c` feat(cli): add job + trigger
- `c4742d0` feat(cli): add mesh profile/status commands
- `69b0416` feat(cli): add remote node + registry
- `d632972` feat(cli): add team/task/delegation
- `6a9f330` feat(cli): root dispatch/completions wiring
- `59fb4e7` fix(cli): expect/unwrap panic message sweep

Validation run:
- `cargo test -p forge-cli --lib job::tests:: -- --nocapture`
- `cargo test -p forge-cli --lib trigger::tests:: -- --nocapture`
- `cargo test -p forge-cli --lib mesh::tests:: -- --nocapture`
- `cargo test -p forge-cli --lib node::tests:: -- --nocapture`
- `cargo test -p forge-cli --lib registry::tests:: -- --nocapture`
- `cargo test -p forge-cli --lib team::tests:: -- --nocapture`
- `cargo test -p forge-cli --lib task::tests:: -- --nocapture`
- `cargo test -p forge-cli --lib delegation::tests:: -- --nocapture`
- `cargo check -p forge-cli`

## Findings (severity-ordered)

1. **High**: `node exec` can silently rewrite user command payload after `--`.
- File: `crates/forge-cli/src/node.rs:383`
- File: `crates/forge-cli/src/node.rs:430`
- Cause: parser strips `--json`/`--jsonl`/`--quiet` from all tokens before command reconstruction, including tokens intended as part of remote command after `--`.
- Impact: `forge node exec <id> -- cmd --json` becomes `cmd`; command semantics changed.
- Fix: stop global flag stripping once `--` is seen for `exec`; preserve tail tokens verbatim.

2. **Medium**: `task` command parser strips literal argument values equal to global flag tokens.
- File: `crates/forge-cli/src/task.rs:553`
- File: `crates/forge-cli/src/task.rs:564`
- Cause: parser removes `--json`/`--jsonl`/`--quiet` everywhere, not only in global flag position.
- Impact: impossible to pass literal values that equal those tokens (for example title/body/tag as `--json`).
- Fix: parse flags positionally (front-loaded), or stop flag collection after first subcommand token.

3. **Medium**: `team` command parser has same token-stripping issue as `task`.
- File: `crates/forge-cli/src/team.rs:570`
- File: `crates/forge-cli/src/team.rs:581`
- Impact: values matching `--json`/`--jsonl`/`--quiet` cannot be passed as user data.
- Fix: same strategy as `task`.

4. **Medium**: `task retry --actor` is parsed but ignored.
- File: `crates/forge-cli/src/task.rs:842`
- File: `crates/forge-cli/src/task.rs:449`
- Cause: actor is accepted in parse path; execution path binds `_actor` and drops it.
- Impact: operator expectation mismatch; missing audit attribution for retry action.
- Fix: plumb actor to retry event/audit path or remove flag from interface until supported.

5. **Low**: `registry ls` accepts multiple scope tokens; last one silently wins.
- File: `crates/forge-cli/src/registry.rs:741`
- File: `crates/forge-cli/src/registry.rs:752`
- Impact: ambiguous user input not rejected (`registry ls agents prompts`).
- Fix: enforce at most one scope positional.

6. **Low**: `--json`/`--jsonl` mutual exclusivity inconsistent across command families.
- Enforced in: `node`, `task`, `team`.
- Not enforced in: `job`, `trigger`, `mesh`, `registry`, `delegation`.
- Impact: inconsistent UX; scripts may observe divergent behavior.
- Fix: centralize conflict check in root/global-flag handling or standardize all command parsers.

## Test Gaps
- Missing regression test: `node exec` preserves all tokens after `--`.
- Missing regression tests: `task` and `team` literal payload/token values equal to `--json` are preserved.
- Missing behavior test: `task retry --actor` persists actor semantics (or explicit rejection if unsupported).

## Summary
Core feature set landed and current module tests pass. Main risk area is CLI argument parsing consistency and command payload preservation (highest risk in `node exec`).
