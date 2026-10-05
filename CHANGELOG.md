# Changelog

All notable changes to this project are documented here. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project intends to follow [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added

- Package removal: `d` marks an unpinned package for `brew uninstall --formula|--cask <name>`; removals are reviewed before upgrades and never use `--force` or `--ignore-dependencies`.
- Dependency rules: a package is removable only if everything installed that needs it is removed in the same plan; otherwise the review drops it and says why. Pinned formulae cannot be removed, and dependents are removed before dependencies.
- `brew autoremove` offer (`a` on the receipt) when formulae installed only as dependencies are needed by nothing installed, with its own review and confirmation.
- Colour theme with light and dark variants that degrades to 256, 16 or no colours (honours `NO_COLOR`); state is always also shown as text.
- `c` (continue) on the list opens the review; on the receipt `c` reviews `brew cleanup`.
- Every `brew` call now also sets `HOMEBREW_NO_AUTOREMOVE=1`, `HOMEBREW_NO_INSTALL_CLEANUP=1`, `HOMEBREW_NO_ENV_HINTS=1`, `HOMEBREW_NO_COLOR=1` and `HOMEBREW_NO_EMOJI=1` (alongside `HOMEBREW_NO_AUTO_UPDATE=1`).
- Homebrew tap: GoReleaser publishes `Formula/brewboard.rb` into this repository on each `v*` tag, including prereleases. Install with `brew tap kaanemec/brew-board https://github.com/KaanEmec/brew-board && brew install brewboard`.

### Changed

- README rewritten as a landing page with screenshots, install options, keyboard table and safety guarantees.

### v0.1: inspection

- Homebrew adapter: finds `brew`, reads `brew info --json=v2 --installed` and `brew outdated --json=v2`, merges them into one inventory, with a 60 second timeout per command and classified errors.
- TUI: package list with search, type filter, outdated-only view, details view, status bar, and distinct loading, stale, empty and error states.
- Read-only by design: `HOMEBREW_NO_AUTO_UPDATE=1` on every call, no shell, no package-changing commands.

### v0.2: maintenance session

- Stage outdated, unpinned packages with `space` or `a`; `c` (continue) re-checks them against a fresh inventory and opens the review.
- Review screen shows each exact command and a plain-language explanation; nothing runs until `y`.
- Execution runs one `brew upgrade --formula|--cask <name>` per package with live output, stops at the first failure, and can be cancelled with `x`.
- Plain-text receipt reconciled against a reloaded inventory (upgraded, failed, cancelled, not run, uncertain), saved with `s` in the home directory with mode 0600.
- Optional `brew cleanup` with its own review and confirmation.

### v1.0 prep: CI, release pipeline, polish

- GitHub Actions CI on macOS (build, gofmt, vet, lint, race tests).
- GoReleaser pipeline triggered by `v*` tags: macOS `arm64` and `amd64` tarballs plus `checksums.txt`; `brewboard --version` reports the tag.
- README: quick start with checksum verification, keyboard reference, plan and receipt examples, troubleshooting table, limitations.

## Before tagging 1.0

- [ ] Licence: to be chosen by the maintainer (no LICENSE file exists yet).
- [ ] Confirm the module path `github.com/kaanemec/brew-board` and that the GitHub repository matches it.
- [ ] Run the release workflow once from a tag and confirm the archives and `checksums.txt` are attached.
- [ ] On a clean machine or account, install from the release using the README steps, including `shasum -a 256 -c`, and run `brewboard --version`.
- [ ] Test on an Intel Mac, or state in the release notes that `amd64` is untested.
