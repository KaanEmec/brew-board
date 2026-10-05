package tui

import (
	"strings"
	"sync"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
)

// pal is the Brew Board palette. Each colour has a light- and a dark-background
// variant; lipgloss picks one and degrades it to the terminal's profile (256
// or 16 colours, or none with NO_COLOR). Colour never carries meaning alone:
// every state also has a text marker.
var pal = struct {
	accent, formula, cask, ok, warn, bad, pin, dim, text, bright lipgloss.AdaptiveColor
	selBg, barBg, chipBg, border                                 lipgloss.AdaptiveColor
}{
	accent:  lipgloss.AdaptiveColor{Light: "#B4530A", Dark: "#F2A65A"}, // copper: brand, title, outdated
	formula: lipgloss.AdaptiveColor{Light: "#4D7C0F", Dark: "#A3C585"}, // muted green
	cask:    lipgloss.AdaptiveColor{Light: "#0E7490", Dark: "#5FD7D7"}, // teal; also "info"
	ok:      lipgloss.AdaptiveColor{Light: "#15803D", Dark: "#5FD787"},
	warn:    lipgloss.AdaptiveColor{Light: "#A16207", Dark: "#FFD75F"},
	bad:     lipgloss.AdaptiveColor{Light: "#B91C1C", Dark: "#FF8787"},
	pin:     lipgloss.AdaptiveColor{Light: "#6D28D9", Dark: "#AF87FF"},
	dim:     lipgloss.AdaptiveColor{Light: "#6B7280", Dark: "#8A8A8A"},
	text:    lipgloss.AdaptiveColor{Light: "#1F2937", Dark: "#E4E4E4"},
	bright:  lipgloss.AdaptiveColor{Light: "#000000", Dark: "#FFFFFF"},
	selBg:   lipgloss.AdaptiveColor{Light: "#F6E3CF", Dark: "#3D3229"}, // warm selection
	barBg:   lipgloss.AdaptiveColor{Light: "#E8E8E8", Dark: "#262626"},
	chipBg:  lipgloss.AdaptiveColor{Light: "#D7D7D7", Dark: "#3A3A3A"},
	border:  lipgloss.AdaptiveColor{Light: "#BCBCBC", Dark: "#585858"},
}

// ink is a style reduced to its escape sequences, so hot paths can paint text
// by concatenation instead of lipgloss.Render. Under the ASCII profile both
// are empty and painting is a no-op.
type ink struct{ on, off string }

func inkOf(s lipgloss.Style) ink {
	r := s.Render("X") // escape sequences contain no X
	i := strings.IndexByte(r, 'X')
	if i < 0 {
		return ink{}
	}
	return ink{on: r[:i], off: r[i+1:]}
}

func (k ink) paint(s string) string {
	if k.on == "" || s == "" {
		return s
	}
	return k.on + s + k.off
}

// paintAll paints each line separately, so styles never span a line break.
func (k ink) paintAll(lines []string) []string {
	for i, l := range lines {
		lines[i] = k.paint(l)
	}
	return lines
}

// rowRole is one kind of segment of a package row.
type rowRole int

const (
	rolePlain rowRole = iota
	roleName
	roleMark
	roleChecked
	roleRemove
	roleBox
	roleFormula
	roleCask
	roleAvailable
	rolePin
	roleDesc
	roleCount
)

// theme holds every ink the views use. It is built once per colour profile
// and background, so View never builds styles.
type theme struct {
	text, bold, dim, title, accent, accentBold ink
	ok, okBold, warn, warnBold, bad, badBold   ink
	pin, cask, formula, bright, border, colHdr ink

	// row[0] paints a package row, row[1] the cursor row on the selection
	// background.
	row [2][roleCount]ink

	// Status bar segments on the bar background, and the state chip.
	barText, barDim, barBright, barAccent, barOk, barWarn, barBad, barInfo ink
	chipOk, chipAccent, chipWarn, chipBad, chipInfo, chipDim               ink
}

func newTheme() *theme {
	base := lipgloss.NewStyle()
	fg := func(c lipgloss.TerminalColor) lipgloss.Style { return base.Foreground(c) }
	onBar := func(c lipgloss.TerminalColor) ink { return inkOf(fg(c).Background(pal.barBg)) }
	chip := func(c lipgloss.TerminalColor) ink { return inkOf(fg(c).Background(pal.chipBg).Bold(true)) }

	t := &theme{
		text:       inkOf(fg(pal.text)),
		bold:       inkOf(fg(pal.bright).Bold(true)),
		dim:        inkOf(fg(pal.dim)),
		title:      inkOf(fg(pal.accent).Bold(true)),
		accent:     inkOf(fg(pal.accent)),
		accentBold: inkOf(fg(pal.accent).Bold(true)),
		ok:         inkOf(fg(pal.ok)),
		okBold:     inkOf(fg(pal.ok).Bold(true)),
		warn:       inkOf(fg(pal.warn)),
		warnBold:   inkOf(fg(pal.warn).Bold(true)),
		bad:        inkOf(fg(pal.bad)),
		badBold:    inkOf(fg(pal.bad).Bold(true)),
		pin:        inkOf(fg(pal.pin)),
		cask:       inkOf(fg(pal.cask)),
		formula:    inkOf(fg(pal.formula)),
		bright:     inkOf(fg(pal.bright)),
		border:     inkOf(fg(pal.border)),
		colHdr:     inkOf(fg(pal.dim).Underline(true)),

		barText:   onBar(pal.text),
		barDim:    onBar(pal.dim),
		barBright: inkOf(fg(pal.bright).Background(pal.barBg).Bold(true)),
		barAccent: inkOf(fg(pal.accent).Background(pal.barBg).Bold(true)),
		barOk:     onBar(pal.ok),
		barWarn:   onBar(pal.warn),
		barBad:    onBar(pal.bad),
		barInfo:   onBar(pal.cask),

		chipOk:     chip(pal.ok),
		chipAccent: chip(pal.accent),
		chipWarn:   chip(pal.warn),
		chipBad:    chip(pal.bad),
		chipInfo:   chip(pal.cask),
		chipDim:    inkOf(fg(pal.dim).Background(pal.chipBg)),
	}

	rows := [roleCount]lipgloss.Style{
		rolePlain:     base,
		roleName:      base,
		roleMark:      fg(pal.accent).Bold(true),
		roleChecked:   fg(pal.ok).Bold(true),
		roleRemove:    fg(pal.bad).Bold(true),
		roleBox:       fg(pal.dim),
		roleFormula:   fg(pal.formula),
		roleCask:      fg(pal.cask),
		roleAvailable: fg(pal.bright).Bold(true),
		rolePin:       fg(pal.pin).Bold(true),
		roleDesc:      fg(pal.dim),
	}
	for r, s := range rows {
		t.row[0][r] = inkOf(s)
		sel := s.Background(pal.selBg)
		switch rowRole(r) {
		case rolePlain, roleDesc:
			sel = sel.Foreground(pal.text)
		case roleName:
			sel = sel.Foreground(pal.bright).Bold(true)
		}
		t.row[1][r] = inkOf(sel)
	}
	return t
}

type themeKey struct {
	profile termenv.Profile
	isDark  bool
}

var themeCache struct {
	sync.Mutex
	key themeKey
	t   *theme
}

// currentTheme returns the theme for lipgloss's current colour profile and
// background, rebuilding it only when either changes.
func currentTheme() *theme {
	k := themeKey{lipgloss.ColorProfile(), lipgloss.HasDarkBackground()}
	themeCache.Lock()
	defer themeCache.Unlock()
	if themeCache.t == nil || themeCache.key != k {
		themeCache.t, themeCache.key = newTheme(), k
	}
	return themeCache.t
}

// part is a run of text painted with one ink.
type part struct {
	text string
	k    ink
}

// line joins parts into exactly width cells: when they are too wide the text
// is cut to width-1 cells plus "…" (as View would cut it), otherwise the rest
// is filled with spaces painted with fill. Truncating before painting keeps
// backgrounds from wrapping.
func line(parts []part, width int, fill ink) string {
	if width <= 0 {
		return ""
	}
	total := 0
	for _, p := range parts {
		total += ansi.StringWidth(p.text)
	}
	limit, tail := width, ""
	if total > width {
		limit, tail = width-1, "…"
	}
	var b strings.Builder
	used := 0
	last := fill
	for _, p := range parts {
		if used >= limit {
			break
		}
		s := p.text
		if w := ansi.StringWidth(s); used+w > limit {
			s = ansi.Truncate(s, limit-used, "")
		}
		b.WriteString(p.k.paint(s))
		used += ansi.StringWidth(s)
		last = p.k
	}
	if tail != "" {
		b.WriteString(last.paint(tail))
		used++
	}
	if used < width {
		b.WriteString(fill.paint(strings.Repeat(" ", width-used)))
	}
	return b.String()
}

// minPanelWidth is the narrowest terminal that still gets bordered panels.
const minPanelWidth = 24

// panelInner is the content width of a panel of the given outer width.
func panelInner(width int) int {
	if width < minPanelWidth {
		return width
	}
	return width - 4 // "│ " and " │"
}

// panel frames body in a rounded border exactly width cells wide, with title
// (already painted) in the top edge. Each body line is cut or padded to the inner width. With
// isOpen the bottom edge is left off, for screens that would otherwise need
// to scroll. Below minPanelWidth the body is returned unframed.
func (t *theme) panel(title string, body []string, width int, isOpen bool) []string {
	if width < minPanelWidth {
		return body
	}
	inner := width - 4
	title = ansi.Truncate(title, max(width-6, 0), "…")
	top := t.border.paint("╭─")
	rule := width - 3
	if title != "" {
		top += " " + title + " "
		rule -= ansi.StringWidth(title) + 2
	}
	top += t.border.paint(strings.Repeat("─", max(rule, 0)) + "╮")

	out := make([]string, 0, len(body)+2)
	out = append(out, top)
	side := t.border.paint("│")
	for _, l := range body {
		out = append(out, side+" "+fitStyled(l, inner)+" "+side)
	}
	if !isOpen {
		out = append(out, t.border.paint("╰"+strings.Repeat("─", width-2)+"╯"))
	}
	return out
}

// fitStyled cuts a possibly styled line to w cells or pads it with spaces.
func fitStyled(s string, w int) string {
	n := ansi.StringWidth(s)
	if n > w {
		s = ansi.Truncate(s, w, "…")
		n = ansi.StringWidth(s)
	}
	return s + strings.Repeat(" ", max(w-n, 0))
}

// hintKeys are the key names a footer hint can start with.
var hintKeys = map[string]bool{
	"space": true, "/": true, "f": true, "o": true, "r": true, "enter": true, "⏎": true,
	"?": true, "q": true, "esc": true, "esc/q": true, "j/k": true, "↑/↓": true, "ctrl+c": true,
	"y": true, "n": true, "x": true, "s": true, "c": true, "d": true, "a": true,
}

// hint styles a footer hint: in each " · " segment the leading keys are
// painted (accent unless keyInk overrides a key) and the description dim.
// The text is unchanged, so fitHint's widths still hold.
func (t *theme) hint(h string, keyInk map[string]ink) string {
	var b strings.Builder
	for i, seg := range strings.Split(h, " · ") {
		if i > 0 {
			b.WriteString(t.dim.paint(" · "))
		}
		var keys []string
		rest := seg
		for {
			f, after, found := strings.Cut(rest, " ")
			if !found || !hintKeys[f] {
				break
			}
			keys, rest = append(keys, f), after
		}
		if len(keys) == 0 {
			b.WriteString(t.dim.paint(seg))
			continue
		}
		k := t.accentBold
		if o, ok := keyInk[keys[0]]; ok {
			k = o
		}
		b.WriteString(k.paint(strings.Join(keys, " ")) + " " + t.dim.paint(rest))
	}
	return b.String()
}
