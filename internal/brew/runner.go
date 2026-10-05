package brew

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// brewEnv is appended to the inherited environment for every brew call.
// HOMEBREW_NO_AUTO_UPDATE stops read commands such as `brew outdated` from
// silently running `brew update` first; the others keep output plain.
var brewEnv = []string{
	"HOMEBREW_NO_AUTO_UPDATE=1",
	"HOMEBREW_NO_ENV_HINTS=1",
	"HOMEBREW_NO_COLOR=1",
	"HOMEBREW_NO_EMOJI=1",
}

// Runner executes a brew binary with an argv slice. It exists so the client can
// be tested without a live Homebrew installation.
type Runner interface {
	Run(ctx context.Context, path string, args ...string) (stdout, stderr []byte, err error)
}

// execRunner runs commands with os/exec. Arguments are always passed as argv,
// never through a shell.
type execRunner struct{}

// Run executes path with args. A non-zero exit becomes a *CommandError; a
// cancelled or expired context is returned as the context's error, unwrapped.
func (execRunner) Run(ctx context.Context, path string, args ...string) (stdout, stderr []byte, err error) {
	var outBuf, errBuf bytes.Buffer
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	cmd.Env = append(os.Environ(), brewEnv...)

	runErr := cmd.Run()
	if runErr == nil {
		return outBuf.Bytes(), errBuf.Bytes(), nil
	}
	// Prefer the context error so callers can use errors.Is(err, context.Canceled).
	if ctxErr := ctx.Err(); ctxErr != nil {
		return outBuf.Bytes(), errBuf.Bytes(), ctxErr
	}
	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		return outBuf.Bytes(), errBuf.Bytes(), &CommandError{
			Args:     append([]string(nil), args...),
			ExitCode: exitErr.ExitCode(),
			Stderr:   strings.TrimSpace(errBuf.String()),
		}
	}
	return outBuf.Bytes(), errBuf.Bytes(), fmt.Errorf("run %s: %w", path, runErr)
}
