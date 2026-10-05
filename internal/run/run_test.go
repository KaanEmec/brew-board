package run

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/kaanemec/brew-board/internal/brew"
	"github.com/kaanemec/brew-board/internal/plan"
)

// The test binary doubles as a fake brew: when fakeBrewEnv is set, TestMain
// behaves like brew instead of running tests. Behaviour is keyed by the last
// argument (the package name, or "cleanup"):
//
//	FAKE_FAIL=<name>   exit with FAKE_EXIT (default 1)
//	FAKE_SLEEP=<name>  print "sleeping", wait for SIGINT, print "interrupted", exit 130
//	FAKE_ORPHAN=<name> leave a background child holding stdout open, then exit 0
//	FAKE_BIG=<name>    print FAKE_BIG_LINES numbered lines
const fakeBrewEnv = "BREWBOARD_FAKE_BREW"

func TestMain(m *testing.M) {
	if os.Getenv(fakeBrewEnv) == "1" {
		os.Exit(fakeBrew(os.Args[1:]))
	}
	os.Exit(m.Run())
}

func fakeBrew(args []string) int {
	name := args[len(args)-1]
	fmt.Println("args: " + strings.Join(args, " "))
	fmt.Fprintln(os.Stderr, "stderr: "+name)
	switch name {
	case os.Getenv("FAKE_FAIL"):
		code, err := strconv.Atoi(os.Getenv("FAKE_EXIT"))
		if err != nil {
			code = 1
		}
		return code
	case os.Getenv("FAKE_SLEEP"):
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, os.Interrupt)
		fmt.Println("sleeping")
		select {
		case <-sig:
			fmt.Println("interrupted")
			return 130
		case <-time.After(30 * time.Second):
			return 0
		}
	case os.Getenv("FAKE_ORPHAN"):
		child := exec.Command("sleep", "3")
		child.Stdout = os.Stdout
		child.Stderr = os.Stderr
		if err := child.Start(); err != nil {
			return 2
		}
	case os.Getenv("FAKE_BIG"):
		n, _ := strconv.Atoi(os.Getenv("FAKE_BIG_LINES"))
		for i := range n {
			fmt.Printf("line %06d %s\n", i, strings.Repeat("x", 90))
		}
	}
	return 0
}

func testPlan(t *testing.T, names ...string) plan.Plan {
	t.Helper()
	var sel []plan.Selection
	for _, n := range names {
		sel = append(sel, plan.Selection{
			Package: brew.Package{Name: n, Kind: brew.KindFormula, Outdated: true, AvailableVersion: "2"},
			Op:      plan.OpUpgrade,
		})
	}
	p, err := plan.Build(sel, brew.Inventory{}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// execute runs the plan and collects every event. onLine is called for each
// OutputLine so a test can react (e.g. cancel).
func execute(t *testing.T, ctx context.Context, env []string, p plan.Plan, onLine func(OutputLine)) ([]Result, []Event) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ex := Executor{BrewPath: self, Env: append([]string{fakeBrewEnv + "=1"}, env...)}
	events := make(chan Event, 64)
	done := make(chan []Result, 1)
	go func() { done <- ex.Execute(ctx, p, events) }()

	var got []Event
	for {
		ev := <-events
		got = append(got, ev)
		if l, ok := ev.(OutputLine); ok && onLine != nil {
			onLine(l)
		}
		if _, ok := ev.(Finished); ok {
			break
		}
	}
	return <-done, got
}

func TestExecute(t *testing.T) {
	tests := []struct {
		name       string
		env        []string
		cancelOn   string // cancel when this line is streamed
		wantStatus []Status
		wantExit   []int
		check      func(t *testing.T, rs []Result)
	}{
		{
			name:       "all succeed in order",
			wantStatus: []Status{StatusCompleted, StatusCompleted, StatusCompleted},
			wantExit:   []int{0, 0, 0},
		},
		{
			name:       "second fails, third pending",
			env:        []string{"FAKE_FAIL=b", "FAKE_EXIT=3"},
			wantStatus: []Status{StatusCompleted, StatusFailed, StatusPending},
			wantExit:   []int{0, 3, 0},
			check: func(t *testing.T, rs []Result) {
				var cf *CommandFailedError
				if !errors.As(rs[1].Err, &cf) || cf.ExitCode != 3 || strings.Join(cf.Args, " ") != "upgrade --formula b" {
					t.Errorf("Err = %#v, want CommandFailedError exit 3", rs[1].Err)
				}
			},
		},
		{
			name:       "cancel mid-sleep sends SIGINT",
			env:        []string{"FAKE_SLEEP=b"},
			cancelOn:   "sleeping",
			wantStatus: []Status{StatusCompleted, StatusCancelled, StatusPending},
			wantExit:   []int{0, 130, 0},
			check: func(t *testing.T, rs []Result) {
				if !errors.Is(rs[1].Err, context.Canceled) {
					t.Errorf("Err = %v, want context.Canceled", rs[1].Err)
				}
				if !strings.Contains(rs[1].Output, "interrupted") {
					t.Errorf("brew did not see SIGINT; output %q", rs[1].Output)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			p := testPlan(t, "a", "b", "c")
			rs, evs := execute(t, ctx, tt.env, p, func(l OutputLine) {
				if tt.cancelOn != "" && l.Line == tt.cancelOn {
					cancel()
				}
			})

			if len(rs) != len(p.Items) {
				t.Fatalf("got %d results, want %d", len(rs), len(p.Items))
			}
			for i, r := range rs {
				if r.Status != tt.wantStatus[i] {
					t.Errorf("item %d status = %s, want %s (err %v)", i, r.Status, tt.wantStatus[i], r.Err)
				}
				if r.Status == StatusPending {
					if !r.Started.IsZero() || r.Err != nil || r.Output != "" {
						t.Errorf("pending item %d has run data: %+v", i, r)
					}
					continue
				}
				if r.ExitCode != tt.wantExit[i] {
					t.Errorf("item %d exit = %d, want %d", i, r.ExitCode, tt.wantExit[i])
				}
				if r.Started.IsZero() || r.Ended.Before(r.Started) {
					t.Errorf("item %d bad times %v..%v", i, r.Started, r.Ended)
				}
				if i > 0 && r.Started.Before(rs[i-1].Ended) {
					t.Errorf("item %d started before item %d ended", i, i-1)
				}
				name := r.Item.Package.Name
				want := "args: upgrade --formula " + name + "\n"
				if !strings.Contains(r.Output, want) || !strings.Contains(r.Output, "stderr: "+name+"\n") {
					t.Errorf("item %d output %q missing stdout or stderr", i, r.Output)
				}
			}
			if tt.check != nil {
				tt.check(t, rs)
			}
			checkEvents(t, evs, rs)
		})
	}
}

// checkEvents verifies event order and that streamed lines match Output.
func checkEvents(t *testing.T, evs []Event, rs []Result) {
	t.Helper()
	lines := make(map[int][]string)
	current := -1
	for _, ev := range evs {
		switch ev := ev.(type) {
		case ItemStarted:
			if ev.Index != current+1 {
				t.Errorf("ItemStarted %d after item %d", ev.Index, current)
			}
			current = ev.Index
		case OutputLine:
			if ev.Index != current {
				t.Errorf("OutputLine for %d while %d running", ev.Index, current)
			}
			lines[ev.Index] = append(lines[ev.Index], ev.Line)
		case ItemFinished:
			if ev.Index != current || ev.Result.Status != rs[ev.Index].Status {
				t.Errorf("ItemFinished %d mismatch", ev.Index)
			}
		case Finished:
			if len(ev.Results) != len(rs) {
				t.Errorf("Finished has %d results", len(ev.Results))
			}
		}
	}
	if _, ok := evs[len(evs)-1].(Finished); !ok {
		t.Errorf("last event is %T, want Finished", evs[len(evs)-1])
	}
	for i, r := range rs {
		joined := strings.Join(lines[i], "\n")
		if joined != "" {
			joined += "\n"
		}
		if joined != r.Output {
			t.Errorf("item %d streamed %q, Output %q", i, joined, r.Output)
		}
	}
}

func TestExecuteCapsOutput(t *testing.T) {
	const n = 5000 // ~500 KiB
	rs, evs := execute(t, t.Context(), []string{"FAKE_BIG=a", "FAKE_BIG_LINES=" + strconv.Itoa(n)}, testPlan(t, "a"), nil)
	out := rs[0].Output
	if rs[0].Status != StatusCompleted {
		t.Fatalf("status %s: %v", rs[0].Status, rs[0].Err)
	}
	if !strings.HasPrefix(out, "[output truncated;") {
		t.Errorf("missing truncation note: %q", out[:80])
	}
	if len(out) > MaxOutput+100 {
		t.Errorf("Output is %d bytes, cap %d", len(out), MaxOutput)
	}
	if !strings.Contains(out, fmt.Sprintf("line %06d ", n-1)) || strings.Contains(out, "line 000000 ") {
		t.Errorf("Output does not keep the tail")
	}
	var streamed int
	for _, ev := range evs {
		if _, ok := ev.(OutputLine); ok {
			streamed++
		}
	}
	if streamed != n+2 { // args line + stderr line + n
		t.Errorf("streamed %d lines, want %d", streamed, n+2)
	}
}

func TestLineWriter(t *testing.T) {
	tests := []struct {
		name   string
		writes []string
		want   []string
	}{
		{name: "split across writes", writes: []string{"he", "llo\nwor", "ld\n"}, want: []string{"hello", "world"}},
		{name: "crlf and trailing partial", writes: []string{"a\r\nb"}, want: []string{"a", "b"}},
		{name: "empty line kept", writes: []string{"a\n\nb\n"}, want: []string{"a", "", "b"}},
		{name: "long line flushed", writes: []string{strings.Repeat("z", maxLine)}, want: []string{strings.Repeat("z", maxLine)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []string
			w := &lineWriter{emit: func(s string) { got = append(got, s) }}
			for _, s := range tt.writes {
				if _, err := w.Write([]byte(s)); err != nil {
					t.Fatal(err)
				}
			}
			w.flush()
			if strings.Join(got, "|") != strings.Join(tt.want, "|") {
				t.Errorf("lines = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExecuteStartFailure(t *testing.T) {
	// A missing executable fails to start: no exit code, later items pending.
	rs := Executor{BrewPath: t.TempDir() + "/missing-brew"}.Execute(t.Context(), testPlan(t, "a", "b"), nil)
	if rs[0].Status != StatusFailed || rs[0].ExitCode != -1 || rs[0].Err == nil {
		t.Errorf("result = %+v, want start failure", rs[0])
	}
	if rs[1].Status != StatusPending {
		t.Errorf("second item = %s, want pending", rs[1].Status)
	}
}

func TestExecuteStalledConsumerCancel(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ex := Executor{BrewPath: self, Env: []string{fakeBrewEnv + "=1", "FAKE_SLEEP=a"}}
	events := make(chan Event, 1) // never drained: the first OutputLine send blocks
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan []Result, 1)
	go func() { done <- ex.Execute(ctx, testPlan(t, "a", "b"), events) }()

	time.Sleep(500 * time.Millisecond)
	cancel()
	select {
	case rs := <-done:
		if rs[0].Status != StatusCancelled {
			t.Errorf("item 0 = %s (err %v), want cancelled", rs[0].Status, rs[0].Err)
		}
		if rs[1].Status != StatusPending {
			t.Errorf("item 1 = %s, want pending", rs[1].Status)
		}
		if !strings.Contains(rs[0].Output, "args: upgrade --formula a") {
			t.Errorf("output not accumulated after events were dropped: %q", rs[0].Output)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Execute did not return after cancel with a stalled consumer")
	}
}

func TestExecuteWaitDelayAfterSuccess(t *testing.T) {
	old := waitDelay
	waitDelay = 200 * time.Millisecond
	t.Cleanup(func() { waitDelay = old })

	rs, _ := execute(t, t.Context(), []string{"FAKE_ORPHAN=a"}, testPlan(t, "a"), nil)
	r := rs[0]
	if r.Status != StatusCompleted || r.Err != nil || r.ExitCode != 0 {
		t.Fatalf("result = %s exit %d err %v, want completed", r.Status, r.ExitCode, r.Err)
	}
	if !strings.HasSuffix(r.Output, waitDelayNote+"\n") {
		t.Errorf("output missing wait-delay note: %q", r.Output)
	}
}

func TestLineWriterSplitsLongLinesAtRuneBoundary(t *testing.T) {
	// "é" is 2 bytes, so a cut at maxLine would land mid-rune for odd offsets.
	long := "x" + strings.Repeat("é", maxLine) // 1 + 2*maxLine bytes
	for _, terminated := range []bool{true, false} {
		var got []string
		w := &lineWriter{emit: func(s string) { got = append(got, s) }}
		in := long
		if terminated {
			in += "\n"
		}
		// Feed in awkward chunks so partial runes straddle writes.
		for b := []byte(in); len(b) > 0; {
			n := min(len(b), 4097)
			if _, err := w.Write(b[:n]); err != nil {
				t.Fatal(err)
			}
			b = b[n:]
		}
		w.flush()
		if len(got) < 2 {
			t.Fatalf("terminated=%v: got %d lines, want a split", terminated, len(got))
		}
		for i, l := range got {
			if len(l) > maxLine {
				t.Errorf("line %d is %d bytes, cap %d", i, len(l), maxLine)
			}
			if !utf8.ValidString(l) {
				t.Errorf("line %d split a rune", i)
			}
		}
		if strings.Join(got, "") != long {
			t.Errorf("terminated=%v: pieces do not reassemble the input", terminated)
		}
	}
}

func TestDefaultEnvDisablesHiddenWork(t *testing.T) {
	for _, want := range []string{
		"HOMEBREW_NO_AUTO_UPDATE=1", "HOMEBREW_NO_AUTOREMOVE=1", "HOMEBREW_NO_INSTALL_CLEANUP=1",
		"HOMEBREW_NO_ENV_HINTS=1", "HOMEBREW_NO_COLOR=1", "HOMEBREW_NO_EMOJI=1",
	} {
		if !slices.Contains(DefaultEnv, want) {
			t.Errorf("DefaultEnv missing %s", want)
		}
	}
}
