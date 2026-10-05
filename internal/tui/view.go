package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/kaanemec/brew-board/internal/brew"
	"github.com/kaanemec/brew-board/internal/run"
)

const (
	missing      = "—"
	timeLayout   = "2006-01-02 15:04:05"
	kindWidth    = 7 // len("formula")
	pinWidth     = 3 // len("pin")
	cursorMarker = "> "
	outdatedMark = "↑ "
	checkWidth   = 4  // len("[x] ")
	tightWidth   = 50 // below this the check column becomes a "*" marker
)

// Styles degrade through lipgloss: with NO_COLOR or a dumb terminal they render
// as plain text, so every state also carries a textual marker.
var (
	titleStyle    = lipgloss.NewStyle().Bold(true)
	dimStyle      = lipgloss.NewStyle().Faint(true)
	selectedStyle = lipgloss.NewStyle().Reverse(true)
	outdatedStyle = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#B45309", Dark: "#FBBF24"})
	errorStyle    = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#B91C1C", Dark: "#F87171"})
	staleStyle    = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#1D4ED8", Dark: "#93C5FD"})
)

// View renders the current screen. Every line is truncated to the terminal
// width by cell width, so wide runes and long output never wrap.
func (m model) View() string {
	var body []string
	switch {
	case m.isHelpVisible:
		body = scrolled(m.helpLines(), m.helpScroll)
	case m.mode == viewDetails:
		body = scrolled(m.detailLines(), m.detailScroll)
	case m.mode == viewReview, m.mode == viewReceipt:
		body = scrolled(m.sessionLines(), m.sess.scroll)
	case m.mode == viewRunning:
		body = m.runLines()
	default:
		body = m.listLines()
	}
	lines := append([]string{m.headerLine()}, body...)

	// Pin the status bar, hints, and any footer notice to the bottom.
	pinned := m.pinnedLines()
	room := m.height - 2 - len(pinned)
	if len(lines) > room {
		lines = lines[:max(room, 1)]
	}
	for len(lines) < room {
		lines = append(lines, "")
	}
	lines = append(lines, m.statusLine(), m.hintLine())
	lines = append(lines, pinned...)
	for i, l := range lines {
		lines[i] = ansi.Truncate(l, m.width, "…")
	}
	return strings.Join(lines, "\n")
}

func scrolled(lines []string, offset int) []string {
	return lines[min(max(offset, 0), len(lines)):]
}

// bodyRoom is the number of rows between the header and the footer.
func (m model) bodyRoom() int {
	return max(m.height-3-len(m.pinnedLines()), 1) // header, status, hints
}

// maxScrollOf is the largest useful scroll offset of a body of n lines.
func (m model) maxScrollOf(n int) int {
	return max(n-m.bodyRoom(), 0)
}

// pinnedLines are shown under the footer, outside the scrolled body, so the
// outcome of saving a receipt stays visible wherever the receipt is scrolled.
func (m model) pinnedLines() []string {
	if m.mode != viewReceipt || m.isHelpVisible {
		return nil
	}
	switch {
	case m.sess.saveErr != nil:
		return m.wrapLines(errorStyle.Render("[error] Could not save the receipt: "+m.sess.saveErr.Error()+" · s retry"), "")
	case m.sess.savedPath != "":
		return m.wrapLines(staleStyle.Render("Saved to "+m.sess.savedPath), "")
	}
	return nil
}

func (m model) headerLine() string {
	label := " · read-only"
	switch m.mode {
	case viewRunning:
		label = " · running Homebrew"
	case viewReceipt:
		label = " · session receipt"
	}
	h := titleStyle.Render("Brew Board") + dimStyle.Render(label)
	if m.hasInv && m.inv.BrewVersion != "" {
		h += dimStyle.Render(" · " + m.inv.BrewVersion)
	}
	return h
}

// bannerLine is the one-line notice shown above a list that is not fresh.
func (m model) bannerLine() (string, bool) {
	if !m.hasInv {
		return "", false
	}
	at := m.loadedAt.Format(timeLayout)
	switch {
	case m.isLoading:
		return staleStyle.Render("[stale, refreshing…] showing inventory loaded at " + at), true
	case m.err != nil:
		title, _ := describeError(m.err)
		return errorStyle.Render("[error] Refresh failed: " + title + ". Showing inventory loaded at " + at + " · r retry"), true
	}
	return "", false
}

func (m model) isSearchLineVisible() bool {
	return m.hasInv && (m.isSearching || m.search.Value() != "")
}

// listHeight is the number of package rows that fit; it mirrors listLines.
func (m model) listHeight() int {
	h := m.height - 4 // header, column header, status, hints
	if _, ok := m.bannerLine(); ok {
		h--
	}
	if m.isSearchLineVisible() {
		h--
	}
	return max(h, 1)
}

func (m model) listLines() []string {
	if !m.hasInv {
		if m.isLoading {
			return []string{"", "Loading Homebrew inventory…", "", dimStyle.Render("Running brew info and brew outdated (read-only).")}
		}
		return m.errorLines()
	}

	var lines []string
	if b, ok := m.bannerLine(); ok {
		lines = append(lines, b)
	}
	if m.isSearchLineVisible() {
		lines = append(lines, m.search.View())
	}

	if len(m.all) == 0 {
		return append(lines,
			"",
			"Homebrew is working, but no formulae or casks are installed.",
			dimStyle.Render("Install something with brew, then press r to refresh."),
		)
	}

	cols := m.columns()
	lines = append(lines, dimStyle.Render(cols.header()))
	if len(m.visible) == 0 {
		return append(lines,
			"",
			"No packages match "+m.filterSummary()+".",
			dimStyle.Render("esc clears the search · f changes type · o toggles outdated-only"),
		)
	}
	end := min(m.offset+m.listHeight(), len(m.visible))
	for i := m.offset; i < end; i++ {
		p := m.visible[i]
		lines = append(lines, cols.row(p, i == m.cursor, m.selected[keyOf(p)]))
	}
	return lines
}

func (m model) errorLines() []string {
	title, detail := describeError(m.err)
	wrap := lipgloss.NewStyle().Width(max(m.width-2, 20))
	lines := []string{"", errorStyle.Bold(true).Render("[error] " + title), ""}
	lines = append(lines, strings.Split(wrap.Render(detail), "\n")...)
	return append(lines, "", dimStyle.Render("r retry · q quit"))
}

// columns describes the list layout for the current width. Description and
// kind columns are dropped as the terminal narrows.
type columns struct {
	name, version, desc int
	hasKind, hasCheck   bool
}

func (m model) columns() columns {
	w := m.width
	c := columns{hasKind: w >= 72, hasCheck: w >= tightWidth}

	maxName, maxVersion := max(len("NAME"), m.nameWidth), max(len("VERSION"), m.versionWidth)

	// cursor + marker + name + " " + [kind + " "] + version + " " + pin
	fixed := len(cursorMarker) + len("  ") + 1 + 1 + pinWidth
	if c.hasCheck {
		fixed += checkWidth
	}
	if c.hasKind {
		fixed += kindWidth + 1
	}
	versionCap := 22
	if w >= 100 {
		versionCap = 30
	}
	c.version = min(maxVersion, versionCap, max((w-fixed)/2, 4))

	rest := w - fixed - c.version
	if w >= 100 {
		c.name = min(maxName, 32)
		c.desc = rest - c.name - 1
		if c.desc < 10 {
			c.desc = 0
		}
	}
	if c.desc == 0 {
		c.name = min(maxName, rest)
	}
	c.name = max(c.name, 4)
	return c
}

func (c columns) header() string {
	var b strings.Builder
	b.WriteString(strings.Repeat(" ", len(cursorMarker)+2))
	if c.hasCheck {
		b.WriteString(strings.Repeat(" ", checkWidth))
	}
	b.WriteString(fit("NAME", c.name) + " ")
	if c.hasKind {
		b.WriteString(fit("KIND", kindWidth) + " ")
	}
	b.WriteString(fit("VERSION", c.version) + " " + fit("PIN", pinWidth))
	if c.desc > 0 {
		b.WriteString(" " + fit("DESCRIPTION", c.desc))
	}
	return b.String()
}

// row renders one package. isCursor marks the highlighted row; isStaged marks
// a package selected for the maintenance session.
func (c columns) row(p brew.Package, isCursor, isStaged bool) string {
	var b strings.Builder
	if isCursor {
		b.WriteString(cursorMarker)
	} else {
		b.WriteString(strings.Repeat(" ", len(cursorMarker)))
	}
	marker := "  "
	if p.Outdated {
		marker = outdatedMark
		if !isCursor {
			marker = outdatedStyle.Render(marker)
		}
	}
	if isStaged && !c.hasCheck {
		marker = "* "
	}
	b.WriteString(marker)
	if c.hasCheck {
		// Boxes only on rows that can be (or are) staged, to keep the list calm.
		switch {
		case isStaged:
			b.WriteString("[x] ")
		case unstageableReason(p) == "":
			b.WriteString("[ ] ")
		default:
			b.WriteString(strings.Repeat(" ", checkWidth))
		}
	}
	b.WriteString(fit(p.Name, c.name) + " ")
	if c.hasKind {
		b.WriteString(fit(string(p.Kind), kindWidth) + " ")
	}
	pin := ""
	if p.Pinned {
		pin = "pin"
	}
	b.WriteString(fit(versionText(p), c.version) + " " + fit(pin, pinWidth))
	if c.desc > 0 {
		b.WriteString(" " + fit(p.Description, c.desc))
	}
	if isCursor {
		return selectedStyle.Render(b.String())
	}
	return b.String()
}

// versionText is "installed" or "installed → available" when outdated.
func versionText(p brew.Package) string {
	installed := orMissing(latestInstalled(p))
	if p.Outdated && p.AvailableVersion != "" {
		return installed + " → " + p.AvailableVersion
	}
	return installed
}

func latestInstalled(p brew.Package) string {
	if len(p.InstalledVersions) == 0 {
		return ""
	}
	return p.InstalledVersions[len(p.InstalledVersions)-1]
}

func (m model) detailLines() []string {
	p := m.detail
	const labelWidth = 14
	valueWidth := max(m.width-labelWidth-1, 10)
	wrap := lipgloss.NewStyle().Width(valueWidth)

	var lines []string
	if b, ok := m.bannerLine(); ok {
		lines = append(lines, b) // first, so a short terminal never hides it
	}
	lines = append(lines, "")
	field := func(label, value string) {
		for i, l := range strings.Split(wrap.Render(orMissing(value)), "\n") {
			if i > 0 {
				label = ""
			}
			lines = append(lines, dimStyle.Render(fit(label, labelWidth))+" "+l)
		}
	}

	autoUpdates := missing // only meaningful for casks
	if p.Kind == brew.KindCask {
		autoUpdates = yesNo(p.AutoUpdates)
	}
	field("Name", titleStyle.Render(p.Name))
	field("Display name", p.DisplayName)
	field("Kind", string(p.Kind))
	field("Tap", p.Tap)
	field("Installed", strings.Join(p.InstalledVersions, ", "))
	field("Available", p.AvailableVersion)
	field("Outdated", markedYesNo(p.Outdated, outdatedMark))
	field("Pinned", yesNo(p.Pinned))
	field("Deprecated", yesNo(p.Deprecated))
	field("Disabled", yesNo(p.Disabled))
	field("Auto-updates", autoUpdates)
	field("Description", p.Description)
	field("Homepage", p.Homepage)

	source := "Source: brew info/outdated --json=v2, loaded at " + m.loadedAt.Format(timeLayout)
	if m.inv.BrewPath != "" {
		source += " from " + m.inv.BrewPath
	}
	note := "Values are Homebrew's metadata at load time; " + missing + " means Homebrew did not report it."
	para := lipgloss.NewStyle().Width(max(m.width, 20))
	lines = append(lines, "")
	lines = append(lines, strings.Split(para.Render(dimStyle.Render(source)), "\n")...)
	lines = append(lines, strings.Split(para.Render(dimStyle.Render(note)), "\n")...)
	return lines
}

// helpLines renders keyHelp. At 100×24 it fits without scrolling; narrower
// terminals wrap the descriptions (or, below 30 cells for them, put each one
// under its keys), and the overlay scrolls.
func (m model) helpLines() []string {
	labelWidth := 0
	for _, k := range keyHelp {
		labelWidth = max(labelWidth, ansi.StringWidth(k.label))
	}
	indent := 2 + labelWidth + 1
	if m.width-indent < 30 {
		indent = 6 // stacked
	}
	wrap := lipgloss.NewStyle().Width(max(m.width-indent, 20))
	lines := []string{titleStyle.Render("Keys")}
	for _, k := range keyHelp {
		desc := strings.Split(wrap.Render(k.desc), "\n")
		if indent == 6 {
			lines = append(lines, "  "+k.label)
		} else {
			lines = append(lines, "  "+fit(k.label, labelWidth)+" "+desc[0])
			desc = desc[1:]
		}
		for _, d := range desc {
			lines = append(lines, strings.Repeat(" ", indent)+d)
		}
	}
	return append(lines, m.wrapLines(dimStyle.Render("Markers: "+outdatedMark+"outdated · pin pinned · > cursor · "+
		"[x] selected (* when narrow). Nothing changes until you review a plan and press y."), "")...)
}

func (m model) statusLine() string {
	state := "ready"
	switch {
	case m.mode == viewRunning && !m.sess.isFinished:
		state = "running"
		if m.sess.isCancelling {
			state = errorStyle.Render("cancelling…")
		}
	case m.pending == purposeReview:
		state = staleStyle.Render("checking selections…")
	case m.pending == purposeReceipt:
		state = staleStyle.Render("refreshing after run…")
	case m.mode == viewReview:
		state = "review"
	case m.mode == viewReceipt:
		state = "receipt"
	case m.isLoading && !m.hasInv:
		state = "loading…"
	case m.isLoading:
		state = staleStyle.Render("stale, refreshing…")
	case m.err != nil:
		state = errorStyle.Render("error")
	}
	// State comes first so it survives truncation on narrow terminals.
	s := "[" + state + "]"
	if !m.hasInv {
		return s
	}
	c := m.counts
	s += fmt.Sprintf(" %d formulae · %d casks · %d outdated │ showing %d", c.formulae, c.casks, c.outdated, len(m.visible))
	if n := len(m.selected); n > 0 && (m.mode == viewList || m.mode == viewDetails) {
		s += fmt.Sprintf(" │ %d selected", n)
	}
	if m.notice != "" {
		s += " │ " + m.notice
	}
	if f := m.filterSummary(); f != "" {
		s += " │ " + f
	}
	return s
}

// filterSummary describes the active type filter, outdated toggle, and search.
func (m model) filterSummary() string {
	var parts []string
	if m.kind != filterAll {
		parts = append(parts, "type: "+m.kind.String())
	}
	if m.isOutdatedOnly {
		parts = append(parts, "outdated only")
	}
	if q := m.search.Value(); q != "" {
		parts = append(parts, fmt.Sprintf("search: %q", q))
	}
	return strings.Join(parts, ", ")
}

func (m model) hintLine() string {
	var h string
	switch {
	case m.isHelpVisible:
		h = "? / esc close help"
		if m.maxScrollOf(len(m.helpLines())) > 0 {
			h = "j/k scroll · " + h
		}
	case m.mode == viewDetails:
		h = "esc/q back · ? help · ctrl+c quit"
		if m.maxScrollOf(len(m.detailLines())) > 0 {
			h = "j/k scroll · " + h
		}
	case m.mode == viewReview:
		h = m.reviewFooter()
	case m.mode == viewRunning:
		h = m.runFooter()
	case m.mode == viewReceipt:
		h = m.receiptFooter()
	case m.isSearching:
		h = "type to filter · ↑/↓ move · enter keep · esc clear"
	case !m.hasInv:
		h = "r retry · q quit"
	default:
		h = m.fitHint(
			"space select · u review · / search · f type · o outdated · r refresh · enter details · ? help · q quit",
			"space select · u review · / search · f type · o outdated · r refresh · ⏎ details · ? help · q quit",
			"space select · u review · / search · ? help · q quit",
			"u review · / search · ? help · q quit",
		)
	}
	return dimStyle.Render(h)
}

// fitHint returns the first hint that fits the terminal width, or the last
// one, so the most important keys survive on narrow terminals.
func (m model) fitHint(hints ...string) string {
	for _, h := range hints {
		if ansi.StringWidth(h) <= m.width {
			return h
		}
	}
	return hints[len(hints)-1]
}

// describeError maps a load error to a short title and an actionable detail.
func describeError(err error) (title, detail string) {
	var cmdErr *brew.CommandError
	switch {
	case err == nil:
		return "unknown error", ""
	case errors.Is(err, brew.ErrNotFound):
		return "Homebrew not found",
			"Brew Board could not find the brew executable on PATH or in the standard prefixes " +
				"(/opt/homebrew, /usr/local, /home/linuxbrew/.linuxbrew). Install Homebrew from https://brew.sh " +
				"or add brew to your PATH, then press r to retry."
	case errors.Is(err, brew.ErrUnsupported):
		return "Unsupported Homebrew version",
			"brew is installed but does not provide the structured JSON (v2) output Brew Board needs. " +
				"Update Homebrew to 4.0 or newer (brew update), then press r. Details: " + err.Error()
	case errors.Is(err, context.DeadlineExceeded):
		return "Homebrew timed out",
			"brew did not answer in time. It may be busy with another brew process or an auto-update. " +
				"Wait a moment and press r to retry."
	case errors.Is(err, context.Canceled):
		return "Load cancelled", "The inventory load was cancelled. Press r to retry."
	case errors.Is(err, brew.ErrMalformed):
		return "Unreadable Homebrew output",
			"brew ran but its JSON output could not be parsed. Press r to retry; if it keeps failing, " +
				"run brew doctor. Details: " + err.Error()
	case errors.As(err, &cmdErr):
		return "brew command failed",
			fmt.Sprintf("brew %s exited with status %d: %s. Press r to retry.",
				strings.Join(cmdErr.Args, " "), cmdErr.ExitCode, orMissing(firstLine(cmdErr.Stderr)))
	default:
		return "Could not load inventory", err.Error() + ". Press r to retry."
	}
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// fit truncates s with an ellipsis or pads it to exactly w cells.
func fit(s string, w int) string {
	if w <= 0 {
		return ""
	}
	s = ansi.Truncate(s, w, "…")
	if pad := w - ansi.StringWidth(s); pad > 0 {
		s += strings.Repeat(" ", pad)
	}
	return s
}

func orMissing(s string) string {
	if strings.TrimSpace(s) == "" {
		return missing
	}
	return s
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func markedYesNo(b bool, mark string) string {
	if b {
		return strings.TrimSpace(mark) + " yes"
	}
	return "no"
}

// wrapLines wraps s to the terminal width, indented by indent.
func (m model) wrapLines(s, indent string) []string {
	wrap := lipgloss.NewStyle().Width(max(m.width-len(indent), 20))
	lines := strings.Split(wrap.Render(s), "\n")
	for i := range lines {
		lines[i] = indent + lines[i]
	}
	return lines
}

func (m model) reviewLines() []string {
	s := m.sess
	lines := []string{"", titleStyle.Render("Review the plan")}
	if len(s.problems) > 0 {
		lines = append(lines, "", errorStyle.Render("Changed since you selected:"))
		for _, p := range s.problems {
			lines = append(lines, m.wrapLines(p, "  ")...)
		}
	}
	if len(s.plan.Items) == 0 {
		return append(lines, "", "Nothing left to run. Select packages again in the list and press u.")
	}
	lines = append(lines, "")
	lines = append(lines, m.wrapLines(fmt.Sprintf("Brew Board will run %s, in order, stopping at the first failure:",
		commandCount(len(s.plan.Items))), "")...)
	for i, it := range s.plan.Items {
		lines = append(lines, "", fmt.Sprintf("%2d. %s", i+1, titleStyle.Render(it.Command())))
		lines = append(lines, m.wrapLines(dimStyle.Render(it.Explain()), "    ")...)
	}
	lines = append(lines, "")
	lines = append(lines, m.wrapLines(dependencyNote, "")...)
	lines = append(lines, dimStyle.Render("Nothing runs until you press y."))
	if s.isCleanupOffer && s.savedPath == "" {
		lines = append(lines, dimStyle.Render("The receipt of the previous run is not saved: esc, then s, to save it first."))
	}
	return lines
}

func commandCount(n int) string {
	if n == 1 {
		return "1 command"
	}
	return fmt.Sprintf("%d commands", n)
}

// sessionLines is the scrollable body of the review or receipt screen.
func (m model) sessionLines() []string {
	if m.mode == viewReceipt {
		return m.receiptLines()
	}
	return m.reviewLines()
}

// maxScroll is the largest useful scroll offset of the session screen.
func (m model) maxScroll() int {
	if m.mode != viewReview && m.mode != viewReceipt {
		return 0
	}
	return m.maxScrollOf(len(m.sessionLines()))
}

// scrollHints returns the footer variants of the session screen, longest
// first: with a scroll hint when it does not fit, then without.
func (m model) scrollHints(footers ...string) []string {
	if m.maxScroll() == 0 {
		return footers
	}
	return append([]string{"j/k scroll · " + footers[0]}, footers...)
}

func (m model) reviewFooter() string {
	var h string
	switch n := len(m.sess.plan.Items); n {
	case 0:
		h = "nothing left to run · esc back"
	case 1:
		h = "y run this command · esc back"
	default:
		h = fmt.Sprintf("y run these %d commands · esc back", n)
	}
	return m.fitHint(m.scrollHints(h)...)
}

// minOutputRows is how many output lines the running screen keeps visible
// before it collapses its spacing and item list.
const minOutputRows = 3

// runLines is the running screen: item statuses, the current state (or the
// cancel prompt), and an output pane with the newest lines. On a short
// terminal the item list is windowed around the running item so the state
// line and the output pane stay visible.
func (m model) runLines() []string {
	s := m.sess
	title := titleStyle.Render("Running the reviewed plan")
	items := make([]string, len(s.results))
	for i, r := range s.results {
		items[i] = fmt.Sprintf("%2d. %s %s", i+1, fit(statusLabel(r), 14), r.Item.Command())
	}
	var state []string
	switch {
	case s.isFinished:
		state = m.wrapLines(staleStyle.Render("Finished. Refreshing the inventory to check what changed…"), "")
	case s.isConfirmingCancel:
		state = append(m.wrapLines(errorStyle.Bold(true).Render(cancelPrompt), ""), m.wrapLines(dimStyle.Render(cancelDetail), "")...)
	case s.isCancelling:
		state = []string{errorStyle.Render("Cancelling: waiting for Homebrew to stop…")}
	case s.current >= 0 && s.current < len(s.results):
		state = []string{fmt.Sprintf("Running %d/%d: %s", s.current+1, len(s.results), s.results[s.current].Item.Command())}
	default:
		state = []string{"Starting…"}
	}
	label := dimStyle.Render("Output")

	room := m.bodyRoom()
	lines := append([]string{"", title}, items...)
	lines = append(append(append(lines, ""), state...), "", label)
	if len(lines)+minOutputRows > room {
		// Compact: no spacers, and only the items around the running one.
		itemRoom := max(room-minOutputRows-2-len(state), 1)
		first := clampScroll(s.current-itemRoom/2, len(items)-itemRoom)
		lines = append([]string{title}, items[first:min(first+itemRoom, len(items))]...)
		lines = append(append(lines, state...), label)
	}

	// The output pane takes the remaining rows; View truncates each line to
	// the terminal width.
	outRoom := max(room-len(lines), 1)
	for _, l := range s.output[max(len(s.output)-outRoom, 0):] {
		lines = append(lines, "  "+l)
	}
	return lines
}

func statusLabel(r run.Result) string {
	switch r.Status {
	case run.StatusCompleted, run.StatusFailed:
		return fmt.Sprintf("[%s %d]", r.Status, r.ExitCode)
	case run.StatusRunning:
		return outdatedStyle.Render("[running]")
	default:
		return "[" + string(r.Status) + "]"
	}
}

func (m model) runFooter() string {
	switch {
	case m.sess.isFinished:
		return "refreshing inventory…"
	case m.sess.isConfirmingCancel:
		return "y cancel the run · n keep running"
	case m.sess.isCancelling:
		return "cancelling…"
	default:
		return m.fitHint("x cancel the run · other keys are ignored while Homebrew runs", "x cancel the run")
	}
}

func (m model) receiptLines() []string {
	s := m.sess
	sum := s.receipt.Summary
	lines := []string{"", titleStyle.Render(fmt.Sprintf(
		"%d upgraded · %d failed · %d cancelled · %d not run · %d uncertain",
		sum.Upgraded, sum.Failed, sum.Cancelled, sum.NotRun, sum.Uncertain,
	))}
	if s.refreshErr != nil {
		title, _ := describeError(s.refreshErr)
		lines = append(lines, m.wrapLines(errorStyle.Render(
			"[error] Could not refresh the inventory after the run ("+title+"). "+
				"Versions below are not verified; press r to retry."), "")...)
	}
	lines = append(lines, "") // the save outcome is pinned under the footer
	for _, l := range strings.Split(strings.TrimRight(s.receipt.Text(), "\n"), "\n") {
		lines = append(lines, m.wrapLines(l, "")...)
	}
	return lines
}

func (m model) receiptFooter() string {
	h, short := "s save · c cleanup · esc back to list", "s save · c cleanup · esc back"
	if m.sess.refreshErr != nil {
		h, short = "r retry refresh · "+h, "r retry · "+short
	}
	return m.fitHint(m.scrollHints(h, short)...)
}
