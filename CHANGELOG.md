# Changelog

All notable changes to this project are documented here. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project intends to follow [Semantic Versioning](https://semver.org/).

## [Unreleased]

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
