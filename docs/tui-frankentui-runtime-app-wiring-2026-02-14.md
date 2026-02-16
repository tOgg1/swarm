# FrankenTUI Runtime App Wiring (2026-02-14)

## What changed

- `crates/forge-tui/src/frankentui_bootstrap.rs` now mounts real `App` state instead of static bootstrap text rows.
- Interactive runtime loop now:
  - translates input events to `InputEvent`
  - calls `App::update(...)`
  - executes returned `Command` for `Fetch`/`Quit`/`RunAction` paths
  - refreshes live data on tick/input fetch requests
- Refresh now loads and applies:
  - loop rows (`LoopView`)
  - run history per loop (`RunView`)
  - selected/multi log tails (`LogTailView`)
- Renderer bridge now prints `App::render()` output into FrankenTUI frame each tick/event.
- Runtime action execution now calls real `forge-cli` backends:
  - `resume`, `stop`, `kill`, `rm`, `up`
  - success/error messages mapped back through `ActionResult` into app status handling
- Screen mode is now hard-forced to `ScreenMode::AltScreen` in FrankenTUI runtime host.

## Smoke gate update

- `scripts/rust-frankentui-bootstrap-smoke.sh` marker check now validates pane shell markers:
  - `Forge Loops`
  - `1:Overview`
  - `5:Inbox`

## Validation

- `cargo fmt --all`
- `cargo clippy -p forge-tui --all-targets -- -D warnings`
- `cargo test -p forge-tui --lib frankentui_bootstrap::tests:: -- --nocapture`
- `cargo test -p forge-tui --bin forge-tui -- --nocapture`
- `scripts/rust-frankentui-bootstrap-smoke.sh`
