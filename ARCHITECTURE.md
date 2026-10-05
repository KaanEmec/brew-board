# Brew Board architecture

Keyboard-first Go TUI for routine Homebrew maintenance. Homebrew stays the source of truth; Brew Board only reads it until the user reviews and confirms a plan. See [README](README.md) for the product contract and [roadmap](roadmap/README.md) for jobs.

## System

```
brew executable ──(exec, argv only)──▶ internal/brew  ──Inventory──▶ internal/tui ──▶ terminal
                                        adapter+parsers              Bubble Tea model/views
                                                                            │
                                      internal/plan ◀── selections ─────────┘   (v0.2)
                                      internal/run  ── streams output ──▶ tui    (v0.2)
```

| Package | Responsibility | Depends on |
|---|---|---|
| `cmd/brewboard` | Entry point: flags, discover brew, start the TUI program. | brew, tui |
| `internal/brew` | Locate `brew`, run `brew info --json=v2 --installed` and `brew outdated --json=v2`, normalize into `Package`/`Inventory`, classify errors. Read-only. | stdlib only |
| `internal/tui` | Bubble Tea model: list, search, filters, details, status bar, loading/stale/error states, refresh, and the maintenance session (stage, review, execute, receipt). Reads brew through the `Loader` interface and runs confirmed plans through its `Executor` interface. | brew, plan, run, receipt, bubbletea, bubbles, lipgloss |
| `internal/plan` (v0.2) | Typed, ordered plan items built from selections; staleness check against a fresh inventory. | brew |
| `internal/run` (v0.2) | Executes confirmed plan items via `os/exec` with argv slices, streams output, records a per-item result. | plan |
| `internal/receipt` (v0.2) | Reconciles results against a fresh inventory and renders/saves the plain-text receipt. | plan, run, brew |

## Key decisions

- **Structured output only.** Inventory comes from `--json=v2` (Homebrew ≥ 4). Plain-text `brew list`/`brew outdated` are never parsed. If JSON is unavailable the adapter returns `ErrUnsupported` rather than guessing.
- **Two commands, one inventory.** `info --installed` gives identity, versions, metadata; `outdated` gives authoritative outdated/pinned state. The adapter merges them by name/token.
- **Fully qualified names.** `Package.Name` is the formula `full_name` / cask token Homebrew accepts on the command line (e.g. `hashicorp/tap/terraform`). `brew outdated` reports full names for formulae but short tokens for casks, so the merge falls back to the short token for casks.
- **Hidden follow-on work disabled.** Every brew call runs with the same environment (`brewEnv` in `internal/brew`, `run.DefaultEnv`; keep them identical), so only reviewed commands change the installation:
  - auto-update: `HOMEBREW_NO_AUTO_UPDATE=1`, otherwise `brew outdated` and `brew upgrade` silently run `brew update` first;
  - autoremove: `HOMEBREW_NO_AUTOREMOVE=1`, otherwise `brew uninstall` and `brew cleanup` remove dependency-only formulae themselves (removing A and its dependency-only B in one plan would autoremove B after A, then `brew uninstall B` fails and the plan stops);
  - install-cleanup: `HOMEBREW_NO_INSTALL_CLEANUP=1`, otherwise `brew upgrade` cleans up the upgraded formula's old versions;
  - no `--force` or `--ignore-dependencies` on any command.
  Hints, color and emoji are off too.
- **One timeout layer.** The adapter applies a 60s per-command timeout itself; callers only cancel. The TUI cancels the in-flight load on quit.
- **One command per plan item.** Upgrades run as `brew upgrade --formula <name>` or `brew upgrade --cask <name>`, removals as `brew uninstall --formula|--cask <name>`, one process per package, in order, stopping at the first failure. Cleanup is a separate reviewed item (`brew cleanup`). Cancellation sends SIGINT and waits before killing.
- **Dependency rules for removal.** A package is marked for upgrade or removal, never both (the TUI keeps the marks exclusive; `plan.Build` also rejects it with `ErrConflict`). Edges follow Homebrew: a formula needs every formula in its current keg's full runtime closure (`RuntimeDependencies`; the linked keg, else the newest), a cask needs its `depends_on` formulae and casks, and names are matched with their kind (`plan.Dependents`); direct `Dependencies` are for display and the fallback when brew reports no closure. `Plan.Validate` blocks a removal while an installed dependent is not removed in the same plan (exactly when brew would refuse), and the TUI re-validates after each drop; `Build` orders dependents before their dependencies, removals before upgrades, and rejects removing a pinned package (`ErrPinned`). No `--force` or `--ignore-dependencies`, ever. `brew autoremove` is never implied by an uninstall: after any run the receipt offers it as its own reviewed plan when `plan.Orphans` on the post-run inventory (with an empty plan) lists dependency-only formulae nothing installed needs; Homebrew decides the final list.
- **No shell.** Every call is `exec.CommandContext(path, args...)`. Package names are passed as separate argv entries, never interpolated.
- **Injectable runner.** `brew.Client` runs commands through a small `Runner` interface so parsers and error paths are tested against fixtures in `internal/brew/testdata`, never a live brew.
- **Execution events.** The TUI starts `Executor.Execute` in a `tea.Cmd` with a buffered (256) event channel and reads it one event per `tea.Cmd` until `Finished`; it closes the channel once `Execute` returns. Quitting is disabled while a plan runs, so the channel is always drained.
- **Non-blocking I/O in the TUI.** Loading and refreshing are `tea.Cmd`s; the last inventory stays on screen and is labelled stale until the new one arrives. One refresh in flight at a time.
- **Render cost independent of inventory size.** Per-package search text and cell widths are derived once per load; the visible list is rebuilt only when the inventory, a filter, or the search changes, never on navigation or render. `View` truncates every line to the terminal width by cell width.
- **Honest states.** Loading, stale, empty, error, and unsupported are distinct view states; a failed refresh never replaces a good inventory.
- **Bubble Tea v1 API** (`bubbletea v1.3`, `bubbles v1`, `lipgloss v1`). Colors degrade through lipgloss when the terminal has no color.
- **Module path** `github.com/kaanemec/brew-board`; binary `brewboard`.
- **Tooling.** `gofmt`, `go vet`, `golangci-lint` (config in `.golangci.yml`), table-driven tests with the standard library.

## Roadmap status

| Version | Epic | Status |
|---|---|---|
| v0.1 | Homebrew adapter | done |
| v0.1 | TUI inspection | done |
| v0.1 | Validation | done (fixtures, live macOS run, review fixes, README) |
| v0.2 | Stage and preview / execution / receipt | done (unit-tested with a fake brew; real upgrade run still to be exercised by the owner) |
| v1.0 | Polish / release | in progress (CI, goreleaser, docs; licence and first tagged release pending) |

Update this file when a decision above changes.
