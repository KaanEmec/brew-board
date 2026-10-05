# Job: discover Homebrew and load inventory

## Objective
Read the user's Homebrew installation reliably without altering packages.

## Tasks
- Locate the active `brew` executable and identify its version and available structured output commands.
- Read installed formulae, installed casks, and outdated items from Homebrew.
- Normalize package identity, type, installed version, available version, and relevant metadata into small Go types.
- Keep Homebrew command calls behind an adapter with fixture-based parser tests.

## Expected outcome
A read-only adapter produces a consistent inventory on a supported macOS Homebrew installation and reports unsupported output clearly.
