# Brew Board

Brew Board is a keyboard-first terminal interface for routine Homebrew maintenance. It shows what is installed or outdated, lets you stage a maintenance session, reveals the exact commands it plans to run, and ends with a receipt. Nothing changes on your system until you review the plan and press `y`.

## Quick start

Requires macOS with Homebrew 4 or newer.

**From a GitHub release** (Apple Silicon `arm64`, Intel `amd64`):

```sh
VERSION=1.0.0 ARCH=arm64      # pick the release; use amd64 on Intel
BASE=https://github.com/kaanemec/brew-board/releases/download/v$VERSION
curl -LO $BASE/brewboard_${VERSION}_darwin_${ARCH}.tar.gz
curl -LO $BASE/checksums.txt
grep "darwin_${ARCH}" checksums.txt | shasum -a 256 -c -    # must print: ...tar.gz: OK
tar -xzf brewboard_${VERSION}_darwin_${ARCH}.tar.gz
./brewboard --version
```

Move `brewboard` somewhere on your `PATH` (for example `/usr/local/bin`). The binary is not code-signed; if macOS blocks a browser-downloaded copy, check the checksum first, then clear the quarantine flag with `xattr -d com.apple.quarantine brewboard`.

**With Go 1.27 or newer:**

```sh
go install github.com/kaanemec/brew-board/cmd/brewboard@latest
```

**From source:**

```sh
make build                  # builds bin/brewboard
bin/brewboard               # run it
go run ./cmd/brewboard      # or run from source
make check                  # gofmt check, go vet, golangci-lint, go test -race
```

`make check` needs `golangci-lint` on your PATH. Run `brewboard --version` to print the version and exit.

## Keyboard reference

| Key | Action |
| --- | --- |
| `j` / `k`, `↓` / `↑` | Move selection |
| `g` / `G`, `home` / `end` | First / last package |
| `pgdn` / `pgup`, `ctrl+d` / `ctrl+u` | Page down / up |
| `/` | Search name and description (`enter` keeps the search, `esc` clears it) |
| `esc` | Clear the search (list view) |
| `f` | Cycle type filter: all, formulae, casks |
| `o` | Toggle outdated-only |
| `r` | Refresh the inventory |
| `enter` | Open package details |
| `esc`, `q`, `backspace`, `left`, `h` | Leave details |
| `space` | Select or unselect the highlighted outdated, unpinned package |
| `a` | Select or unselect all visible outdated, unpinned packages |
| `u` | Re-check the selection and open the review |
| `y` | Review: run the listed commands |
| `esc` | Review: back to the list, selections kept |
| `j` / `k`, `pgdn` / `pgup` | Review, receipt, details and help: scroll |
| `x` / `ctrl+c` | Running: ask to cancel the run (`y` interrupts the running command and skips the remaining ones, `n` or `esc` keeps it running) |
| `s` | Receipt: save it to your home directory (the saved path or error shows under the footer) |
| `c` | Receipt: review `brew cleanup` |
| `esc` / `enter` | Receipt: back to the list, selections cleared |
| `?` | Toggle help |
| `q` / `ctrl+c` | Quit (`ctrl+c` works everywhere except while commands run, where it asks to cancel) |

List markers: `>` cursor row, `↑` outdated, `pin` pinned, `[x]` selected (`*` on narrow terminals). The status bar starts with `[ready]`, `[loading…]`, `[stale, refreshing…]`, `[checking selections…]`, `[running]` or `[error]`, and shows how many packages are selected. After a failed or running refresh, a banner above the list shows when the displayed inventory was loaded.

### Maintenance session

Stage outdated packages with `space` or `a`, then press `u`. Brew Board reloads the inventory, drops selections that changed and says why, then shows the review. Nothing runs until you press `y`. Upgrades run one per package, in order, stopping at the first failure, with live output. Afterwards the inventory is reloaded and a receipt shows what changed; `s` saves it as `brewboard-receipt-<timestamp>-<id>.txt` (mode 0600) in your home directory. `c` offers `brew cleanup`, which gets its own review and `y`.

## Plan and confirmation example

After selecting `ripgrep` (formula) and `iterm2` (cask) and pressing `u`:

```text
Review the plan

Brew Board will run 2 commands, in order, stopping at the first failure:

 1. brew upgrade --formula ripgrep
    Upgrade formula ripgrep from 14.1.0 to 14.1.1. Homebrew may also upgrade dependencies.

 2. brew upgrade --cask iterm2
    Upgrade cask iterm2 from 3.5.0 to 3.5.1.

Homebrew may also upgrade dependencies or do related work of its own.
Nothing runs until you press y.

y run these 2 commands · esc back
```

If a package changed since you selected it (for example it is already up to date), a "Changed since you selected:" list appears above the commands and that item is dropped.

## Receipt example

Here `iterm2` failed, so the remaining command did not run:

```text
Brew Board maintenance receipt
Created:  2026-10-05 14:32:07 BST
Homebrew: Homebrew 7.0.7

1. brew upgrade --formula ripgrep: upgraded, exit 0, 14.1.0 -> 14.1.1
2. brew upgrade --cask iterm2: failed, exit 1, 3.5.0 (unchanged)
3. brew upgrade --formula wget: not run, 1.24.5 (unchanged)

Summary: 1 upgraded, 1 failed, 0 cancelled, 1 not run, 0 uncertain
A command failed. Re-run the command in a terminal to see Homebrew's full message.
```

If the inventory cannot be reloaded after the run, the receipt adds `NOT VERIFIED: the post-run inventory refresh failed (...); versions below are from before the run`, upgrades that exited 0 are reported as `uncertain`, and each version reads `<version> before the run (after not verified)`.

## Troubleshooting

| What you see | Cause | What to do |
| --- | --- | --- |
| `[error] Homebrew not found` | `brew` is not on `PATH` or in `/opt/homebrew`, `/usr/local` or `/home/linuxbrew/.linuxbrew` | Install Homebrew from https://brew.sh or add it to `PATH`, then press `r`. |
| `[error] Unsupported Homebrew version` | `brew` rejected `--json=v2` | Run `brew update` to get Homebrew 4.0 or newer, then press `r`. |
| `[error] Homebrew timed out` | A `brew` command took over 60 seconds, often because another brew process or an update is running | Wait for the other process to finish, then press `r`. |
| `[error] brew command failed` with `exited with status N: <first stderr line>` | Homebrew itself failed | Run the same `brew` command in a terminal for the full message, fix it (try `brew doctor`), then press `r`. |
| `[error] Unreadable Homebrew output` | The JSON could not be parsed | Press `r`; if it persists, run `brew doctor`. |
| Receipt shows `NOT VERIFIED` and `uncertain` results | The refresh after the run failed | Press `r` on the receipt to retry; check the packages with `brew info <name>`. |
| Receipt shows `failed, exit N` | A reviewed command failed; later commands were not run | Re-run that command in a terminal, fix the cause, stage the remaining packages again. |
| A cask has a newer version on its website or in its own updater, but no `↑` marker | `brew outdated` skips self-updating casks unless `--greedy` is given, and Brew Board does not pass it | Use the app's own updater, or run `brew upgrade --cask --greedy <name>` yourself. |
| Banner stays at `[stale, refreshing…]` | A refresh is still waiting on `brew` (each of the two read commands may take up to 60 seconds) | Wait about two minutes; it then becomes `[error]` with a reason. Quit other `brew` processes and press `r`. |

## Limitations

- macOS only. Needs Homebrew 4 or newer with `--json=v2` support. Tested with Homebrew 7.0.x on Apple Silicon (prefix `/opt/homebrew`); Intel Macs are untested. Linuxbrew is untested and unsupported.
- Brew Board looks for `brew` on `PATH`, then at `/opt/homebrew/bin/brew`, `/usr/local/bin/brew` and `/home/linuxbrew/.linuxbrew/bin/brew`.
- To read the inventory it runs only `brew --version`, `brew info --json=v2 --installed` and `brew outdated --json=v2`. A confirmed session additionally runs exactly the reviewed commands. Every call sets `HOMEBREW_NO_AUTO_UPDATE=1`, so no hidden `brew update` happens.
- Self-updating casks are not reported as outdated (no `--greedy`).
- Pinned packages cannot be selected. The list shows the latest installed version; the details view lists all installed versions.
- Upgrades are per package; Homebrew may upgrade dependencies as part of one. Brew Board does not resolve dependencies itself.
- No noninteractive mode, no `brew update`, no installs or uninstalls, and no automatic cleanup.
- Release binaries are not code-signed or notarized.

## Design notes

Brew Board is a transparent maintenance session, not a package store. Homebrew remains the source of truth: Brew Board has no package database of its own and runs no package-changing command at startup. Commands are executed as argv, never through a shell. See [ARCHITECTURE.md](ARCHITECTURE.md) for the technical shape and [the roadmap](roadmap/README.md) for version goals and jobs.
