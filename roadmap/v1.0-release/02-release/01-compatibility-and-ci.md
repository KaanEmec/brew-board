# Job: verify supported Homebrew behavior

## Objective
Avoid a 1.0 release that works only with one local Homebrew snapshot.

## Tasks
- Test adapter parsing against representative supported Homebrew outputs and a current macOS installation.
- Add CI for Go tests, formatting, static checks, and macOS builds.
- Verify failure paths such as missing `brew`, command refusal, partial updates, and interrupted sessions.
- State the supported macOS/Homebrew range; leave Linuxbrew unclaimed until separately tested.

## Expected outcome
The support statement matches tested behavior and common failure paths have understandable results.
