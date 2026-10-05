// Package run executes a confirmed plan item by item with os/exec, streaming
// output to the caller and recording a truthful per-item result. Commands are
// always started from an argv slice, never through a shell.
package run

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/kaanemec/brew-board/internal/plan"
)

// MaxOutput is the most output kept per item; older output is dropped.
const MaxOutput = 256 << 10

// maxLine bounds a single streamed line so a stream without newlines
// (e.g. progress bars) still reaches the caller.
const maxLine = 16 << 10

// waitDelay is how long a cancelled brew gets to clean up after SIGINT
// before it is killed.
//
// It is a variable only so tests can shorten it.
var waitDelay = 10 * time.Second

// waitDelayNote is appended to Output when brew exited 0 but kept the output
// pipe open past waitDelay.
const waitDelayNote = "(output pipe closed after wait delay)"

// DefaultEnv is appended to os.Environ when Executor.Env is nil.
// It mirrors brewEnv in internal/brew/runner.go so upgrades run under the
// same conditions as inventory reads; consolidate the two later.
var DefaultEnv = []string{
	"HOMEBREW_NO_AUTO_UPDATE=1",
	"HOMEBREW_NO_ENV_HINTS=1",
	"HOMEBREW_NO_COLOR=1",
	"HOMEBREW_NO_EMOJI=1",
}

// Status is the state of one plan item.
type Status string

// Item statuses.
const (
	StatusPending   Status = "pending"
	StatusRunning   Status = "running"
	StatusCompleted Status = "completed"
	StatusFailed    Status = "failed"
	StatusCancelled Status = "cancelled"
)

// Result records what happened to one plan item.
type Result struct {
	Item   plan.Item
	Args   []string
	Status Status
	// Started and Ended are zero for items that never ran.
	Started, Ended time.Time
	// ExitCode is the process exit code, or -1 if it did not exit normally.
	ExitCode int
	// Output is merged stdout and stderr, capped at MaxOutput (tail kept).
	Output string
	Err    error
}

// CommandFailedError reports a brew command that exited non-zero.
type CommandFailedError struct {
	Args     []string
	ExitCode int
}

func (e *CommandFailedError) Error() string {
	return fmt.Sprintf("brew %s exited with status %d", strings.Join(e.Args, " "), e.ExitCode)
}

// Event is sent to the caller while a plan executes.
type Event interface{ isEvent() }

// ItemStarted is sent just before item Index starts.
type ItemStarted struct{ Index int }

// OutputLine is one line of merged stdout/stderr from item Index.
type OutputLine struct {
	Index int
	Line  string
}

// ItemFinished is sent when item Index stops, with its final result.
type ItemFinished struct {
	Index  int
	Result Result
}

// Finished is sent last, with one result per plan item.
type Finished struct{ Results []Result }

func (ItemStarted) isEvent()  {}
func (OutputLine) isEvent()   {}
func (ItemFinished) isEvent() {}
func (Finished) isEvent()     {}

// Executor runs plans against a brew executable.
type Executor struct {
	BrewPath string
	// Env is appended to os.Environ; nil means DefaultEnv.
	Env []string
}

// Execute runs the plan's items strictly in order and returns one result per
// item. It stops at the first failure or when ctx is cancelled; later items
// stay StatusPending. Cancellation sends SIGINT and allows brew a grace
// period before it is killed.
//
// The channel is never closed; the caller owns it and should buffer it. An
// event is delivered if the channel has room or a receiver is ready, and
// otherwise Execute waits for one only until ctx is done. After that further
// events (including Finished) are dropped, while output is still collected, so
// a consumer that stops draining cannot keep Execute from returning once ctx
// is cancelled. A nil channel disables events.
func (e Executor) Execute(ctx context.Context, p plan.Plan, events chan<- Event) []Result {
	results := make([]Result, len(p.Items))
	for i, it := range p.Items {
		results[i] = Result{Item: it, Args: it.Args(), Status: StatusPending}
	}
	send := func(ev Event) {
		if events == nil {
			return
		}
		select {
		case events <- ev:
			return
		default:
		}
		select {
		case events <- ev:
		case <-ctx.Done():
		}
	}
	for i := range results {
		if ctx.Err() != nil {
			break // never started; stays pending
		}
		send(ItemStarted{Index: i})
		results[i] = e.runItem(ctx, i, results[i], send)
		send(ItemFinished{Index: i, Result: results[i]})
		if results[i].Status != StatusCompleted {
			break
		}
	}
	out := make([]Result, len(results))
	copy(out, results)
	send(Finished{Results: out})
	return results
}

func (e Executor) runItem(ctx context.Context, idx int, r Result, send func(Event)) Result {
	env := e.Env
	if env == nil {
		env = DefaultEnv
	}
	lw := &lineWriter{emit: func(line string) { send(OutputLine{Index: idx, Line: line}) }}

	cmd := exec.CommandContext(ctx, e.BrewPath, r.Args...)
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdout = lw // same non-file writer: one pipe, writes serialized by os/exec
	cmd.Stderr = lw
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	cmd.WaitDelay = waitDelay

	r.Status = StatusRunning
	r.Started = time.Now()
	err := cmd.Run()
	r.Ended = time.Now()
	lw.flush()
	r.Output = lw.output()

	r.ExitCode = -1
	if cmd.ProcessState != nil {
		r.ExitCode = cmd.ProcessState.ExitCode()
	}
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		r.Status = StatusCompleted
	case ctx.Err() == nil && exitedZeroAfterWaitDelay(err, cmd.ProcessState):
		// brew itself succeeded; only a leftover child held the pipe open.
		r.Status = StatusCompleted
		r.Output = strings.TrimSuffix(r.Output, "\n") + "\n" + waitDelayNote + "\n"
	case ctx.Err() != nil:
		r.Status = StatusCancelled
		r.Err = fmt.Errorf("brew %s interrupted: %w", strings.Join(r.Args, " "), ctx.Err())
	case errors.As(err, &exitErr):
		r.Status = StatusFailed
		r.Err = &CommandFailedError{Args: r.Args, ExitCode: r.ExitCode}
	default:
		r.Status = StatusFailed
		r.Err = fmt.Errorf("run brew %s: %w", strings.Join(r.Args, " "), err)
	}
	return r
}

// exitedZeroAfterWaitDelay reports whether err is only the WaitDelay timeout
// and the process itself exited with status 0.
func exitedZeroAfterWaitDelay(err error, ps *os.ProcessState) bool {
	return errors.Is(err, exec.ErrWaitDelay) && ps != nil && ps.Exited() && ps.ExitCode() == 0
}

// lineWriter splits a byte stream into lines, emitting each one and keeping a
// tail-capped copy. os/exec calls Write from a single goroutine at a time.
type lineWriter struct {
	emit      func(string)
	partial   []byte
	buf       []byte
	truncated bool
}

func (w *lineWriter) Write(b []byte) (int, error) {
	w.partial = append(w.partial, b...)
	for {
		i := bytes.IndexByte(w.partial, '\n')
		if i < 0 {
			for len(w.partial) > maxLine {
				cut := cutPoint(w.partial)
				w.line(w.partial[:cut])
				w.partial = w.partial[cut:]
			}
			return len(b), nil
		}
		w.longLine(w.partial[:i])
		w.partial = w.partial[i+1:]
	}
}

// cutPoint returns where to split b (len(b) > maxLine): at most maxLine bytes
// in, backed off to a rune boundary.
func cutPoint(b []byte) int {
	cut := maxLine
	for cut > 0 && !utf8.RuneStart(b[cut]) {
		cut--
	}
	if cut == 0 { // not UTF-8 at all; split anywhere
		cut = maxLine
	}
	return cut
}

// longLine emits b, split into pieces of at most maxLine bytes.
func (w *lineWriter) longLine(b []byte) {
	for len(b) > maxLine {
		cut := cutPoint(b)
		w.line(b[:cut])
		b = b[cut:]
	}
	w.line(b)
}

func (w *lineWriter) flush() {
	if len(w.partial) > 0 {
		w.longLine(w.partial)
		w.partial = nil
	}
}

func (w *lineWriter) line(b []byte) {
	s := string(bytes.TrimRight(b, "\r"))
	w.buf = append(w.buf, s...)
	w.buf = append(w.buf, '\n')
	if len(w.buf) > 2*MaxOutput { // compact occasionally to bound memory
		w.buf = append([]byte(nil), w.buf[len(w.buf)-MaxOutput:]...)
		w.truncated = true
	}
	w.emit(s)
}

func (w *lineWriter) output() string {
	if len(w.buf) <= MaxOutput && !w.truncated {
		return string(w.buf)
	}
	tail := w.buf[len(w.buf)-min(len(w.buf), MaxOutput):]
	if i := bytes.IndexByte(tail, '\n'); i >= 0 && i < len(tail)-1 {
		tail = tail[i+1:] // start at a line boundary
	}
	return fmt.Sprintf("[output truncated; showing the last %d bytes]\n", len(tail)) + string(tail)
}
