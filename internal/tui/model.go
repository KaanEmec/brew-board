// Package tui is the Bubble Tea interface of Brew Board: a package list with
// search and filters, a details view, a status bar, and explicit loading,
// stale, empty, and error states, plus the maintenance session (stage,
// review, confirm, execute, receipt). It reads Homebrew only through the
// brew.Loader interface and changes it only through an Executor, and only
// after the user has reviewed a plan and pressed y.
package tui

import (
	"context"
	"maps"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/kaanemec/brew-board/internal/brew"
	"github.com/kaanemec/brew-board/internal/plan"
)

// Messages produced by the load command.
type (
	inventoryLoadedMsg struct{ inv brew.Inventory }
	inventoryFailedMsg struct{ err error }
)

// InterruptMsg reports an external interrupt (SIGINT or SIGTERM) forwarded
// with tea.Program.Send. The model treats it exactly like ctrl+c: it asks
// before cancelling a running command and quits otherwise.
type InterruptMsg struct{}

type viewMode int

const (
	viewList viewMode = iota
	viewDetails
	viewReview  // plan review; nothing has run yet
	viewRunning // executing a confirmed plan
	viewReceipt // session receipt after the post-run refresh
)

// loadPurpose says what happens after the inventory load in flight finishes.
type loadPurpose int

const (
	purposeBrowse  loadPurpose = iota // plain refresh
	purposeReview                     // re-check selections, then open the review
	purposeReceipt                    // post-run refresh, then build the receipt
)

// kindFilter restricts the list to one package kind.
type kindFilter int

const (
	filterAll kindFilter = iota
	filterFormulae
	filterCasks
)

func (f kindFilter) next() kindFilter { return (f + 1) % 3 }

func (f kindFilter) String() string {
	switch f {
	case filterFormulae:
		return "formulae"
	case filterCasks:
		return "casks"
	default:
		return "all"
	}
}

func (f kindFilter) matches(k brew.Kind) bool {
	switch f {
	case filterFormulae:
		return k == brew.KindFormula
	case filterCasks:
		return k == brew.KindCask
	default:
		return true
	}
}

// pkgKey identifies a package across reloads; a formula and a cask may share a name.
type pkgKey struct {
	name string
	kind brew.Kind
}

func keyOf(p brew.Package) pkgKey { return pkgKey{name: p.Name, kind: p.Kind} }

// pkgMeta is derived once per inventory so that search keystrokes and
// filters do not re-fold text or re-measure cell widths.
type pkgMeta struct {
	haystack                string // lower-cased name, display name, and description
	nameWidth, versionWidth int    // terminal cells of the name and versionText
}

// inventoryCounts are the status bar totals of the loaded inventory.
type inventoryCounts struct{ formulae, casks, outdated int }

// Option configures the root model returned by New.
type Option func(*model)

// WithContext sets the parent context for inventory loads. The default is
// context.Background().
func WithContext(ctx context.Context) Option {
	return func(m *model) { m.ctx = ctx }
}

// WithClock sets the clock used when an inventory carries no LoadedAt time.
// Tests use it to keep rendered times deterministic.
func WithClock(now func() time.Time) Option {
	return func(m *model) { m.now = now }
}

// WithExecutor sets the Executor that runs confirmed plans. Without one, the
// review screen refuses to start execution.
func WithExecutor(e Executor) Option {
	return func(m *model) { m.exec = e }
}

// WithHomeDir sets the function that locates the directory receipts are
// saved to. The default is os.UserHomeDir.
func WithHomeDir(dir func() (string, error)) Option {
	return func(m *model) { m.homeDir = dir }
}

// WithSpinner sets the animation shown next to the running item. The
// default is spinner.MiniDot; the README screenshot tool replaces it because
// its renderer's font has no Braille glyphs.
func WithSpinner(s spinner.Spinner) Option {
	return func(m *model) { m.spin.Spinner = s }
}

type model struct {
	loader  brew.Loader
	exec    Executor
	ctx     context.Context
	now     func() time.Time
	homeDir func() (string, error)

	// load holds the cancel func of the in-flight load. It is shared between
	// copies of the model so Init and Update (value receivers) see the same one.
	load *loadState

	// Inventory state. inv is the last good inventory and is kept when a
	// refresh fails; isLoading is true while a load is in flight.
	inv       brew.Inventory
	hasInv    bool
	loadedAt  time.Time
	all       []brew.Package
	meta      []pkgMeta // derived per-package data, aligned with all
	counts    inventoryCounts
	isLoading bool
	err       error
	pending   loadPurpose // what the load in flight is for

	// List state. nameWidth and versionWidth are the widest cells in visible,
	// cached by refilter so View stays independent of the inventory size.
	visible        []brew.Package
	nameWidth      int
	versionWidth   int
	cursor         int
	offset         int
	search         textinput.Model
	isSearching    bool
	kind           kindFilter
	isOutdatedOnly bool

	// selected holds the packages marked for upgrade and removing those
	// marked for removal; a package is in at most one of them. Both are
	// replaced, never mutated, so copies of the model do not share edits.
	selected map[pkgKey]bool
	removing map[pkgKey]bool
	sess     session
	spin     spinner.Model // animates the running item; ticks only while a run is active

	mode          viewMode
	detail        brew.Package
	detailNeeders []string // installed packages that depend on detail, derived when it opens
	detailScroll  int      // first visible line of the details screen
	notice        string   // one-shot status message, cleared on the next key press
	isHelpVisible bool
	helpScroll    int // first visible line of the help overlay

	width, height int
}

// New returns the root Bubble Tea model. Its Init starts the first inventory
// load through loader, which must be non-nil.
func New(loader brew.Loader, opts ...Option) tea.Model {
	ti := textinput.New()
	ti.Prompt = "/ "
	ti.Placeholder = "search name or description"
	ti.CharLimit = 100
	ti.PromptStyle = lipgloss.NewStyle().Foreground(pal.accent).Bold(true)
	ti.PlaceholderStyle = lipgloss.NewStyle().Foreground(pal.dim)
	ti.Cursor.Style = lipgloss.NewStyle().Foreground(pal.accent)

	// Detect the background now, before Bubble Tea owns the terminal, so the
	// adaptive palette never queries it mid-session.
	_ = lipgloss.HasDarkBackground()

	m := model{
		loader:    loader,
		ctx:       context.Background(),
		now:       time.Now,
		homeDir:   os.UserHomeDir,
		load:      &loadState{},
		isLoading: true,
		search:    ti,
		spin:      spinner.New(spinner.WithSpinner(spinner.MiniDot)),
		width:     80,
		height:    24,
	}
	for _, opt := range opts {
		opt(&m)
	}
	return m
}

// Init starts the initial inventory load.
func (m model) Init() tea.Cmd {
	return m.loadCmd()
}

// loadState tracks the cancel func of the load in flight.
type loadState struct {
	mu     sync.Mutex
	cancel context.CancelFunc
}

// begin cancels any previous load and returns the context for a new one.
func (l *loadState) begin(parent context.Context) context.Context {
	ctx, cancel := context.WithCancel(parent)
	l.mu.Lock()
	prev := l.cancel
	l.cancel = cancel
	l.mu.Unlock()
	if prev != nil {
		prev()
	}
	return ctx
}

// stop cancels the load in flight, if any, and releases its context.
func (l *loadState) stop() {
	l.mu.Lock()
	cancel := l.cancel
	l.cancel = nil
	l.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// loadCmd runs the loader off the update loop and reports the result as a
// message. The brew adapter owns per-command timeouts; the TUI only cancels
// the load when the user quits or the load completes.
func (m model) loadCmd() tea.Cmd {
	loader := m.loader
	ctx := m.load.begin(m.ctx)
	return func() tea.Msg {
		inv, err := loader.Load(ctx)
		if err != nil {
			return inventoryFailedMsg{err: err}
		}
		return inventoryLoadedMsg{inv: inv}
	}
}

// quit cancels the in-flight load so no brew process is orphaned.
func (m model) quit() (tea.Model, tea.Cmd) {
	m.load.stop()
	return m, tea.Quit
}

// Update handles messages; it never blocks. A run must stay on screen until
// it finishes: if a run is active but the model is no longer showing it, its
// context is cancelled so the executor stops and returns.
func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	next, cmd := m.update(msg)
	if nm, ok := next.(model); ok && nm.mode != viewRunning && nm.sess.isRunning() {
		nm.sess.cancel()
		nm.sess.isCancelling = true
		next = nm
	}
	return next, cmd
}

func (m model) update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.search.Width = max(10, msg.Width-6)
		m.ensureVisible()
		// Scrolled screens may now fit; keep their offsets in range.
		m.sess.scroll = min(m.sess.scroll, m.maxScroll())
		m.helpScroll = min(m.helpScroll, m.maxScrollOf(len(m.helpLines())))
		m.detailScroll = min(m.detailScroll, m.maxScrollOf(len(m.detailLines())))
		return m, nil
	case inventoryLoadedMsg:
		m.load.stop()
		m.applyInventory(msg.inv)
		return m.afterLoad(nil), nil
	case inventoryFailedMsg:
		m.load.stop()
		m.isLoading = false
		m.err = msg.err
		m.ensureVisible() // the error banner takes a row from the list
		return m.afterLoad(msg.err), nil
	case runEventMsg:
		return m.handleRunEvent(msg)
	case spinner.TickMsg:
		if m.mode != viewRunning || !m.sess.isRunning() {
			return m, nil // the run ended: let the tick chain stop
		}
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd
	case tea.KeyMsg:
		return m.handleKey(msg)
	case InterruptMsg:
		return m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlC})
	}
	if m.isSearching {
		var cmd tea.Cmd
		m.search, cmd = m.search.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m *model) applyInventory(inv brew.Inventory) {
	sel, _ := m.selectedKey()
	m.inv = inv
	m.hasInv = true
	m.isLoading = false
	m.err = nil
	m.loadedAt = inv.LoadedAt
	if m.loadedAt.IsZero() {
		m.loadedAt = m.now()
	}
	// Each kind arrives sorted; interleave them so one alphabetical list covers both.
	m.all = inv.All()
	slices.SortStableFunc(m.all, func(a, b brew.Package) int { return strings.Compare(a.Name, b.Name) })
	m.meta = make([]pkgMeta, len(m.all))
	m.counts = inventoryCounts{}
	for i, p := range m.all {
		m.meta[i] = pkgMeta{
			haystack:     strings.ToLower(p.Name + "\n" + p.DisplayName + "\n" + p.Description),
			nameWidth:    ansi.StringWidth(p.Name),
			versionWidth: ansi.StringWidth(versionText(p)),
		}
		if p.Kind == brew.KindCask {
			m.counts.casks++
		} else {
			m.counts.formulae++
		}
		if p.Outdated {
			m.counts.outdated++
		}
	}
	m.refreshDetail()
	m.refilter(sel)
}

// refreshDetail re-resolves the open details package against the new
// inventory, or returns to the list when it is no longer installed.
func (m *model) refreshDetail() {
	if m.mode != viewDetails {
		return
	}
	want := keyOf(m.detail)
	if i := slices.IndexFunc(m.all, func(p brew.Package) bool { return keyOf(p) == want }); i >= 0 {
		m.openDetail(m.all[i])
		return
	}
	m.mode = viewList
	m.isHelpVisible = false
	m.notice = m.detail.Name + " is no longer installed"
}

// openDetail shows p in the details view with its reverse dependencies.
func (m *model) openDetail(p brew.Package) {
	m.detail = p
	m.detailNeeders = plan.Dependents(m.inv, p)
}

func (m model) selectedKey() (pkgKey, bool) {
	if m.cursor < 0 || m.cursor >= len(m.visible) {
		return pkgKey{}, false
	}
	return keyOf(m.visible[m.cursor]), true
}

// refilter recomputes the visible slice and its column widths and keeps sel
// selected when it is still visible; otherwise the cursor is clamped. It runs
// only when the inventory, a filter, or the search changes, never on
// navigation or render.
func (m *model) refilter(sel pkgKey) {
	query := strings.ToLower(strings.TrimSpace(m.search.Value()))
	m.visible = make([]brew.Package, 0, len(m.all)) // fresh: copies of the model keep their own
	m.nameWidth, m.versionWidth = 0, 0
	for i, p := range m.all {
		if !m.kind.matches(p.Kind) || (m.isOutdatedOnly && !p.Outdated) {
			continue
		}
		if query != "" && !strings.Contains(m.meta[i].haystack, query) {
			continue
		}
		m.visible = append(m.visible, p)
		m.nameWidth = max(m.nameWidth, m.meta[i].nameWidth)
		m.versionWidth = max(m.versionWidth, m.meta[i].versionWidth)
	}
	if i := slices.IndexFunc(m.visible, func(p brew.Package) bool { return keyOf(p) == sel }); i >= 0 {
		m.cursor = i
	}
	m.cursor = min(m.cursor, len(m.visible)-1)
	m.cursor = max(m.cursor, 0)
	m.ensureVisible()
}

// ensureVisible scrolls so the cursor row is on screen.
func (m *model) ensureVisible() {
	h := m.listHeight()
	if m.cursor < m.offset {
		m.offset = m.cursor
	}
	if m.cursor >= m.offset+h {
		m.offset = m.cursor - h + 1
	}
	m.offset = max(0, min(m.offset, len(m.visible)-h))
}

func (m *model) move(delta int) {
	if len(m.visible) == 0 {
		return
	}
	m.cursor = max(0, min(m.cursor+delta, len(m.visible)-1))
	m.ensureVisible()
}

func (m model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	m.notice = ""
	if m.mode == viewRunning {
		return m.handleRunKey(msg) // ctrl+c asks to cancel instead of quitting
	}
	if msg.Type == tea.KeyCtrlC {
		return m.quit()
	}
	if m.isHelpVisible {
		if d, ok := m.scrollDelta(msg); ok {
			m.helpScroll = clampScroll(m.helpScroll+d, m.maxScrollOf(len(m.helpLines())))
			return m, nil
		}
		switch msg.String() {
		case "?", "esc", "q":
			m.isHelpVisible = false
		}
		return m, nil
	}
	switch m.mode {
	case viewReview:
		return m.handleReviewKey(msg)
	case viewReceipt:
		return m.handleReceiptKey(msg)
	}
	if m.mode == viewDetails {
		if d, ok := m.scrollDelta(msg); ok {
			m.detailScroll = clampScroll(m.detailScroll+d, m.maxScrollOf(len(m.detailLines())))
			return m, nil
		}
		switch msg.String() {
		case "esc", "q", "backspace", "left", "h":
			m.mode = viewList
		case "?":
			m.openHelp()
		}
		return m, nil
	}
	if m.isSearching {
		return m.handleSearchKey(msg)
	}
	return m.handleListKey(msg)
}

func (m model) handleSearchKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	sel, _ := m.selectedKey()
	switch msg.Type {
	case tea.KeyEsc:
		m.search.SetValue("")
		m.search.Blur()
		m.isSearching = false
		m.refilter(sel)
		return m, nil
	case tea.KeyEnter:
		m.search.Blur()
		m.isSearching = false
		return m, nil
	case tea.KeyUp:
		m.move(-1)
		return m, nil
	case tea.KeyDown:
		m.move(1)
		return m, nil
	}
	var cmd tea.Cmd
	m.search, cmd = m.search.Update(msg)
	m.refilter(sel)
	return m, cmd
}

func (m model) handleListKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	sel, _ := m.selectedKey()
	switch msg.String() {
	case "q":
		return m.quit()
	case "j", "down":
		m.move(1)
	case "k", "up":
		m.move(-1)
	case "g", "home":
		m.move(-len(m.visible))
	case "G", "end":
		m.move(len(m.visible))
	case "pgdown", "ctrl+d":
		m.move(m.listHeight())
	case "pgup", "ctrl+u":
		m.move(-m.listHeight())
	case "/":
		if !m.hasInv {
			return m, nil
		}
		m.isSearching = true
		m.ensureVisible()
		return m, m.search.Focus()
	case "esc":
		if m.search.Value() != "" {
			m.search.SetValue("")
			m.refilter(sel)
		}
	case "f":
		m.kind = m.kind.next()
		m.refilter(sel)
	case "o":
		m.isOutdatedOnly = !m.isOutdatedOnly
		m.refilter(sel)
	case "r":
		if m.isLoading {
			return m, nil // one load in flight at a time
		}
		m.isLoading = true
		m.err = nil
		m.ensureVisible() // the stale banner takes a row from the list
		return m, m.loadCmd()
	case "enter":
		if len(m.visible) > 0 {
			m.openDetail(m.visible[m.cursor])
			m.detailScroll = 0
			m.mode = viewDetails
		}
	case " ":
		m.toggleSelected()
	case "d":
		m.toggleRemoval()
	case "a":
		m.toggleAllVisible()
	case "c":
		return m.startReview()
	case "?":
		m.openHelp()
	}
	return m, nil
}

func (m *model) openHelp() {
	m.isHelpVisible = true
	m.helpScroll = 0
}

// keyHelp is the help overlay, one row per group of bindings. keys lists
// every key string (as tea.KeyMsg.String reports it) that the row documents;
// TestHelpListsEveryBinding checks it against the case labels of the key
// handlers, so a new binding needs a row here.
var keyHelp = []struct {
	label, desc string
	keys        []string
}{
	{"j / k, ↓ / ↑", "move selection; scroll the other screens", []string{"j", "k", "down", "up"}},
	{"g / G, home / end", "first / last package", []string{"g", "G", "home", "end"}},
	{"pgdn / pgup, ctrl+d / ctrl+u", "page down / up", []string{"pgdown", "pgup", "ctrl+d", "ctrl+u"}},
	{"/", "search; enter keeps it, esc clears it, ↑ / ↓ move", []string{"/", "enter", "esc", "up", "down"}},
	{"f", "cycle type: all → formulae → casks", []string{"f"}},
	{"o", "toggle outdated-only", []string{"o"}},
	{"r", "refresh inventory (read-only)", []string{"r"}},
	{"enter", "package details", []string{"enter"}},
	{"esc / q (details)", "back to list (also h, ←, backspace)", []string{"esc", "q", "h", "left", "backspace"}},
	{"space / d / a (list)", "mark upgrade / mark removal / all visible outdated", []string{" ", "d", "a"}},
	{"c", "continue: re-check the marks and open the review", []string{"c"}},
	{"y / esc (review)", "run the reviewed commands / back, selections kept", []string{"y", "esc"}},
	{"x, ctrl+c (running)", "ask to cancel the run; then y cancels, n keeps it running", []string{"x", "ctrl+c", "y", "n", "esc"}},
	{"s / c / a (receipt)", "save receipt / review cleanup / review autoremove", []string{"s", "c", "a"}},
	{"r (receipt)", "retry the refresh after the run when it failed", []string{"r"}},
	{"esc / enter (receipt)", "back to list, selections cleared", []string{"esc", "enter"}},
	{"?", "toggle this help", []string{"?"}},
	{"q / ctrl+c", "quit", []string{"q", "ctrl+c"}},
}

// unstageableReason explains why p cannot be staged for upgrade, or returns
// "" when it can. It mirrors the rules plan.Build enforces.
func unstageableReason(p brew.Package) string {
	switch {
	case p.Pinned:
		return "pinned"
	case !p.Outdated:
		return "already up to date"
	}
	return ""
}

// setMarks returns a copy of marks with keys added (on) or removed.
func setMarks(marks map[pkgKey]bool, keys []pkgKey, on bool) map[pkgKey]bool {
	out := maps.Clone(marks)
	if out == nil {
		out = map[pkgKey]bool{}
	}
	for _, k := range keys {
		if on {
			out[k] = true
		} else {
			delete(out, k)
		}
	}
	return out
}

// setSelected marks keys for upgrade (on), which clears any removal mark,
// or clears their upgrade mark.
func (m *model) setSelected(keys []pkgKey, on bool) {
	m.selected = setMarks(m.selected, keys, on)
	if on {
		m.removing = setMarks(m.removing, keys, false)
	}
}

// setRemoving marks keys for removal (on), which clears any upgrade mark,
// or clears their removal mark.
func (m *model) setRemoving(keys []pkgKey, on bool) {
	m.removing = setMarks(m.removing, keys, on)
	if on {
		m.selected = setMarks(m.selected, keys, false)
	}
}

// unmark clears both marks of keys.
func (m *model) unmark(keys ...pkgKey) {
	m.selected = setMarks(m.selected, keys, false)
	m.removing = setMarks(m.removing, keys, false)
}

// markOf is the operation p is marked for, or "" when it is unmarked.
func (m model) markOf(p brew.Package) plan.Op {
	k := keyOf(p)
	switch {
	case m.removing[k]:
		return plan.OpUninstall
	case m.selected[k]:
		return plan.OpUpgrade
	}
	return ""
}

func (m *model) toggleSelected() {
	if m.pending == purposeReview {
		m.notice = "checking selections…"
		return
	}
	if len(m.visible) == 0 {
		return
	}
	p := m.visible[m.cursor]
	k := keyOf(p)
	if m.selected[k] {
		m.setSelected([]pkgKey{k}, false) // deselecting is always allowed
		return
	}
	switch unstageableReason(p) {
	case "pinned":
		m.notice = p.Name + " is pinned; brew unpin " + p.Name + " to allow upgrades"
	case "already up to date":
		m.notice = p.Name + " is up to date; only outdated packages can be selected"
	default:
		m.setSelected([]pkgKey{k}, true)
	}
}

// toggleRemoval marks the highlighted package for removal, or clears its
// removal mark. Any installed package that is not pinned can be marked; the
// review enforces the dependency rules. A notice names dependents that are
// not marked yet.
func (m *model) toggleRemoval() {
	if m.pending == purposeReview {
		m.notice = "checking selections…"
		return
	}
	if len(m.visible) == 0 {
		return
	}
	p := m.visible[m.cursor]
	k := keyOf(p)
	if m.removing[k] {
		m.setRemoving([]pkgKey{k}, false)
		return
	}
	if p.Pinned {
		m.notice = p.Name + " is pinned; brew unpin " + p.Name + " to allow removal"
		return
	}
	m.setRemoving([]pkgKey{k}, true)
	// Dependents among the packages not marked for removal, matched by name
	// and kind.
	rest := m.inv
	marked := func(q brew.Package) bool { return m.removing[keyOf(q)] }
	rest.Formulae = slices.DeleteFunc(slices.Clone(m.inv.Formulae), marked)
	rest.Casks = slices.DeleteFunc(slices.Clone(m.inv.Casks), marked)
	if unmarked := plan.Dependents(rest, p); len(unmarked) > 0 {
		m.notice = p.Name + " is required by " + strings.Join(unmarked, ", ") + "; mark them too or the review drops it"
	}
}

// toggleAllVisible marks every visible outdated, unpinned package for
// upgrade, or clears them all when they are already marked. Packages marked
// for removal keep that mark.
func (m *model) toggleAllVisible() {
	if m.pending == purposeReview {
		m.notice = "checking selections…"
		return
	}
	var keys []pkgKey
	isAllSelected := true
	for _, p := range m.visible {
		if unstageableReason(p) != "" || m.removing[keyOf(p)] {
			continue
		}
		keys = append(keys, keyOf(p))
		isAllSelected = isAllSelected && m.selected[keyOf(p)]
	}
	if len(keys) == 0 {
		m.notice = "no visible outdated, unpinned packages to select"
		return
	}
	m.setSelected(keys, !isAllSelected)
}
