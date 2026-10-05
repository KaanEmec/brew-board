package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/kaanemec/brew-board/internal/brew"
	"github.com/kaanemec/brew-board/internal/plan"
	"github.com/kaanemec/brew-board/internal/run"
)

// fakeExecutor records each call and runs script (default: every item
// completes with one output line).
type fakeExecutor struct {
	mu     sync.Mutex
	plans  []plan.Plan
	ctx    context.Context
	script func(ctx context.Context, p plan.Plan, events chan<- run.Event) []run.Result
}

func (f *fakeExecutor) Execute(ctx context.Context, p plan.Plan, events chan<- run.Event) []run.Result {
	f.mu.Lock()
	f.plans = append(f.plans, p)
	f.ctx = ctx
	script := f.script
	f.mu.Unlock()
	if script == nil {
		script = completeAll
	}
	return script(ctx, p, events)
}

func (f *fakeExecutor) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.plans)
}

func (f *fakeExecutor) lastCtx() context.Context {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ctx
}

func completeAll(_ context.Context, p plan.Plan, events chan<- run.Event) []run.Result {
	results := make([]run.Result, len(p.Items))
	for i, it := range p.Items {
		events <- run.ItemStarted{Index: i}
		events <- run.OutputLine{Index: i, Line: "==> Running " + it.Command()}
		results[i] = run.Result{
			Item:     it,
			Args:     it.Args(),
			Status:   run.StatusCompleted,
			Started:  fixedTime,
			Ended:    fixedTime,
			ExitCode: 0,
		}
		events <- run.ItemFinished{Index: i, Result: results[i]}
	}
	events <- run.Finished{Results: results}
	return results
}

// upgradedInventory is the fixture after git was upgraded; VS Code is unchanged.
func upgradedInventory() brew.Inventory {
	inv := fixtureInventory()
	inv.Formulae[0].InstalledVersions = []string{"2.51.0"}
	inv.Formulae[0].Outdated = false
	return inv
}

func sessionModel(t *testing.T, loader brew.Loader, exec Executor, width int, opts ...Option) model {
	t.Helper()
	opts = append([]Option{
		WithClock(func() time.Time { return fixedTime }),
		WithExecutor(exec),
		WithHomeDir(func() (string, error) { return t.TempDir(), nil }),
	}, opts...)
	m := New(loader, opts...).(model)
	next, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: 30})
	m = next.(model)
	return runCmd(t, m, m.Init())
}

// stage moves the cursor to each named package and presses space.
func stage(t *testing.T, m model, pkgs ...string) model {
	t.Helper()
	for _, name := range pkgs {
		m = cursorTo(t, m, name)
		m, _ = press(t, m, "space")
	}
	return m
}

func cursorTo(t *testing.T, m model, name string) model {
	t.Helper()
	for i, p := range m.visible {
		if p.Name == name {
			m.cursor = i
			return m
		}
	}
	t.Fatalf("%s not visible: %v", name, names(m))
	return m
}

// review presses u and completes the selection check.
func review(t *testing.T, m model) model {
	t.Helper()
	m, cmd := press(t, m, "u")
	m = runCmd(t, m, cmd)
	if m.mode != viewReview {
		t.Fatalf("mode = %v, expected review", m.mode)
	}
	return m
}

// confirm presses y and returns the execute and wait commands.
func confirm(t *testing.T, m model) (model, tea.Cmd, tea.Cmd) {
	t.Helper()
	m, cmd := press(t, m, "y")
	if cmd == nil {
		t.Fatal("y returned no command")
	}
	batch, ok := cmd().(tea.BatchMsg)
	if !ok || len(batch) != 2 {
		t.Fatalf("y should start the executor and the event wait, got %#v", batch)
	}
	return m, batch[0], batch[1]
}

// drain feeds events until the run finishes, then completes the refresh.
func drain(t *testing.T, m model, wait tea.Cmd) model {
	t.Helper()
	cmd := wait
	for m.mode == viewRunning && !m.sess.isFinished {
		next, c := m.Update(cmd())
		m, cmd = next.(model), c
	}
	return runCmd(t, m, cmd)
}

// runSession stages pkgs, reviews, confirms, and runs to the receipt.
func runSession(t *testing.T, m model, pkgs ...string) model {
	t.Helper()
	m = review(t, stage(t, m, pkgs...))
	m, execute, wait := confirm(t, m)
	execute()
	return drain(t, m, wait)
}

func TestSpaceStagesOnlyUpgradable(t *testing.T) {
	m, _ := loaded(t, 100)
	tests := []struct {
		pkg, notice string
	}{
		{pkg: "jq", notice: "jq is up to date"},
		{pkg: "node", notice: "node is pinned"},
	}
	for _, tt := range tests {
		got := stage(t, m, tt.pkg)
		if len(got.selected) != 0 || !strings.Contains(got.notice, tt.notice) {
			t.Errorf("%s: selected=%v notice=%q, expected refusal %q", tt.pkg, got.selected, got.notice, tt.notice)
		}
	}

	m = stage(t, m, "git")
	if !m.selected[pkgKey{name: "git", kind: brew.KindFormula}] {
		t.Fatalf("git not selected: %v", m.selected)
	}
	v := m.View()
	for _, want := range []string{"[x] git", "[ ] visual-studio-code", "1 selected"} {
		if !strings.Contains(v, want) {
			t.Errorf("view missing %q:\n%s", want, v)
		}
	}
	if strings.Contains(v, "[ ] jq") {
		t.Errorf("up-to-date rows should not show a box:\n%s", v)
	}
	if m = stage(t, m, "git"); len(m.selected) != 0 {
		t.Errorf("second space should unselect: %v", m.selected)
	}
}

func TestTightWidthUsesStarMarker(t *testing.T) {
	m, _ := loaded(t, 40)
	m = stage(t, m, "git")
	if v := m.View(); !strings.Contains(v, "* git") || strings.Contains(v, "[x]") {
		t.Errorf("tight list should mark selection with *:\n%s", v)
	}
}

func TestSelectAllVisible(t *testing.T) {
	m, _ := loaded(t, 100)
	m, _ = press(t, m, "a")
	if len(m.selected) != 2 || !m.selected[pkgKey{name: "git", kind: brew.KindFormula}] ||
		!m.selected[pkgKey{name: "visual-studio-code", kind: brew.KindCask}] {
		t.Fatalf("a should select outdated, unpinned packages: %v", m.selected)
	}
	if m, _ = press(t, m, "a"); len(m.selected) != 0 {
		t.Errorf("second a should unselect all: %v", m.selected)
	}
	m, _ = press(t, m, "f", "f", "a") // casks only
	if len(m.selected) != 1 {
		t.Errorf("a should only touch visible packages: %v", m.selected)
	}
}

func TestReviewNeedsSelection(t *testing.T) {
	m, _ := loaded(t, 100)
	m, cmd := press(t, m, "u")
	if cmd != nil || m.mode != viewList || m.notice != "select outdated packages with space" {
		t.Errorf("u without selection: cmd=%v mode=%v notice=%q", cmd != nil, m.mode, m.notice)
	}
}

func TestReviewDropsStaleSelection(t *testing.T) {
	loader := &fakeLoader{results: []fakeResult{{inv: fixtureInventory()}, {inv: upgradedInventory()}}}
	exec := &fakeExecutor{}
	m := sessionModel(t, loader, exec, 100)
	m = stage(t, m, "git", "visual-studio-code")

	m, cmd := press(t, m, "u")
	if v := m.View(); !strings.Contains(v, "checking selections…") {
		t.Errorf("missing checking state:\n%s", v)
	}
	if again, _ := press(t, m, "space"); again.notice != "checking selections…" {
		t.Errorf("selection changed while checking: %q", again.notice)
	}
	m = runCmd(t, m, cmd)
	if m.mode != viewReview {
		t.Fatalf("mode = %v, expected review", m.mode)
	}
	want := "git: already up to date (installed 2.51.0) — removed from plan"
	if len(m.sess.problems) != 1 || m.sess.problems[0] != want {
		t.Errorf("problems = %q, expected %q", m.sess.problems, want)
	}
	if len(m.sess.plan.Items) != 1 || m.sess.plan.Items[0].Package.Name != "visual-studio-code" {
		t.Errorf("plan = %+v, expected only visual-studio-code", m.sess.plan.Items)
	}
	if m.selected[pkgKey{name: "git", kind: brew.KindFormula}] || len(m.selected) != 1 {
		t.Errorf("stale git should leave the selection: %v", m.selected)
	}
	v := m.View()
	for _, s := range []string{want, "brew upgrade --cask visual-studio-code", "Upgrade cask visual-studio-code", dependencyNote, "y run this command · esc back"} {
		if !strings.Contains(v, s) {
			t.Errorf("review missing %q:\n%s", s, v)
		}
	}

	t.Run("empty plan cannot run", func(t *testing.T) {
		loader := &fakeLoader{results: []fakeResult{{inv: fixtureInventory()}, {inv: upgradedInventory()}}}
		m := sessionModel(t, loader, exec, 100)
		m = review(t, stage(t, m, "git"))
		if v := m.View(); !strings.Contains(v, "nothing left to run") {
			t.Errorf("missing empty-plan message:\n%s", v)
		}
		m, cmd := press(t, m, "y")
		if cmd != nil || m.mode != viewReview || exec.calls() != 0 {
			t.Errorf("y on empty plan: cmd=%v mode=%v calls=%d", cmd != nil, m.mode, exec.calls())
		}
		if m, _ = press(t, m, "esc"); m.mode != viewList || len(m.selected) != 0 {
			t.Errorf("esc: mode=%v selected=%v", m.mode, m.selected)
		}
	})
}

func TestReviewEscKeepsSelections(t *testing.T) {
	loader := &fakeLoader{results: []fakeResult{{inv: fixtureInventory()}}}
	exec := &fakeExecutor{}
	m := sessionModel(t, loader, exec, 100)
	m = review(t, stage(t, m, "git", "visual-studio-code"))
	if v := m.View(); !strings.Contains(v, "y run these 2 commands · esc back") || !strings.Contains(v, "brew upgrade --formula git") {
		t.Errorf("review footer or command missing:\n%s", v)
	}
	m, cmd := press(t, m, "esc")
	if cmd != nil || m.mode != viewList || len(m.selected) != 2 || exec.calls() != 0 {
		t.Errorf("esc: cmd=%v mode=%v selected=%v calls=%d", cmd != nil, m.mode, m.selected, exec.calls())
	}
}

func TestReviewWithoutExecutorRefuses(t *testing.T) {
	m := sessionModel(t, &fakeLoader{results: []fakeResult{{inv: fixtureInventory()}}}, nil, 100)
	m = review(t, stage(t, m, "git"))
	if m, cmd := press(t, m, "y"); cmd != nil || m.mode != viewReview || m.notice == "" {
		t.Errorf("y without executor: cmd=%v mode=%v notice=%q", cmd != nil, m.mode, m.notice)
	}
}

func TestExecutionStartsOnce(t *testing.T) {
	loader := &fakeLoader{results: []fakeResult{{inv: fixtureInventory()}}}
	exec := &fakeExecutor{}
	m := sessionModel(t, loader, exec, 100)
	m = review(t, stage(t, m, "git"))
	m, execute, wait := confirm(t, m)
	if m.mode != viewRunning {
		t.Fatalf("mode = %v, expected running", m.mode)
	}
	for _, k := range []string{"y", "u", "q", "esc", "enter", "r"} {
		next, cmd := press(t, m, k)
		if cmd != nil || next.mode != viewRunning {
			t.Errorf("%s while running: cmd=%v mode=%v", k, cmd != nil, next.mode)
		}
	}
	execute()
	m = drain(t, m, wait)
	if exec.calls() != 1 {
		t.Errorf("executor calls = %d, expected 1", exec.calls())
	}
}

func TestRunEventsUpdateScreen(t *testing.T) {
	loader := &fakeLoader{results: []fakeResult{{inv: fixtureInventory()}}}
	m := sessionModel(t, loader, &fakeExecutor{}, 100)
	m = review(t, stage(t, m, "git", "visual-studio-code"))
	m, execute, wait := confirm(t, m)
	if v := m.View(); !strings.Contains(v, "[pending]") {
		t.Errorf("items should start pending:\n%s", v)
	}
	execute()
	step := func(m model) model {
		t.Helper()
		next, cmd := m.Update(wait())
		if cmd == nil {
			t.Fatal("event handling did not wait for the next event")
		}
		return next.(model)
	}

	m = step(m) // ItemStarted 0
	if m.sess.results[0].Status != run.StatusRunning {
		t.Errorf("status = %s, expected running", m.sess.results[0].Status)
	}
	if v := m.View(); !strings.Contains(v, "Running 1/2: brew upgrade --formula git") || !strings.Contains(v, "[running]") {
		t.Errorf("running item not shown:\n%s", v)
	}
	m = step(m) // OutputLine
	if v := m.View(); !strings.Contains(v, "==> Running brew upgrade --formula git") {
		t.Errorf("output not shown:\n%s", v)
	}
	m = step(m) // ItemFinished 0
	if v := m.View(); !strings.Contains(v, "[completed 0]") || !strings.Contains(v, "[pending]") {
		t.Errorf("item statuses not updated:\n%s", v)
	}
}

func TestOutputIsBoundedAndSanitized(t *testing.T) {
	loader := &fakeLoader{results: []fakeResult{{inv: fixtureInventory()}}}
	m := sessionModel(t, loader, &fakeExecutor{}, 100)
	m = review(t, stage(t, m, "git"))
	m, _, _ = confirm(t, m)
	feed := func(line string) {
		next, _ := m.Update(runEventMsg{ev: run.OutputLine{Line: line}})
		m = next.(model)
	}
	for i := range maxOutputLines + 500 {
		feed(fmt.Sprintf("line %d", i))
	}
	if n := len(m.sess.output); n != maxOutputLines {
		t.Errorf("kept %d lines, expected %d", n, maxOutputLines)
	}
	if last := m.sess.output[len(m.sess.output)-1]; last != fmt.Sprintf("line %d", maxOutputLines+499) {
		t.Errorf("last line = %q", last)
	}
	feed("\x1b[31m 10%\r 100%\x1b[0m\tdone")
	if got := m.sess.output[len(m.sess.output)-1]; got != " 100%    done" {
		t.Errorf("sanitized line = %q", got)
	}
}

func TestCancelAsksThenInterrupts(t *testing.T) {
	exec := &fakeExecutor{script: func(ctx context.Context, p plan.Plan, events chan<- run.Event) []run.Result {
		events <- run.ItemStarted{Index: 0}
		<-ctx.Done()
		results := []run.Result{{
			Item: p.Items[0], Args: p.Items[0].Args(), Status: run.StatusCancelled,
			Started: fixedTime, Ended: fixedTime, ExitCode: -1, Err: ctx.Err(),
		}}
		events <- run.ItemFinished{Index: 0, Result: results[0]}
		events <- run.Finished{Results: results}
		return results
	}}
	loader := &fakeLoader{results: []fakeResult{{inv: fixtureInventory()}}}
	m := sessionModel(t, loader, exec, 100)
	m = review(t, stage(t, m, "git"))
	m, execute, wait := confirm(t, m)
	t.Cleanup(m.sess.cancel)
	done := make(chan struct{})
	go func() {
		defer close(done)
		execute()
	}()
	next, _ := m.Update(wait()) // ItemStarted
	m = next.(model)

	m, _ = press(t, m, "x")
	if v := m.View(); !strings.Contains(v, cancelPrompt) {
		t.Fatalf("missing cancel prompt:\n%s", v)
	}
	m, _ = press(t, m, "n")
	if m.sess.isConfirmingCancel || exec.lastCtx().Err() != nil {
		t.Fatal("n should keep the command running")
	}
	m, _ = press(t, m, "ctrl+c")
	if !m.sess.isConfirmingCancel {
		t.Fatal("ctrl+c should ask before cancelling")
	}
	m, _ = press(t, m, "y")
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("executor did not observe cancellation")
	}
	if !errors.Is(exec.lastCtx().Err(), context.Canceled) {
		t.Errorf("executor context err = %v, expected canceled", exec.lastCtx().Err())
	}
	m = drain(t, m, wait)
	if m.mode != viewReceipt || m.sess.receipt.Summary.Cancelled != 1 {
		t.Fatalf("mode=%v summary=%+v", m.mode, m.sess.receipt.Summary)
	}
	if v := m.View(); !strings.Contains(v, "1 cancelled") || !strings.Contains(v, "partial work") {
		t.Errorf("receipt missing cancellation:\n%s", v)
	}
}

func TestClosedChannelFinishesRun(t *testing.T) {
	exec := &fakeExecutor{script: func(context.Context, plan.Plan, chan<- run.Event) []run.Result { return nil }}
	loader := &fakeLoader{results: []fakeResult{{inv: fixtureInventory()}}}
	m := sessionModel(t, loader, exec, 100)
	m = review(t, stage(t, m, "git"))
	m, execute, wait := confirm(t, m)
	execute() // returns without Finished and closes the channel
	if m = drain(t, m, wait); m.mode != viewReceipt || m.sess.receipt.Summary.NotRun != 1 {
		t.Errorf("mode=%v summary=%+v", m.mode, m.sess.receipt.Summary)
	}
}

func TestFinishedRefreshesAndShowsReceipt(t *testing.T) {
	after := upgradedInventory()
	loader := &fakeLoader{results: []fakeResult{{inv: fixtureInventory()}, {inv: fixtureInventory()}, {inv: after}}}
	m := sessionModel(t, loader, &fakeExecutor{}, 100)
	m = runSession(t, m, "git", "visual-studio-code")
	if loader.calls != 3 {
		t.Errorf("loader calls = %d, expected 3 (initial, check, post-run)", loader.calls)
	}
	if m.mode != viewReceipt {
		t.Fatalf("mode = %v, expected receipt", m.mode)
	}
	s := m.sess.receipt.Summary
	if s.Upgraded != 1 || s.Uncertain != 1 || s.Failed+s.Cancelled+s.NotRun != 0 {
		t.Errorf("summary = %+v, expected 1 upgraded and 1 uncertain", s)
	}
	v := m.View()
	for _, want := range []string{
		"1 upgraded · 0 failed · 0 cancelled · 0 not run · 1 uncertain",
		"brew upgrade --formula git: upgraded, exit 0, 2.50.0 -> 2.51.0",
		"brew upgrade --cask visual-studio-code: uncertain",
		"A result is uncertain",
		"s save · c cleanup",
	} {
		if !strings.Contains(v, want) {
			t.Errorf("receipt missing %q:\n%s", want, v)
		}
	}
	if git, _ := plan.Find(m.inv, "git", brew.KindFormula); git.Outdated {
		t.Error("inventory not refreshed after the run")
	}
}

func TestFailedPostRunRefreshIsLabelled(t *testing.T) {
	loader := &fakeLoader{results: []fakeResult{{inv: fixtureInventory()}, {inv: fixtureInventory()}, {err: errors.New("boom")}, {inv: upgradedInventory()}}}
	m := sessionModel(t, loader, &fakeExecutor{}, 100)
	m = runSession(t, m, "git")
	if m.mode != viewReceipt || m.sess.refreshErr == nil {
		t.Fatalf("mode=%v refreshErr=%v", m.mode, m.sess.refreshErr)
	}
	if v := m.View(); !strings.Contains(v, "not verified") || !strings.Contains(v, "NOT VERIFIED") || m.sess.receipt.Verified {
		t.Errorf("receipt should flag unverified versions:\n%s", v)
	}
	m, cmd := press(t, m, "r")
	m = runCmd(t, m, cmd)
	if m.sess.refreshErr != nil || !m.sess.receipt.Verified || m.sess.receipt.Summary.Upgraded != 1 {
		t.Errorf("retry: refreshErr=%v summary=%+v", m.sess.refreshErr, m.sess.receipt.Summary)
	}
}

func TestSaveReceipt(t *testing.T) {
	dir := t.TempDir()
	loader := &fakeLoader{results: []fakeResult{{inv: fixtureInventory()}, {inv: fixtureInventory()}, {inv: upgradedInventory()}}}
	m := sessionModel(t, loader, &fakeExecutor{}, 100, WithHomeDir(func() (string, error) { return dir, nil }))
	m = runSession(t, m, "git")
	m, _ = press(t, m, "s")
	want := m.sess.savedPath
	if filepath.Dir(want) != dir || !strings.HasPrefix(filepath.Base(want), "brewboard-receipt-") || m.sess.saveErr != nil {
		t.Fatalf("savedPath=%q err=%v, expected a receipt in %q", want, m.sess.saveErr, dir)
	}
	data, err := os.ReadFile(want)
	if err != nil || !strings.Contains(string(data), "brew upgrade --formula git: upgraded") {
		t.Errorf("receipt file: err=%v content=%q", err, data)
	}
	if v := m.View(); !strings.Contains(strings.Join(strings.Fields(v), ""), "Savedto"+want) { // the path may wrap
		t.Errorf("view missing saved path:\n%s", v)
	}
	if m, _ = press(t, m, "s"); !strings.Contains(m.notice, "already saved") {
		t.Errorf("second s: notice=%q", m.notice)
	}

	t.Run("home directory error", func(t *testing.T) {
		loader := &fakeLoader{results: []fakeResult{{inv: fixtureInventory()}}}
		m := sessionModel(t, loader, &fakeExecutor{}, 100, WithHomeDir(func() (string, error) { return "", errors.New("no home") }))
		m = runSession(t, m, "git")
		if m, _ = press(t, m, "s"); m.sess.saveErr == nil || !strings.Contains(m.View(), "no home") {
			t.Errorf("save error not shown: %v", m.sess.saveErr)
		}
	})
}

func TestCleanupGoesThroughReview(t *testing.T) {
	loader := &fakeLoader{results: []fakeResult{{inv: fixtureInventory()}, {inv: fixtureInventory()}, {inv: upgradedInventory()}}}
	exec := &fakeExecutor{}
	m := sessionModel(t, loader, exec, 100)
	m = runSession(t, m, "git")

	m, cmd := press(t, m, "c")
	if cmd != nil || m.mode != viewReview || exec.calls() != 1 {
		t.Fatalf("c: cmd=%v mode=%v calls=%d", cmd != nil, m.mode, exec.calls())
	}
	v := m.View()
	for _, want := range []string{"brew cleanup", "Remove old versions", "y run this command · esc back", "not saved"} {
		if !strings.Contains(v, want) {
			t.Errorf("cleanup review missing %q:\n%s", want, v)
		}
	}
	if m, _ = press(t, m, "esc"); m.mode != viewReceipt || exec.calls() != 1 || m.sess.plan.Items[0].Op != plan.OpUpgrade {
		t.Fatalf("esc from cleanup review: mode=%v calls=%d plan=%v", m.mode, exec.calls(), m.sess.plan.Items)
	}

	m, _ = press(t, m, "c")
	m, execute, wait := confirm(t, m)
	execute()
	m = drain(t, m, wait)
	if exec.calls() != 2 || len(exec.plans[1].Items) != 1 || exec.plans[1].Items[0].Op != plan.OpCleanup {
		t.Fatalf("cleanup plan = %+v", exec.plans)
	}
	if v := m.View(); m.mode != viewReceipt || !strings.Contains(v, "brew cleanup: completed") {
		t.Errorf("cleanup receipt:\n%s", v)
	}
	if m, _ = press(t, m, "c"); m.mode != viewReceipt || !strings.Contains(m.notice, "already ran") {
		t.Errorf("second cleanup offer: mode=%v notice=%q", m.mode, m.notice)
	}
}

func TestReceiptBackClearsSelections(t *testing.T) {
	for _, key := range []string{"esc", "enter"} {
		t.Run(key, func(t *testing.T) {
			loader := &fakeLoader{results: []fakeResult{{inv: fixtureInventory()}, {inv: fixtureInventory()}, {inv: upgradedInventory()}}}
			m := sessionModel(t, loader, &fakeExecutor{}, 100)
			m = runSession(t, m, "git", "visual-studio-code")
			m, _ = press(t, m, key)
			if m.mode != viewList || len(m.selected) != 0 || len(m.sess.plan.Items) != 0 {
				t.Errorf("mode=%v selected=%v plan=%v", m.mode, m.selected, m.sess.plan.Items)
			}
			if v := m.View(); strings.Contains(v, "selected") || !strings.Contains(v, "2.51.0") {
				t.Errorf("list should show refreshed inventory and no selection:\n%s", v)
			}
		})
	}
}

func TestSessionViewWidths(t *testing.T) {
	for _, width := range []int{60, 120} {
		t.Run(fmt.Sprintf("width %d", width), func(t *testing.T) {
			loader := &fakeLoader{results: []fakeResult{{inv: fixtureInventory()}, {inv: upgradedInventory()}}}
			m := sessionModel(t, loader, &fakeExecutor{}, width)
			screens := map[string]model{}
			m = review(t, stage(t, m, "git", "visual-studio-code"))
			screens["review with problems"] = m

			running, _, _ := confirm(t, m)
			next, _ := running.Update(runEventMsg{ev: run.ItemStarted{Index: 0}})
			next, _ = next.Update(runEventMsg{ev: run.OutputLine{Line: strings.Repeat("long output ", 30)}})
			running = next.(model)
			screens["running"] = running
			screens["cancel prompt"] = mustPress(t, running, "x")

			m, execute, wait := confirm(t, m)
			execute()
			m = drain(t, m, wait)
			screens["receipt"] = m
			screens["receipt saved"] = mustPress(t, m, "s")
			screens["cleanup review"] = mustPress(t, m, "c")

			for name, s := range screens {
				for _, line := range strings.Split(s.View(), "\n") {
					if w := ansi.StringWidth(line); w > width {
						t.Errorf("%s: line wider than %d (%d): %q", name, width, w, line)
					}
				}
			}
		})
	}
}

func resize(m model, w, h int) model {
	next, _ := m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	return next.(model)
}

func TestReviewScrollsLongPlans(t *testing.T) {
	inv := brew.Inventory{LoadedAt: fixedTime}
	for i := range 20 {
		inv.Formulae = append(inv.Formulae, brew.Package{
			Name: fmt.Sprintf("pkg%02d", i), Kind: brew.KindFormula,
			InstalledVersions: []string{"1.0"}, AvailableVersion: "1.1", Outdated: true,
		})
	}
	m := sessionModel(t, &fakeLoader{results: []fakeResult{{inv: inv}}}, &fakeExecutor{}, 80)
	m = resize(m, 80, 24)
	m, _ = press(t, m, "a")
	m = review(t, m)
	last := "brew upgrade --formula pkg19"
	if v := m.View(); strings.Contains(v, last) || !strings.Contains(v, "j/k scroll · y run these 20 commands") {
		t.Fatalf("long plan should scroll:\n%s", v)
	}
	for range 5 {
		m, _ = press(t, m, "pgdown")
	}
	if v := m.View(); !strings.Contains(v, last) || !strings.Contains(v, dependencyNote[:40]) {
		t.Errorf("end of plan not reachable by scrolling:\n%s", v)
	}
	if m, _ = press(t, m, "k"); m.sess.scroll != m.maxScroll()-1 {
		t.Errorf("k: scroll = %d, expected %d", m.sess.scroll, m.maxScroll()-1)
	}
}

func TestHelpFitsSmallTerminal(t *testing.T) {
	m, _ := loaded(t, 100)
	m = resize(m, 100, 24)
	m, _ = press(t, m, "?")
	v := m.View()
	for _, want := range []string{"space / a", "y / esc (review)", "x, ctrl+c (running)", "s / c (receipt)", "q / ctrl+c", "Markers"} {
		if !strings.Contains(v, want) {
			t.Errorf("help at 24 rows missing %q:\n%s", want, v)
		}
	}
}

// blockingExecutor starts one item and blocks until its context is done,
// then reports the item cancelled. started is closed once Execute runs.
func blockingExecutor(started chan struct{}) *fakeExecutor {
	return &fakeExecutor{script: func(ctx context.Context, p plan.Plan, events chan<- run.Event) []run.Result {
		close(started)
		events <- run.ItemStarted{Index: 0}
		<-ctx.Done()
		results := []run.Result{{Item: p.Items[0], Args: p.Items[0].Args(), Status: run.StatusCancelled, ExitCode: -1, Err: ctx.Err()}}
		events <- run.Finished{Results: results}
		return results
	}}
}

func TestRetryLoadBlocksCleanup(t *testing.T) {
	loader := &fakeLoader{results: []fakeResult{{inv: fixtureInventory()}, {inv: fixtureInventory()}, {err: errors.New("boom")}, {inv: upgradedInventory()}}}
	exec := &fakeExecutor{}
	m := sessionModel(t, loader, exec, 100)
	m = runSession(t, m, "git")
	if m.mode != viewReceipt || m.sess.refreshErr == nil {
		t.Fatalf("mode=%v refreshErr=%v", m.mode, m.sess.refreshErr)
	}

	m, retry := press(t, m, "r") // the retry load is in flight
	if retry == nil || !m.isLoading {
		t.Fatal("r should start a retry load")
	}
	m, cmd := press(t, m, "c")
	if cmd != nil || m.mode != viewReceipt || m.notice != loadBusyNotice {
		t.Fatalf("c during retry: cmd=%v mode=%v notice=%q", cmd != nil, m.mode, m.notice)
	}
	m, cmd = press(t, m, "y")
	if cmd != nil || m.mode != viewReceipt || exec.calls() != 1 {
		t.Fatalf("y during retry: cmd=%v mode=%v calls=%d", cmd != nil, m.mode, exec.calls())
	}
	m = runCmd(t, m, retry)
	if m.mode != viewReceipt || m.sess.refreshErr != nil || !m.sess.receipt.Verified || m.sess.receipt.Summary.Upgraded != 1 {
		t.Errorf("after retry: mode=%v refreshErr=%v receipt=%+v", m.mode, m.sess.refreshErr, m.sess.receipt.Summary)
	}

	t.Run("review y waits for a load", func(t *testing.T) {
		m, _ := press(t, m, "c")
		m.isLoading = true
		if m, cmd := press(t, m, "y"); cmd != nil || m.mode != viewReview || m.notice != loadBusyNotice || exec.calls() != 1 {
			t.Errorf("y while loading: cmd=%v mode=%v notice=%q calls=%d", cmd != nil, m.mode, m.notice, exec.calls())
		}
	})

	t.Run("receipt load never interrupts a run", func(t *testing.T) {
		m, _ := press(t, m, "c")
		m, _, _ = confirm(t, m)
		t.Cleanup(m.sess.cancel)
		m.pending = purposeReceipt // a late receipt load lands mid-run
		next, _ := m.Update(inventoryLoadedMsg{inv: upgradedInventory()})
		if got := next.(model); got.mode != viewRunning || !got.sess.isRunning() || got.sess.isCancelling {
			t.Errorf("late load: mode=%v running=%v cancelling=%v", got.mode, got.sess.isRunning(), got.sess.isCancelling)
		}
	})
}

func TestAbandonedRunIsCancelledAndDrained(t *testing.T) {
	started := make(chan struct{})
	exec := blockingExecutor(started)
	m := sessionModel(t, &fakeLoader{results: []fakeResult{{inv: fixtureInventory()}}}, exec, 100)
	m = review(t, stage(t, m, "git"))
	m, execute, wait := confirm(t, m)
	done := make(chan struct{})
	go func() {
		defer close(done)
		execute()
	}()
	<-started

	m.mode = viewReceipt // a regression switches screens mid-run
	next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = next.(model)
	if !errors.Is(exec.lastCtx().Err(), context.Canceled) {
		t.Fatalf("abandoned run not cancelled: %v", exec.lastCtx().Err())
	}
	for cmd := wait; cmd != nil; { // events keep draining until the channel closes
		next, cmd = m.Update(cmd())
		m = next.(model)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("executor did not return")
	}
}

func TestDismissedReceiptStaysDismissed(t *testing.T) {
	loader := &fakeLoader{results: []fakeResult{{inv: fixtureInventory()}, {inv: fixtureInventory()}, {err: errors.New("boom")}, {inv: upgradedInventory()}}}
	m := sessionModel(t, loader, &fakeExecutor{}, 100)
	m = runSession(t, m, "git")
	m, retry := press(t, m, "r")
	m, _ = press(t, m, "esc")
	if m.mode != viewList || len(m.selected) != 0 {
		t.Fatalf("esc: mode=%v selected=%v", m.mode, m.selected)
	}
	m = stage(t, m, "visual-studio-code")
	m = runCmd(t, m, retry)
	if m.mode != viewList || len(m.sess.receipt.Changes) != 0 {
		t.Fatalf("retry after dismissal reopened a receipt: mode=%v", m.mode)
	}
	if git, _ := plan.Find(m.inv, "git", brew.KindFormula); git.Outdated {
		t.Error("retry inventory not applied")
	}
	if m, _ = press(t, m, "esc"); !m.selected[pkgKey{name: "visual-studio-code", kind: brew.KindCask}] {
		t.Errorf("selection lost: %v", m.selected)
	}
}

func TestReviewShowsFreshVersions(t *testing.T) {
	fresh := fixtureInventory()
	fresh.Formulae[0].InstalledVersions = []string{"2.50.1"}
	loader := &fakeLoader{results: []fakeResult{{inv: fixtureInventory()}, {inv: fresh}}}
	m := sessionModel(t, loader, &fakeExecutor{}, 100)
	m = review(t, stage(t, m, "git"))
	if len(m.sess.plan.Items) != 1 || strings.Join(m.sess.plan.Items[0].Package.InstalledVersions, ",") != "2.50.1" {
		t.Fatalf("plan = %+v", m.sess.plan.Items)
	}
	if v := m.View(); !strings.Contains(v, "from 2.50.1 to 2.51.0") || strings.Contains(v, "from 2.50.0") {
		t.Errorf("review should show the fresh version:\n%s", v)
	}
}

func TestInterruptMsgActsLikeCtrlC(t *testing.T) {
	t.Run("idle quits", func(t *testing.T) {
		m, loader := loaded(t, 100)
		next, cmd := m.Update(InterruptMsg{})
		if cmd == nil {
			t.Fatal("interrupt while idle should quit")
		}
		if _, ok := cmd().(tea.QuitMsg); !ok || next.(model).mode != viewList || loader.calls != 1 {
			t.Errorf("interrupt while idle: msg=%T", cmd())
		}
	})
	t.Run("running asks first", func(t *testing.T) {
		started := make(chan struct{})
		exec := blockingExecutor(started)
		m := sessionModel(t, &fakeLoader{results: []fakeResult{{inv: fixtureInventory()}}}, exec, 100)
		m = review(t, stage(t, m, "git"))
		m, execute, _ := confirm(t, m)
		t.Cleanup(m.sess.cancel)
		go execute()
		<-started
		next, cmd := m.Update(InterruptMsg{})
		m = next.(model)
		if cmd != nil || m.mode != viewRunning || !m.sess.isConfirmingCancel || !strings.Contains(m.View(), cancelPrompt) {
			t.Fatalf("interrupt while running: cmd=%v mode=%v confirming=%v", cmd != nil, m.mode, m.sess.isConfirmingCancel)
		}
		if exec.lastCtx().Err() != nil {
			t.Error("interrupt must not cancel without confirmation")
		}
		if !StopRun(m, 5*time.Second) || !errors.Is(exec.lastCtx().Err(), context.Canceled) {
			t.Errorf("StopRun did not cancel and wait: %v", exec.lastCtx().Err())
		}
	})
}
