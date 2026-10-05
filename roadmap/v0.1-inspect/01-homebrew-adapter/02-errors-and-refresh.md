# Job: handle errors and refresh

## Objective
Keep the interface understandable when Homebrew is absent, busy, outdated, or returns incomplete data.

## Tasks
- Distinguish missing Homebrew, command failure, timeout/cancellation, and malformed output.
- Run inventory calls without blocking keyboard input; allow a user-requested refresh.
- Preserve the last known view while refresh runs and label it as potentially stale.
- Avoid automatic commands that modify or clean the installation.

## Expected outcome
The TUI can show loading, stale, empty, and error states without freezing or implying that a failed refresh succeeded.
