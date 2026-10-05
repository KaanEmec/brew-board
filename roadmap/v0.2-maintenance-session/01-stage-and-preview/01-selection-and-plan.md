# Job: stage a maintenance plan

## Objective
Let users deliberately select the work they want Homebrew to perform.

## Tasks
- Add selection of one or more outdated formulae/casks for update.
- Represent proposed operations as ordered, typed plan items with package identities.
- Refresh relevant inventory before finalizing a plan and flag stale selections.
- Keep removal and cleanup out of the initial update flow until separately specified.

## Expected outcome
The user can stage a precise update plan and see which selections are still valid before any command runs.
