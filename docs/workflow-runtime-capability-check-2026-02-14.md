# Workflow runtime capability check (2026-02-14)

## Scope

Verify whether workflow runtime can execute `agent` / `loop` / `logic` steps and `when` conditions.

## Commands and outcomes

1. Validate and run existing `basic` workflow (`agent` + `bash`):

```bash
cargo run -q -p forge-cli --bin forge-cli -- workflow validate basic
cargo run -q -p forge-cli --bin forge-cli -- workflow run basic
cargo run -q -p forge-cli --bin forge-cli -- workflow logs wfr_810a6ab58bb3437f8ab480858c64f150
```

Observed:
- Validation succeeds.
- Run fails on step `plan` with:
  - `workflow run currently supports bash/human steps only; got step type "agent"`.

2. Probe `when` behavior with `_probe_when` workflow (`when = "false"` on first bash step):

```bash
cargo run -q -p forge-cli --bin forge-cli -- workflow validate _probe_when
cargo run -q -p forge-cli --bin forge-cli -- workflow run _probe_when
cargo run -q -p forge-cli --bin forge-cli -- workflow logs wfr_db4f0e3e4b874168bc93e307ad5bac89
```

Observed:
- Validation succeeds.
- Run succeeds.
- Step with `when = "false"` still executes (`stdout: SHOULD_NOT_RUN`).

3. Probe `logic` execution with `_probe_logic` workflow:

```bash
cargo run -q -p forge-cli --bin forge-cli -- workflow validate _probe_logic
cargo run -q -p forge-cli --bin forge-cli -- workflow run _probe_logic
cargo run -q -p forge-cli --bin forge-cli -- workflow logs wfr_954ecadfa8ee44178de42acef1fe8fe4
```

Observed:
- Validation succeeds.
- Run fails on `logic` step with:
  - `workflow run currently supports bash/human steps only; got step type "logic"`.

## Code pointers

- Run-time hard gate: `crates/forge-cli/src/workflow.rs:1055`
- Resume-time hard gate: `crates/forge-cli/src/workflow.rs:1850`
- `when` normalization exists but no runtime evaluation path: `crates/forge-cli/src/workflow.rs:2313`

## Conclusion

Current runtime supports execution of `bash` and `human` only.
`agent`, `loop`, `logic`, `job`, and `workflow` step types parse/validate but are not executable yet.
`when` is currently not enforced during execution.
