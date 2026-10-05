package tui

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/kaanemec/brew-board/internal/brew"
	"github.com/kaanemec/brew-board/internal/plan"
	"github.com/kaanemec/brew-board/internal/receipt"
	"github.com/kaanemec/brew-board/internal/run"
)

const (
	// eventBuffer is the capacity of the channel an execution streams into.
	eventBuffer = 256
	// maxOutputLines bounds the output kept in memory for the output pane.
	maxOutputLines = 2000
	// cancelPrompt is shown before interrupting a running Homebrew command;
	// cancelDetail says what each answer does.
	cancelPrompt = "Cancel the run? Partial work may remain. y/n"
	cancelDetail = "y interrupts the running Homebrew command and skips the remaining ones · n keeps it running"
	// dependencyNote is shown on every review screen.
	dependencyNote = "Homebrew may also upgrade dependencies or do related work of its own."
	// removalWarning is shown on a review screen that removes packages.
	removalWarning = "Removal deletes the package's files; Brew Board never passes --force or --ignore-dependencies."
)

// Executor runs a confirmed plan; run.Executor implements it.
//
// Execute must send events in order with Finished last and must not send
// after it returns. The TUI owns the events channel: it buffers it, keeps
// receiving until Finished arrives, and closes it once Execute returns, so
// blocking sends never strand the executor.
type Executor interface {
	Execute(ctx context.Context, p plan.Plan, events chan<- run.Event) []run.Result
}

// runEventMsg carries one execution event; isClosed reports that Execute
// returned and the channel was closed.
type runEventMsg struct {
	ev       run.Event
	isClosed bool
}

// session is the state of one maintenance session, from review to receipt.
type session struct {
	plan plan.Plan
	// Review.
	staged      []plan.Selection // selection snapshot taken when u was pressed
	problems    []string         // selections dropped from the plan, with the reason
	isFollowUp  bool             // cleanup or autoremove review opened from a receipt; esc returns there
	receiptPlan plan.Plan        // the receipt's plan while its follow-up is reviewed
	scroll      int              // first visible body line of the review or receipt screen

	// Execution.
	before             brew.Inventory // inventory at confirmation
	results            []run.Result
	current            int // index of the running item, -1 before the first
	output             []string
	events             <-chan run.Event
	cancel             context.CancelFunc
	done               chan struct{} // closed once Execute has returned
	isConfirmingCancel bool
	isCancelling       bool
	isFinished         bool

	// Receipt.
	receipt    receipt.Receipt
	orphans    []string // dependency-only formulae nothing installed needs after the run
	refreshErr error    // post-run refresh failed; versions are not verified
	savedPath  string
	saveErr    error
}

// isRunning reports that an execution started and has not finished.
func (s session) isRunning() bool { return s.cancel != nil && !s.isFinished }

// loadBusyNotice refuses session actions while a load is in flight.
const loadBusyNotice = "wait for the refresh to finish"

// afterLoad finishes whatever the completed load was started for. The
// inventory is already applied; it never changes mode while a run is active,
// and a receipt load only opens the receipt it was started for.
func (m model) afterLoad(err error) model {
	purpose := m.pending
	m.pending = purposeBrowse
	if m.sess.isRunning() {
		return m
	}
	switch purpose {
	case purposeReview:
		if err != nil {
			m.sess = session{}
			m.notice = "could not check selections; press u to retry"
			return m
		}
		return m.openReview()
	case purposeReceipt:
		if !m.sess.isFinished || (m.mode != viewRunning && m.mode != viewReceipt) {
			return m // the receipt was dismissed or replaced; keep the inventory only
		}
		return m.openReceipt(err)
	}
	return m
}

// startReview re-checks the selection against a fresh inventory before the
// review screen opens.
func (m model) startReview() (tea.Model, tea.Cmd) {
	switch {
	case len(m.selected) == 0 && len(m.removing) == 0:
		m.notice = "mark packages with space (upgrade) or d (remove)"
		return m, nil
	case m.isLoading:
		m.notice = "wait for the refresh to finish, then press u"
		return m, nil
	}
	m.sess = session{}
	for _, p := range m.all {
		k := keyOf(p)
		switch {
		case m.selected[k] && m.removing[k]:
			// The key handlers keep the marks exclusive; never guess which one was meant.
			m.sess.problems = append(m.sess.problems, p.Name+": marked for both upgrade and removal — removed from plan")
			m.unmark(k)
		case m.removing[k]:
			m.sess.staged = append(m.sess.staged, plan.Selection{Package: p, Op: plan.OpUninstall})
		case m.selected[k]:
			m.sess.staged = append(m.sess.staged, plan.Selection{Package: p, Op: plan.OpUpgrade})
		}
	}
	for _, marks := range []map[pkgKey]bool{m.selected, m.removing} {
		for k := range marks {
			if !slices.ContainsFunc(m.all, func(p brew.Package) bool { return keyOf(p) == k }) {
				m.sess.problems = append(m.sess.problems, k.name+": no longer installed — removed from plan")
				m.unmark(k)
			}
		}
	}
	slices.Sort(m.sess.problems)
	m.pending = purposeReview
	m.isLoading = true
	m.err = nil
	m.ensureVisible()
	return m, m.loadCmd()
}

// openReview builds the plan from the staged snapshot, validates it against
// the inventory just loaded, and drops blocked or stale items from plan and
// marks. Dropping an item can block another (a removal whose dependent was
// dropped), so validation repeats until the plan is clean. Kept items then
// carry the freshly loaded package, so the review shows the versions
// installed now rather than at staging.
func (m model) openReview() model {
	now := m.now()
	var kept []plan.Selection
	for _, sel := range m.sess.staged {
		switch {
		case sel.Op == plan.OpUpgrade:
			if reason := unstageableReason(sel.Package); reason != "" {
				m.dropStale(sel.Package, reason)
				continue
			}
		case sel.Package.Pinned: // plan.Build would reject the whole plan
			m.dropStale(sel.Package, plan.ErrPinned.Error())
			continue
		}
		kept = append(kept, sel)
	}
	p, err := plan.Build(kept, m.inv, now)
	if err != nil && !errors.Is(err, plan.ErrEmpty) {
		m.sess.problems = append(m.sess.problems, "cannot build plan: "+err.Error())
		p = plan.Plan{CreatedAt: now}
	}
	for range len(p.Items) + 1 { // each pass drops at least one item or ends
		problems := p.Validate(m.inv)
		if len(problems) == 0 {
			break
		}
		blocked := map[pkgKey]bool{}
		for _, pr := range problems {
			m.dropStale(pr.Item.Package, pr.Reason)
			blocked[keyOf(pr.Item.Package)] = true
		}
		p.Items = slices.DeleteFunc(slices.Clone(p.Items), func(it plan.Item) bool { return blocked[keyOf(it.Package)] })
	}
	for i, it := range p.Items {
		k := keyOf(it.Package)
		if j := slices.IndexFunc(m.all, func(fresh brew.Package) bool { return keyOf(fresh) == k }); j >= 0 {
			p.Items[i].Package = m.all[j]
		}
	}

	m.sess.plan = p
	m.mode = viewReview
	m.isHelpVisible = false
	m.isSearching = false
	m.search.Blur()
	return m
}

func (m *model) dropStale(p brew.Package, reason string) {
	m.sess.problems = append(m.sess.problems, p.Name+": "+reason+" — removed from plan")
	m.unmark(keyOf(p))
}

// scrollDelta maps a scroll key to a line offset and reports whether msg was
// a scroll key.
func (m model) scrollDelta(msg tea.KeyMsg) (int, bool) {
	page := max(m.bodyRoom()-1, 1)
	switch msg.String() {
	case "j", "down":
		return 1, true
	case "k", "up":
		return -1, true
	case "pgdown", "ctrl+d":
		return page, true
	case "pgup", "ctrl+u":
		return -page, true
	}
	return 0, false
}

func clampScroll(v, maxV int) int { return max(0, min(v, maxV)) }

// scrollKey scrolls the review or receipt screen and reports whether msg was
// a scroll key.
func (m *model) scrollKey(msg tea.KeyMsg) bool {
	d, ok := m.scrollDelta(msg)
	if ok {
		m.sess.scroll = clampScroll(m.sess.scroll+d, m.maxScroll())
	}
	return ok
}

func (m model) handleReviewKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.scrollKey(msg) {
		return m, nil
	}
	switch msg.String() {
	case "esc":
		if m.sess.isFollowUp {
			m.sess.plan = m.sess.receiptPlan
			m.sess.isFollowUp = false
			m.sess.scroll = 0
			m.mode = viewReceipt
			return m, nil
		}
		m.sess = session{}
		m.mode = viewList
	case "y":
		return m.startExecution()
	}
	return m, nil
}

// startExecution runs the reviewed plan. It is reachable only from the
// review screen, so a second execution cannot start while one runs.
func (m model) startExecution() (tea.Model, tea.Cmd) {
	if len(m.sess.plan.Items) == 0 {
		return m, nil
	}
	if m.exec == nil {
		m.notice = "this build cannot run Homebrew commands"
		return m, nil
	}
	if m.isLoading {
		m.notice = loadBusyNotice // a landing load must not reach a running session
		return m, nil
	}
	ctx, cancel := context.WithCancel(m.ctx)
	events := make(chan run.Event, eventBuffer)
	done := make(chan struct{})
	p := m.sess.plan

	m.sess = session{plan: p, before: m.inv, events: events, cancel: cancel, done: done, current: -1}
	m.sess.results = make([]run.Result, len(p.Items))
	for i, it := range p.Items {
		m.sess.results[i] = run.Result{Item: it, Args: it.Args(), Status: run.StatusPending}
	}
	m.mode = viewRunning

	exec := m.exec
	execute := func() tea.Msg {
		exec.Execute(ctx, p, events)
		close(events) // Execute has returned, so nothing sends any more
		close(done)
		return nil
	}
	// The spinner ticks only while this run is on screen; see the
	// spinner.TickMsg case in update.
	return m, tea.Batch(execute, waitForEvent(events), m.spin.Tick)
}

// waitForEvent blocks until the next execution event. The model re-issues it
// after every event until Finished, so the channel is always drained.
func waitForEvent(events <-chan run.Event) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-events
		return runEventMsg{ev: ev, isClosed: !ok}
	}
}

func (m model) handleRunEvent(msg runEventMsg) (tea.Model, tea.Cmd) {
	if m.mode != viewRunning || m.sess.isFinished {
		if m.sess.isRunning() && !msg.isClosed {
			// The run left the screen (Update has cancelled it); keep
			// draining so the executor can return.
			return m, waitForEvent(m.sess.events)
		}
		return m, nil
	}
	switch ev := msg.ev.(type) {
	case run.ItemStarted:
		if m.setResultStatus(ev.Index, run.StatusRunning) {
			m.sess.current = ev.Index
			m.appendOutput("$ " + m.sess.plan.Items[ev.Index].Command())
		}
	case run.OutputLine:
		m.appendOutput(ev.Line)
	case run.ItemFinished:
		if ev.Index >= 0 && ev.Index < len(m.sess.results) {
			m.sess.results = slices.Clone(m.sess.results)
			m.sess.results[ev.Index] = ev.Result
		}
	case run.Finished:
		m.sess.results = slices.Clone(ev.Results)
		return m.finishExecution()
	}
	if msg.isClosed {
		return m.finishExecution() // Execute returned without Finished; keep what we saw
	}
	return m, waitForEvent(m.sess.events)
}

func (m *model) setResultStatus(i int, st run.Status) bool {
	if i < 0 || i >= len(m.sess.results) {
		return false
	}
	m.sess.results = slices.Clone(m.sess.results)
	m.sess.results[i].Status = st
	return true
}

// appendOutput keeps the last maxOutputLines lines, made safe to render.
func (m *model) appendOutput(line string) {
	if i := strings.LastIndexByte(line, '\r'); i >= 0 {
		line = line[i+1:] // progress bars redraw with \r; keep the final state
	}
	line = strings.ReplaceAll(ansi.Strip(line), "\t", "    ")
	// ansi.Strip keeps C0 controls such as \b or BEL; they would move the
	// cursor or beep instead of printing, so drop them.
	line = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, line)
	out := m.sess.output
	if len(out) >= maxOutputLines {
		out = out[len(out)-maxOutputLines+1:]
	}
	m.sess.output = append(out, line) // re-slicing the front keeps the backing array bounded
}

// finishExecution starts the post-run refresh; the receipt follows it.
func (m model) finishExecution() (tea.Model, tea.Cmd) {
	m.sess.isFinished = true
	m.sess.isConfirmingCancel = false
	m.sess.cancel()
	m.pending = purposeReceipt
	m.isLoading = true
	m.err = nil
	return m, m.loadCmd()
}

func (m model) handleRunKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	s := &m.sess
	if s.isFinished {
		if msg.Type == tea.KeyCtrlC {
			return m.quit() // nothing runs any more; only the refresh is in flight
		}
		return m, nil
	}
	if s.isConfirmingCancel {
		switch msg.String() {
		case "y":
			s.isConfirmingCancel = false
			s.isCancelling = true
			s.cancel() // the executor interrupts brew with SIGINT
		case "n", "esc":
			s.isConfirmingCancel = false
		}
		return m, nil
	}
	switch msg.String() {
	case "x", "ctrl+c":
		if !s.isCancelling {
			s.isConfirmingCancel = true
		}
	}
	return m, nil
}

// openReceipt reconciles the run with the inventory just loaded. When the
// refresh failed, the last good inventory stands in and the receipt is
// marked unverified; a successful retry rebuilds it verified.
func (m model) openReceipt(refreshErr error) model {
	m.sess.receipt = receipt.Build(m.sess.plan, m.sess.results, m.sess.before, m.inv, m.now())
	if refreshErr != nil {
		title, _ := describeError(refreshErr)
		m.sess.receipt = m.sess.receipt.Unverified(title)
	}
	m.sess.refreshErr = refreshErr
	m.sess.orphans = m.orphans()
	m.sess.savedPath, m.sess.saveErr = "", nil
	m.sess.scroll = 0
	m.mode = viewReceipt
	return m
}

func (m model) handleReceiptKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.scrollKey(msg) {
		return m, nil
	}
	switch msg.String() {
	case "s":
		m.saveReceipt()
	case "c":
		if slices.ContainsFunc(m.sess.plan.Items, func(it plan.Item) bool { return it.Op == plan.OpCleanup }) {
			m.notice = "cleanup already ran in this session"
			return m, nil
		}
		m.offerFollowUp(plan.Plan{CreatedAt: m.now()}.WithCleanup())
	case "a":
		if len(m.sess.orphans) == 0 {
			m.notice = "no dependency-only formulae are unneeded now"
			return m, nil
		}
		m.offerFollowUp(plan.Plan{CreatedAt: m.now()}.WithAutoremove())
	case "r":
		if m.sess.refreshErr == nil || m.isLoading {
			return m, nil
		}
		m.pending = purposeReceipt
		m.isLoading = true
		return m, m.loadCmd()
	case "esc", "enter":
		if m.pending == purposeReceipt {
			m.pending = purposeBrowse // a retry in flight now only refreshes the list
		}
		m.sess = session{}
		m.selected, m.removing = nil, nil
		m.mode = viewList
	}
	return m, nil
}

// offerFollowUp opens the review of a one-item plan offered on the receipt
// (cleanup or autoremove); esc returns to the receipt. It runs only after y.
func (m *model) offerFollowUp(p plan.Plan) {
	if m.isLoading {
		m.notice = loadBusyNotice
		return
	}
	m.sess.receiptPlan = m.sess.plan
	m.sess.plan = p
	m.sess.problems = nil
	m.sess.scroll = 0
	m.sess.isFollowUp = true
	m.mode = viewReview
}

// orphans lists the formulae that are, after the run, installed only as
// dependencies that nothing installed needs: what brew autoremove would
// consider now, whatever the run did. It reads the post-run inventory, so a
// removal that reported success but left the package installed changes
// nothing, and dependency-only formulae unneeded before the run are listed
// too. Homebrew decides the final list.
func (m model) orphans() []string {
	return plan.Orphans(m.inv, plan.Plan{})
}

// StopRun cancels a run still active in final, the model returned by
// tea.Program.Run, and waits up to wait for its executor to return. It also
// cancels any inventory load in flight. It reports whether nothing is left
// running; models not created by New report true.
func StopRun(final tea.Model, wait time.Duration) bool {
	m, ok := final.(model)
	if !ok {
		return true
	}
	if m.load != nil {
		m.load.stop()
	}
	if m.sess.done == nil {
		return true
	}
	m.sess.cancel() // the executor interrupts brew with SIGINT
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-m.sess.done:
		return true
	case <-t.C:
		return false
	}
}

func (m *model) saveReceipt() {
	if m.sess.savedPath != "" {
		m.notice = "already saved to " + m.sess.savedPath
		return
	}
	dir, err := m.homeDir()
	if err != nil {
		m.sess.saveErr = fmt.Errorf("find home directory: %w", err)
		return
	}
	m.sess.savedPath, m.sess.saveErr = m.sess.receipt.Save(dir)
}
