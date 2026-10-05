# Job: execute approved operations

## Objective
Run only the reviewed Homebrew operations and show what is happening.

## Tasks
- Pass executable and arguments directly to the OS process API, without constructing a shell command string.
- Execute plan items in order and stream output into a readable activity view.
- Record start/end time, command, exit status, and output for each item.
- Stop the plan on a failed operation and preserve its partial result for the receipt.

## Expected outcome
Only confirmed commands run, their progress is visible, and failures leave a truthful record of completed versus pending work.
