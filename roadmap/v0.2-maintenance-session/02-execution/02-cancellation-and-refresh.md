# Job: cancellation and post-run refresh

## Objective
Handle interrupted work without suggesting the package state rolled back.

## Tasks
- Define and implement cancellation for a running Homebrew process, with a confirmation if interruption could leave partial work.
- Keep the TUI responsive while a command runs and prevent duplicate execution.
- Requery Homebrew after completion, failure, or cancellation.
- Reconcile observed installed versions with the planned operations and mark uncertain results.

## Expected outcome
The user sees the real post-run package state and can distinguish completed, failed, cancelled, and uncertain operations.
