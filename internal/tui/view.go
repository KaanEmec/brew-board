package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/kaanemec/brew-board/internal/brew"
	"github.com/kaanemec/brew-board/internal/receipt"
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

// maxPanelWidth caps bordered panels so prose stays readable on wide terminals.
const maxPanelWidth = 100

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
	t := currentTheme()
	switch {
	case m.sess.saveErr != nil:
		return t.bad.paintAll(m.wrapLines("[error] Could not save the receipt: "+m.sess.saveErr.Error()+" · s retry", ""))
	case m.sess.savedPath != "":
		return t.ok.paintAll(m.wrapLines("Saved to "+m.sess.savedPath, ""))
	}
	return nil
}

// headerLine is the title bar: the name, a chip saying what the session may
// do, the Homebrew version, and, when there is room, inventory totals on the
// right.
func (m model) headerLine() string {
	t := currentTheme()
	label, chip := "read-only", t.chipDim
	switch m.mode {
	case viewRunning:
		label, chip = "running Homebrew", t.chipAccent
	case viewReceipt:
		label, chip = "session receipt", t.chipInfo
	}
	left := []part{{"Brew Board", t.title}, {" ", ink{}}, {" " + label + " ", chip}}
	if m.hasInv && m.inv.BrewVersion != "" {
		left = append(left, part{"  Homebrew " + m.inv.BrewVersion, t.dim})
	}
	var right []part
	if m.hasInv && (m.mode == viewList || m.mode == viewDetails) && !m.isHelpVisible {
		c := m.counts
		if c.outdated > 0 {
			right = append(right, part{outdatedMark, t.accent}, part{fmt.Sprint(c.outdated), t.accentBold}, part{" outdated · ", t.dim})
		}
		right = append(right, part{fmt.Sprint(c.formulae + c.casks), t.text}, part{" packages", t.dim})
	}
	lw, rw := partsWidth(left), partsWidth(right)
	if len(right) > 0 && lw+2+rw <= m.width {
		left = append(left, part{strings.Repeat(" ", m.width-lw-rw), ink{}})
		left = append(left, right...)
	}
	var b strings.Builder
	for _, p := range left {
		b.WriteString(p.k.paint(p.text))
	}
	return b.String()
}

func partsWidth(parts []part) int {
	w := 0
	for _, p := range parts {
		w += ansi.StringWidth(p.text)
	}
	return w
}

// bannerLine is the one-line notice shown above a list that is not fresh.
func (m model) bannerLine() (string, bool) {
	if !m.hasInv {
		return "", false
	}
	t := currentTheme()
	at := m.loadedAt.Format(timeLayout)
	switch {
	case m.isLoading:
		return t.warnBold.paint("[stale, refreshing…]") + t.warn.paint(" showing inventory loaded at "+at), true
	case m.err != nil:
		title, _ := describeError(m.err)
		return t.badBold.paint("[error]") + t.bad.paint(" Refresh failed: "+title+". Showing inventory loaded at "+at+" · r retry"), true
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
	t := currentTheme()
	if !m.hasInv {
		if m.isLoading {
			return []string{"", t.accentBold.paint("Loading Homebrew inventory…"), "", t.dim.paint("Running brew info and brew outdated (read-only).")}
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
			t.dim.paint("Install something with brew, then press r to refresh."),
		)
	}

	cols := m.columns()
	lines = append(lines, cols.header(t))
	if len(m.visible) == 0 {
		return append(lines,
			"",
			"No packages match "+m.filterSummary()+".",
			t.dim.paint("esc clears the search · f changes type · o toggles outdated-only"),
		)
	}
	end := min(m.offset+m.listHeight(), len(m.visible))
	for i := m.offset; i < end; i++ {
		p := m.visible[i]
		lines = append(lines, cols.row(t, p, i == m.cursor, m.selected[keyOf(p)]))
	}
	return lines
}

func (m model) errorLines() []string {
	t := currentTheme()
	title, detail := describeError(m.err)
	lines := []string{"", t.badBold.paint("[error] " + title), ""}
	lines = append(lines, wrapTo(detail, m.width-2, "")...)
	return append(lines, "", t.hint("r retry · q quit", nil))
}

// columns describes the list layout for the current width. Description and
// kind columns are dropped as the terminal narrows.
type columns struct {
	width               int
	name, version, desc int
	hasKind, hasCheck   bool
}

func (m model) columns() columns {
	w := m.width
	c := columns{width: w, hasKind: w >= 72, hasCheck: w >= tightWidth}

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

// rowWidth is the cell width of every row and of the column header.
func (c columns) rowWidth() int {
	w := len(cursorMarker) + 2 + c.name + 1 + c.version + 1 + pinWidth
	if c.hasCheck {
		w += checkWidth
	}
	if c.hasKind {
		w += kindWidth + 1
	}
	if c.desc > 0 {
		w += 1 + c.desc
	}
	return w
}

// header renders the column labels, underlined; the gaps stay plain.
func (c columns) header(t *theme) string {
	var b strings.Builder
	col := func(label string, w int) {
		s := fit(label, w)
		l := strings.TrimRight(s, " ")
		b.WriteString(t.colHdr.paint(l) + s[len(l):])
	}
	b.WriteString(strings.Repeat(" ", len(cursorMarker)+2))
	if c.hasCheck {
		b.WriteString(strings.Repeat(" ", checkWidth))
	}
	col("NAME", c.name)
	b.WriteString(" ")
	if c.hasKind {
		col("KIND", kindWidth)
		b.WriteString(" ")
	}
	col("VERSION", c.version)
	b.WriteString(" ")
	col("PIN", pinWidth)
	if c.desc > 0 {
		b.WriteString(" ")
		col("DESCRIPTION", c.desc)
	}
	return b.String()
}

// row renders one package. isCursor marks the highlighted row, which is
// painted on the selection background across the full width; isStaged marks
// a package selected for the maintenance session. The text is the same with
// or without colour.
func (c columns) row(t *theme, p brew.Package, isCursor, isStaged bool) string {
	inks := &t.row[0]
	if isCursor {
		inks = &t.row[1]
	}
	var b strings.Builder
	b.Grow(c.width + 128)
	put := func(r rowRole, s string) {
		k := inks[r]
		b.WriteString(k.on)
		b.WriteString(s)
		b.WriteString(k.off)
	}

	lead := strings.Repeat(" ", len(cursorMarker))
	if isCursor {
		lead = cursorMarker
	}
	switch {
	case isStaged && !c.hasCheck:
		put(rolePlain, lead)
		put(roleChecked, "* ")
	case p.Outdated:
		put(rolePlain, lead)
		put(roleMark, outdatedMark)
	default:
		put(rolePlain, lead+"  ")
	}
	if c.hasCheck {
		// Boxes only on rows that can be (or are) staged, to keep the list calm.
		switch {
		case isStaged:
			put(roleChecked, "[x]")
			put(rolePlain, " ")
		case unstageableReason(p) == "":
			put(roleBox, "[ ]")
			put(rolePlain, " ")
		default:
			put(rolePlain, strings.Repeat(" ", checkWidth))
		}
	}
	put(roleName, fit(p.Name, c.name))
	put(rolePlain, " ")
	if c.hasKind {
		role := roleFormula
		if p.Kind == brew.KindCask {
			role = roleCask
		}
		put(role, fit(string(p.Kind), kindWidth))
		put(rolePlain, " ")
	}
	v := fit(versionText(p), c.version)
	if i := strings.Index(v, versionArrow); p.Outdated && i >= 0 {
		put(rolePlain, v[:i])
		put(roleMark, versionArrow)
		put(roleAvailable, v[i+len(versionArrow):])
	} else {
		put(rolePlain, v)
	}
	put(rolePlain, " ")
	if p.Pinned {
		put(rolePin, "pin")
	} else {
		put(rolePlain, strings.Repeat(" ", pinWidth))
	}
	if c.desc > 0 {
		put(rolePlain, " ")
		put(roleDesc, fit(p.Description, c.desc))
	}
	if pad := c.width - c.rowWidth(); isCursor && pad > 0 {
		put(rolePlain, strings.Repeat(" ", pad))
	}
	return b.String()
}

const versionArrow = " → "

// versionText is "installed" or "installed → available" when outdated.
func versionText(p brew.Package) string {
	installed := orMissing(latestInstalled(p))
	if p.Outdated && p.AvailableVersion != "" {
		return installed + versionArrow + p.AvailableVersion
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
	t := currentTheme()
	p := m.detail
	const labelWidth = 14
	pw := min(m.width, maxPanelWidth)
	valueWidth := max(panelInner(pw)-labelWidth-1, 10)
	wrap := lipgloss.NewStyle().Width(valueWidth)

	var lines []string
	if b, ok := m.bannerLine(); ok {
		lines = append(lines, b) // first, so a short terminal never hides it
	}
	lines = append(lines, "")
	var body []string
	field := func(label, value string, k ink) {
		if strings.TrimSpace(value) == "" {
			value, k = missing, t.dim
		}
		for i, l := range strings.Split(wrap.Render(value), "\n") {
			if i > 0 {
				label = ""
			}
			body = append(body, t.dim.paint(fit(label, labelWidth))+" "+k.paint(strings.TrimRight(l, " ")))
		}
	}
	flag := func(label string, b bool, yes ink) {
		if b {
			field(label, "yes", yes)
			return
		}
		field(label, "no", t.dim)
	}

	kind := t.formula
	if p.Kind == brew.KindCask {
		kind = t.cask
	}
	field("Name", p.Name, t.bold)
	field("Display name", p.DisplayName, ink{})
	field("Kind", string(p.Kind), kind)
	field("Tap", p.Tap, ink{})
	field("Installed", strings.Join(p.InstalledVersions, ", "), ink{})
	field("Available", p.AvailableVersion, ink{})
	if p.Outdated {
		field("Outdated", markedYesNo(true, outdatedMark), t.accentBold)
	} else {
		field("Outdated", markedYesNo(false, outdatedMark), t.dim)
	}
	flag("Pinned", p.Pinned, t.pin)
	flag("Deprecated", p.Deprecated, t.warn)
	flag("Disabled", p.Disabled, t.bad)
	if p.Kind == brew.KindCask { // only meaningful for casks
		flag("Auto-updates", p.AutoUpdates, t.ok)
	} else {
		field("Auto-updates", missing, t.dim)
	}
	field("Description", p.Description, ink{})
	field("Homepage", p.Homepage, t.cask)
	lines = append(lines, t.panel(t.title.paint("Package details"), body, pw, false)...)

	source := "Source: brew info/outdated --json=v2, loaded at " + m.loadedAt.Format(timeLayout)
	if m.inv.BrewPath != "" {
		source += " from " + m.inv.BrewPath
	}
	note := "Values are Homebrew's metadata at load time; " + missing + " means Homebrew did not report it."
	para := lipgloss.NewStyle().Width(max(m.width, 20))
	lines = append(lines, "")
	lines = append(lines, t.dim.paintAll(strings.Split(para.Render(source), "\n"))...)
	lines = append(lines, t.dim.paintAll(strings.Split(para.Render(note), "\n"))...)
	return lines
}

// helpLines renders keyHelp in a centred panel. At 100×24 it fits without
// scrolling (the panel then leaves its bottom edge off); narrower terminals
// wrap the descriptions (or, below 30 cells for them, put each one under its
// keys), and the overlay scrolls.
func (m model) helpLines() []string {
	t := currentTheme()
	labelWidth, descWidth := 0, 0
	for _, k := range keyHelp {
		labelWidth = max(labelWidth, ansi.StringWidth(k.label))
		descWidth = max(descWidth, ansi.StringWidth(k.desc))
	}
	pw := min(m.width, 2+labelWidth+1+descWidth+4)
	inner := panelInner(pw)
	indent := 2 + labelWidth + 1
	if inner-indent < 30 {
		indent = 6 // stacked
	}
	var body []string
	for _, k := range keyHelp {
		desc := wrapTo(k.desc, inner-indent, "")
		if indent == 6 {
			body = append(body, "  "+t.keyLabel(k.label))
		} else {
			body = append(body, "  "+t.keyLabel(fit(k.label, labelWidth))+" "+desc[0])
			desc = desc[1:]
		}
		for _, d := range desc {
			body = append(body, strings.Repeat(" ", indent)+d)
		}
	}
	body = append(body, t.dim.paintAll(wrapTo("Markers: "+outdatedMark+"outdated · pin pinned · > cursor · "+
		"[x] selected (* when narrow). Nothing changes until you review a plan and press y.", inner, ""))...)

	room := m.bodyRoom()
	isOpen := len(body)+2 > room && len(body)+1 <= room
	lines := t.panel(t.title.paint("Keys"), body, pw, isOpen)
	if margin := (m.width - pw) / 2; margin > 0 {
		pad := strings.Repeat(" ", margin)
		for i := range lines {
			lines[i] = pad + lines[i]
		}
	}
	return lines
}

// keyLabel paints the keys of a help label; a "(screen)" suffix stays dim.
func (t *theme) keyLabel(s string) string {
	if i := strings.Index(s, " ("); i >= 0 {
		return t.accentBold.paint(s[:i]) + t.dim.paint(s[i:])
	}
	return t.accentBold.paint(s)
}

// statusLine is the status bar: a state chip, then the inventory totals and
// any notice, on a full-width bar. State comes first so it survives
// truncation on narrow terminals.
func (m model) statusLine() string {
	t := currentTheme()
	state, chip := "ready", t.chipOk
	switch {
	case m.mode == viewRunning && !m.sess.isFinished:
		state, chip = "running", t.chipAccent
		if m.sess.isCancelling {
			state, chip = "cancelling…", t.chipBad
		}
	case m.pending == purposeReview:
		state, chip = "checking selections…", t.chipWarn
	case m.pending == purposeReceipt:
		state, chip = "refreshing after run…", t.chipWarn
	case m.mode == viewReview:
		state, chip = "review", t.chipAccent
	case m.mode == viewReceipt:
		state, chip = "receipt", t.chipInfo
	case m.isLoading && !m.hasInv:
		state, chip = "loading…", t.chipWarn
	case m.isLoading:
		state, chip = "stale, refreshing…", t.chipWarn
	case m.err != nil:
		state, chip = "error", t.chipBad
	}
	parts := []part{{"[" + state + "]", chip}}
	if m.hasInv {
		c := m.counts
		sep := part{" │ ", t.barDim}
		outdated := t.barBright
		if c.outdated > 0 {
			outdated = t.barAccent
		}
		parts = append(parts,
			part{" ", t.barText},
			part{fmt.Sprint(c.formulae), t.barBright}, part{" formulae · ", t.barText},
			part{fmt.Sprint(c.casks), t.barBright}, part{" casks · ", t.barText},
			part{fmt.Sprint(c.outdated), outdated}, part{" outdated", t.barText},
			sep, part{"showing ", t.barText}, part{fmt.Sprint(len(m.visible)), t.barBright},
		)
		if n := len(m.selected); n > 0 && (m.mode == viewList || m.mode == viewDetails) {
			parts = append(parts, sep, part{fmt.Sprintf("%d selected", n), t.barOk})
		}
		if m.notice != "" {
			parts = append(parts, sep, part{m.notice, t.barAccent})
		}
		if f := m.filterSummary(); f != "" {
			parts = append(parts, sep, part{f, t.barInfo})
		}
	}
	return line(parts, m.width, t.barText)
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
	t := currentTheme()
	var h string
	var keys map[string]ink
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
		keys = map[string]ink{"y": t.okBold, "esc": t.dim}
	case m.mode == viewRunning:
		h = m.runFooter()
		if m.sess.isConfirmingCancel {
			keys = map[string]ink{"y": t.badBold}
		}
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
	return t.hint(h, keys)
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

func markedYesNo(b bool, mark string) string {
	if b {
		return strings.TrimSpace(mark) + " yes"
	}
	return "no"
}

// wrapLines wraps s to the terminal width, indented by indent.
func (m model) wrapLines(s, indent string) []string {
	return wrapTo(s, m.width, indent)
}

// wrapTo wraps plain text s to width cells (at least 20), indented by indent,
// without trailing padding, so the lines can be painted afterwards.
func wrapTo(s string, width int, indent string) []string {
	wrap := lipgloss.NewStyle().Width(max(width-len(indent), 20))
	lines := strings.Split(wrap.Render(s), "\n")
	for i := range lines {
		lines[i] = indent + strings.TrimRight(lines[i], " ")
	}
	return lines
}

func (m model) reviewLines() []string {
	t := currentTheme()
	s := m.sess
	pw := min(m.width, maxPanelWidth)
	inner := panelInner(pw)
	var body []string
	if len(s.problems) > 0 {
		body = append(body, t.badBold.paint("Changed since you selected:"))
		for _, p := range s.problems {
			body = append(body, t.warn.paintAll(wrapTo(p, inner, "  "))...)
		}
		body = append(body, "")
	}
	if len(s.plan.Items) == 0 {
		body = append(body, "Nothing left to run. Select packages again in the list and press u.")
		return append([]string{""}, t.panel(t.title.paint("Review the plan"), body, pw, false)...)
	}
	body = append(body, wrapTo(fmt.Sprintf("Brew Board will run %s, in order, stopping at the first failure:",
		commandCount(len(s.plan.Items))), inner, "")...)
	for i, it := range s.plan.Items {
		body = append(body, "", t.accent.paint(fmt.Sprintf("%2d.", i+1))+" "+t.bold.paint(it.Command()))
		body = append(body, t.dim.paintAll(wrapTo(it.Explain(), inner, "    "))...)
	}
	body = append(body, "")
	body = append(body, t.warn.paintAll(wrapTo(dependencyNote, inner, ""))...)
	body = append(body, t.dim.paint("Nothing runs until you press y."))
	if s.isCleanupOffer && s.savedPath == "" {
		body = append(body, t.dim.paintAll(wrapTo("The receipt of the previous run is not saved: esc, then s, to save it first.", inner, ""))...)
	}
	return append([]string{""}, t.panel(t.title.paint("Review the plan"), body, pw, false)...)
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
	t := currentTheme()
	s := m.sess
	title := t.title.paint("Running the reviewed plan")
	items := make([]string, len(s.results))
	for i, r := range s.results {
		items[i] = m.runItem(t, i, r)
	}
	var state []string
	switch {
	case s.isFinished:
		state = t.warn.paintAll(m.wrapLines("Finished. Refreshing the inventory to check what changed…", ""))
	case s.isConfirmingCancel:
		state = append(t.badBold.paintAll(m.wrapLines(cancelPrompt, "")), t.dim.paintAll(m.wrapLines(cancelDetail, ""))...)
	case s.isCancelling:
		state = []string{t.bad.paint("Cancelling: waiting for Homebrew to stop…")}
	case s.current >= 0 && s.current < len(s.results):
		state = []string{t.accentBold.paint(fmt.Sprintf("Running %d/%d:", s.current+1, len(s.results))) +
			" " + s.results[s.current].Item.Command()}
	default:
		state = []string{t.dim.paint("Starting…")}
	}

	room := m.bodyRoom()
	lines := append([]string{"", title}, items...)
	lines = append(append(append(lines, ""), state...), "")
	if len(lines)+1+minOutputRows > room { // +1: the pane's top edge
		// Compact: no spacers, and only the items around the running one.
		itemRoom := max(room-minOutputRows-2-len(state), 1)
		first := clampScroll(s.current-itemRoom/2, len(items)-itemRoom)
		lines = append([]string{title}, items[first:min(first+itemRoom, len(items))]...)
		lines = append(lines, state...)
	}

	// The output pane takes the remaining rows, with a bottom edge only when
	// that still leaves minOutputRows of output.
	outRoom := room - len(lines) - 1
	hasBottom := outRoom-1 >= minOutputRows
	if hasBottom {
		outRoom--
	}
	outRoom = max(outRoom, 1)
	out := make([]string, 0, outRoom)
	for _, l := range s.output[max(len(s.output)-outRoom, 0):] {
		if strings.HasPrefix(l, "$ ") {
			l = t.accent.paint(l) // the command Brew Board started
		}
		out = append(out, " "+l)
	}
	for len(out) < outRoom {
		out = append(out, "")
	}
	return append(lines, t.panel(t.dim.paint("Output"), out, m.width, !hasBottom)...)
}

// runItem is one item of the running screen: a glyph and a status word that
// say the same thing, then the command.
func (m model) runItem(t *theme, i int, r run.Result) string {
	glyph, k, cmd := "○", t.dim, t.dim.paint(r.Item.Command())
	switch r.Status {
	case run.StatusRunning:
		glyph, k, cmd = m.spin.View(), t.accentBold, t.bold.paint(r.Item.Command())
	case run.StatusCompleted:
		glyph, k, cmd = "✓", t.ok, r.Item.Command()
	case run.StatusFailed:
		glyph, k, cmd = "✗", t.badBold, r.Item.Command()
	case run.StatusCancelled:
		glyph, k, cmd = "⊘", t.warn, r.Item.Command()
	}
	return t.dim.paint(fmt.Sprintf("%2d.", i+1)) + " " + k.paint(glyph) + " " + k.paint(fit(statusLabel(r), 14)) + " " + cmd
}

func statusLabel(r run.Result) string {
	switch r.Status {
	case run.StatusCompleted, run.StatusFailed:
		return fmt.Sprintf("[%s %d]", r.Status, r.ExitCode)
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

// verdictGlyph is the glyph and ink of a receipt verdict; the verdict word is
// always printed next to it.
func verdictGlyph(t *theme, verdict string) (string, ink) {
	switch verdict {
	case receipt.VerdictUpgraded, receipt.VerdictCompleted:
		return "✓", t.ok
	case receipt.VerdictFailed:
		return "✗", t.badBold
	case receipt.VerdictCancelled:
		return "⊘", t.warn
	case receipt.VerdictUncertain:
		return "?", t.warnBold
	default:
		return "○", t.dim
	}
}

func (m model) receiptLines() []string {
	t := currentTheme()
	s := m.sess
	sum := s.receipt.Summary
	chips := []struct {
		n     int
		label string
		k     ink
	}{
		{sum.Upgraded, "upgraded", t.okBold},
		{sum.Failed, "failed", t.badBold},
		{sum.Cancelled, "cancelled", t.warnBold},
		{sum.NotRun, "not run", t.bold},
		{sum.Uncertain, "uncertain", t.warnBold},
	}
	var b strings.Builder
	for i, c := range chips {
		if i > 0 {
			b.WriteString(t.dim.paint(" · "))
		}
		k := c.k
		if c.n == 0 {
			k = t.dim
		}
		b.WriteString(k.paint(fmt.Sprintf("%d %s", c.n, c.label)))
	}
	lines := []string{"", b.String()}
	if s.refreshErr != nil {
		title, _ := describeError(s.refreshErr)
		lines = append(lines, t.bad.paintAll(m.wrapLines(
			"[error] Could not refresh the inventory after the run ("+title+"). "+
				"Versions below are not verified; press r to retry.", ""))...)
	}
	lines = append(lines, "") // the save outcome is pinned under the footer
	next := 0                 // index of the next change line in the receipt text
	for _, l := range strings.Split(strings.TrimRight(s.receipt.Text(), "\n"), "\n") {
		if next < len(s.receipt.Changes) {
			c := s.receipt.Changes[next]
			if prefix := fmt.Sprintf("%d. %s: ", next+1, c.Item.Command()); strings.HasPrefix(l, prefix) {
				next++
				lines = append(lines, m.changeLines(t, l, prefix, c.Verdict)...)
				continue
			}
		}
		var k ink
		switch {
		case strings.HasPrefix(l, "NOT VERIFIED"):
			k = t.badBold
		case strings.HasPrefix(l, "Brew Board maintenance receipt"):
			k = t.bold
		case strings.HasPrefix(l, "Created:"), strings.HasPrefix(l, "Homebrew:"), strings.HasPrefix(l, "Summary:"):
			k = t.dim
		case strings.HasPrefix(l, "A command failed"):
			k = t.bad
		case strings.HasPrefix(l, "A command was cancelled"), strings.HasPrefix(l, "A result is uncertain"):
			k = t.warn
		}
		lines = append(lines, k.paintAll(m.wrapLines(l, ""))...)
	}
	return lines
}

// changeLines renders one receipt change with its verdict glyph and a
// hanging indent; the verdict word keeps the glyph's colour.
func (m model) changeLines(t *theme, l, prefix, verdict string) []string {
	glyph, k := verdictGlyph(t, verdict)
	lines := wrapTo(l, m.width-2, "")
	for i, w := range lines {
		if i > 0 {
			lines[i] = "  " + w
			continue
		}
		if strings.HasPrefix(w, prefix+verdict) {
			w = prefix + k.paint(verdict) + w[len(prefix)+len(verdict):]
		}
		lines[i] = k.paint(glyph) + " " + w
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
