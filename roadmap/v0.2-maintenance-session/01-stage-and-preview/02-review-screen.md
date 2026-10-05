# Job: show an honest review screen

## Objective
Make the proposed effects and exact Homebrew commands visible before confirmation.

## Tasks
- Show selected packages, planned command arguments, and a plain-language explanation of each step.
- Explain that Homebrew may update dependencies or perform its own related work.
- Require a clear confirmation action; cancellation returns to staging unchanged.
- Reject an empty, stale, or unsupported plan with an actionable message.

## Expected outcome
The user can review and cancel the whole session, and no package-changing process starts without confirmation.
