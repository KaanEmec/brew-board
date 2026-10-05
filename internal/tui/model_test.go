package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/kaanemec/brew-board/internal/brew"
)

var fixedTime = time.Date(2026, 10, 5, 9, 30, 0, 0, time.UTC)

// fakeLoader returns its results in order; the last one repeats.
type fakeLoader struct {
	results []fakeResult
	calls   int
}

type fakeResult struct {
	inv brew.Inventory
	err error
}

func (f *fakeLoader) Load(context.Context) (brew.Inventory, error) {
	r := f.results[min(f.calls, len(f.results)-1)]
	f.calls++
	return r.inv, r.err
}

func fixtureInventory() brew.Inventory {
	return brew.Inventory{
		BrewPath:    "/opt/homebrew/bin/brew",
		BrewVersion: "4.6.0",
		LoadedAt:    fixedTime,
		Formulae: []brew.Package{
			{Name: "git", Kind: brew.KindFormula, Description: "Distributed revision control system", InstalledVersions: []string{"2.50.0"}, AvailableVersion: "2.51.0", Outdated: true, Tap: "homebrew/core"},
			{Name: "jq", Kind: brew.KindFormula, Description: "Lightweight and flexible command-line JSON processor", InstalledVersions: []string{"1.8.1"}, AvailableVersion: "1.8.1"},
			{Name: "node", Kind: brew.KindFormula, Description: "JavaScript runtime", InstalledVersions: []string{"22.1.0", "24.0.0"}, AvailableVersion: "24.1.0", Outdated: true, Pinned: true},
			{Name: "wget", Kind: brew.KindFormula, Description: "Internet file retriever", InstalledVersions: []string{"1.24.5"}, AvailableVersion: "1.24.5", Tap: "homebrew/core"},
		},
		Casks: []brew.Package{
			{Name: "firefox", Kind: brew.KindCask, Description: "Web browser", InstalledVersions: []string{"140.0"}, AvailableVersion: "140.0"},
			{Name: "visual-studio-code", Kind: brew.KindCask, DisplayName: "Microsoft Visual Studio Code", Description: "Open-source code editor", InstalledVersions: []string{"1.100.0"}, AvailableVersion: "1.101.0", Outdated: true, AutoUpdates: true},
		},
	}
}

// runCmd executes cmd synchronously and feeds its message back into Update.
func runCmd(t *testing.T, m model, cmd tea.Cmd) model {
	t.Helper()
	if cmd == nil {
		t.Fatal("expected a command, got nil")
	}
	next, _ := m.Update(cmd())
	return next.(model)
}

func press(t *testing.T, m model, keys ...string) (model, tea.Cmd) {
	t.Helper()
	var cmd tea.Cmd
	for _, k := range keys {
		var msg tea.KeyMsg
		switch k {
		case "enter":
			msg = tea.KeyMsg{Type: tea.KeyEnter}
		case "esc":
			msg = tea.KeyMsg{Type: tea.KeyEsc}
		case "down":
			msg = tea.KeyMsg{Type: tea.KeyDown}
		case "pgdown":
			msg = tea.KeyMsg{Type: tea.KeyPgDown}
		case "space":
			msg = tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}}
		case "ctrl+c":
			msg = tea.KeyMsg{Type: tea.KeyCtrlC}
		default:
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
		}
		var next tea.Model
		next, cmd = m.Update(msg)
		m = next.(model)
	}
	return m, cmd
}

func typeText(t *testing.T, m model, s string) model {
	t.Helper()
	for _, r := range s {
		m, _ = press(t, m, string(r))
	}
	return m
}

func names(m model) []string {
	out := make([]string, 0, len(m.visible))
	for _, p := range m.visible {
		out = append(out, p.Name)
	}
	return out
}

func newModel(t *testing.T, loader brew.Loader, width int) model {
	t.Helper()
	m := New(loader, WithClock(func() time.Time { return fixedTime })).(model)
	next, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: 30})
	return next.(model)
}

func loaded(t *testing.T, width int) (model, *fakeLoader) {
	t.Helper()
	loader := &fakeLoader{results: []fakeResult{{inv: fixtureInventory()}}}
	m := newModel(t, loader, width)
	return runCmd(t, m, m.Init()), loader
}

func TestInitialLoad(t *testing.T) {
	loader := &fakeLoader{results: []fakeResult{{inv: fixtureInventory()}}}
	m := newModel(t, loader, 100)
	if !m.isLoading || m.hasInv {
		t.Fatalf("before load: isLoading=%v hasInv=%v", m.isLoading, m.hasInv)
	}
	if v := m.View(); !strings.Contains(v, "Loading Homebrew inventory") {
		t.Errorf("loading view missing message:\n%s", v)
	}

	m = runCmd(t, m, m.Init())
	if m.isLoading || !m.hasInv || m.err != nil {
		t.Fatalf("after load: isLoading=%v hasInv=%v err=%v", m.isLoading, m.hasInv, m.err)
	}
	expected := []string{"firefox", "git", "jq", "node", "visual-studio-code", "wget"}
	if got := names(m); strings.Join(got, ",") != strings.Join(expected, ",") {
		t.Errorf("visible = %v, expected %v", got, expected)
	}
	if v := m.View(); !strings.Contains(v, "4 formulae · 2 casks · 3 outdated") {
		t.Errorf("status bar missing counts:\n%s", v)
	}
}

func TestFiltersAndSearch(t *testing.T) {
	tests := []struct {
		name     string
		keys     []string
		search   string
		expected []string
	}{
		{name: "casks only", keys: []string{"f", "f"}, expected: []string{"firefox", "visual-studio-code"}},
		{name: "formulae only", keys: []string{"f"}, expected: []string{"git", "jq", "node", "wget"}},
		{name: "cycle back to all", keys: []string{"f", "f", "f"}, expected: []string{"firefox", "git", "jq", "node", "visual-studio-code", "wget"}},
		{name: "outdated only", keys: []string{"o"}, expected: []string{"git", "node", "visual-studio-code"}},
		{name: "outdated casks", keys: []string{"o", "f", "f"}, expected: []string{"visual-studio-code"}},
		{name: "search by name case insensitive", search: "GI", expected: []string{"git"}},
		{name: "search by description", search: "json", expected: []string{"jq"}},
		{name: "search by display name", search: "microsoft", expected: []string{"visual-studio-code"}},
		{name: "search with no match", search: "zzz", expected: []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, _ := loaded(t, 100)
			m, _ = press(t, m, tt.keys...)
			if tt.search != "" {
				m, _ = press(t, m, "/")
				m = typeText(t, m, tt.search)
			}
			if got := names(m); strings.Join(got, ",") != strings.Join(tt.expected, ",") {
				t.Errorf("visible = %v, expected %v", got, tt.expected)
			}
		})
	}
}

func TestSearchEscClears(t *testing.T) {
	m, _ := loaded(t, 100)
	m, _ = press(t, m, "/")
	m = typeText(t, m, "git")
	if len(m.visible) != 1 {
		t.Fatalf("expected 1 match, got %v", names(m))
	}
	m, _ = press(t, m, "esc")
	if m.isSearching || m.search.Value() != "" || len(m.visible) != 6 {
		t.Errorf("after esc: isSearching=%v query=%q visible=%v", m.isSearching, m.search.Value(), names(m))
	}
}

func TestNoMatchMessage(t *testing.T) {
	m, _ := loaded(t, 100)
	m, _ = press(t, m, "/")
	m = typeText(t, m, "zzz")
	if v := m.View(); !strings.Contains(v, "No packages match") || !strings.Contains(v, `search: "zzz"`) {
		t.Errorf("no-match view missing message:\n%s", v)
	}
}

func TestNavigation(t *testing.T) {
	m, _ := loaded(t, 100)
	m, _ = press(t, m, "j", "j", "k")
	if m.cursor != 1 {
		t.Errorf("after j j k: cursor = %d, expected 1", m.cursor)
	}
	m, _ = press(t, m, "G")
	if m.cursor != 5 {
		t.Errorf("after G: cursor = %d, expected 5", m.cursor)
	}
	m, _ = press(t, m, "j")
	if m.cursor != 5 {
		t.Errorf("past end: cursor = %d, expected 5", m.cursor)
	}
	m, _ = press(t, m, "g")
	if m.cursor != 0 {
		t.Errorf("after g: cursor = %d, expected 0", m.cursor)
	}
	m, _ = press(t, m, "pgdown")
	if m.cursor != 5 {
		t.Errorf("after pgdown: cursor = %d, expected 5", m.cursor)
	}
}

func TestScrollKeepsCursorVisible(t *testing.T) {
	inv := brew.Inventory{LoadedAt: fixedTime}
	for i := range 50 {
		inv.Formulae = append(inv.Formulae, brew.Package{Name: fmt.Sprintf("pkg%02d", i), Kind: brew.KindFormula})
	}
	m := newModel(t, &fakeLoader{results: []fakeResult{{inv: inv}}}, 80)
	m = runCmd(t, m, m.Init())
	m, _ = press(t, m, "G")
	if v := m.View(); !strings.Contains(v, "> ") || !strings.Contains(v, "pkg49") {
		t.Errorf("last row not visible after G:\n%s", v)
	}
	if lines := strings.Count(m.View(), "\n") + 1; lines != 30 {
		t.Errorf("view has %d lines, expected terminal height 30", lines)
	}
}

func TestDetailsRoundTripPreservesState(t *testing.T) {
	m, _ := loaded(t, 100)
	m, _ = press(t, m, "/")
	m = typeText(t, m, "o")
	m, _ = press(t, m, "enter", "j", "enter") // keep search, move, open details
	if m.mode != viewDetails {
		t.Fatalf("mode = %v, expected details", m.mode)
	}
	wantName := m.visible[1].Name
	if m.detail.Name != wantName {
		t.Fatalf("detail = %q, expected %q", m.detail.Name, wantName)
	}
	visibleBefore := names(m)

	for _, back := range []string{"esc", "q"} {
		t.Run(back, func(t *testing.T) {
			m, _ := press(t, m, back)
			if m.mode != viewList {
				t.Fatalf("mode = %v, expected list", m.mode)
			}
			if m.cursor != 1 || m.search.Value() != "o" {
				t.Errorf("cursor=%d query=%q, expected 1 and %q", m.cursor, m.search.Value(), "o")
			}
			if got := names(m); strings.Join(got, ",") != strings.Join(visibleBefore, ",") {
				t.Errorf("visible changed: %v, expected %v", got, visibleBefore)
			}
		})
	}
}

func TestDetailsView(t *testing.T) {
	m, _ := loaded(t, 100)
	m, _ = press(t, m, "j")     // firefox sorts before git
	m, _ = press(t, m, "enter") // git
	v := m.View()
	for _, want := range []string{
		"git", "formula", "homebrew/core", "2.50.0", "2.51.0", "↑ yes",
		"Distributed revision control system",
		"Source: brew info/outdated --json=v2, loaded at 2026-10-05 09:30:00",
	} {
		if !strings.Contains(v, want) {
			t.Errorf("details missing %q:\n%s", want, v)
		}
	}
	// Missing homepage and display name must render as a dash, not a guess.
	if !strings.Contains(v, "Homepage") || !strings.Contains(v, missing) {
		t.Errorf("details should show %q for missing fields:\n%s", missing, v)
	}
}

func TestRefreshKeepsInventoryAndMarksStale(t *testing.T) {
	updated := fixtureInventory()
	updated.Formulae = updated.Formulae[:3] // wget removed
	loader := &fakeLoader{results: []fakeResult{{inv: fixtureInventory()}, {inv: updated}}}
	m := newModel(t, loader, 100)
	m = runCmd(t, m, m.Init())
	m, _ = press(t, m, "j", "j") // node
	sel := m.visible[m.cursor].Name

	m, cmd := press(t, m, "r")
	if !m.isLoading || !m.hasInv || len(m.visible) != 6 {
		t.Fatalf("while refreshing: isLoading=%v hasInv=%v visible=%v", m.isLoading, m.hasInv, names(m))
	}
	if v := m.View(); !strings.Contains(v, "stale, refreshing…") {
		t.Errorf("refreshing view not labelled stale:\n%s", v)
	}
	if _, again := press(t, m, "r"); again != nil {
		t.Error("second r while refreshing started another load")
	}

	m = runCmd(t, m, cmd)
	if m.isLoading || len(m.visible) != 5 {
		t.Fatalf("after refresh: isLoading=%v visible=%v", m.isLoading, names(m))
	}
	if got := m.visible[m.cursor].Name; got != sel {
		t.Errorf("selection after refresh = %q, expected %q", got, sel)
	}
	if loader.calls != 2 {
		t.Errorf("loader calls = %d, expected 2", loader.calls)
	}
}

func TestRefreshUpdatesOpenDetails(t *testing.T) {
	updated := fixtureInventory()
	updated.Formulae[0].InstalledVersions = []string{"2.51.0"} // git upgraded
	updated.Formulae[0].Outdated = false
	updated.Formulae = append(updated.Formulae[:3:3], updated.Formulae[0]) // wget removed
	loader := &fakeLoader{results: []fakeResult{{inv: fixtureInventory()}, {inv: updated}}}
	m := newModel(t, loader, 100)
	m = runCmd(t, m, m.Init())
	m, cmd := press(t, m, "r")  // refresh in flight
	m, _ = press(t, m, "j")     // firefox sorts before git
	m, _ = press(t, m, "enter") // open git meanwhile
	if m.mode != viewDetails || m.detail.Name != "git" || !m.detail.Outdated {
		t.Fatalf("mode=%v detail=%+v, expected outdated git details", m.mode, m.detail)
	}
	m = runCmd(t, m, cmd)
	if m.mode != viewDetails {
		t.Fatalf("mode = %v, expected details to stay open", m.mode)
	}
	if got := m.detail.InstalledVersions; len(got) != 1 || got[0] != "2.51.0" || m.detail.Outdated {
		t.Errorf("detail not refreshed: %+v", m.detail)
	}
	if v := m.View(); !strings.Contains(v, "2.51.0") {
		t.Errorf("details view shows stale values:\n%s", v)
	}
}

func TestRefreshClosesDetailsOfRemovedPackage(t *testing.T) {
	updated := fixtureInventory()
	updated.Formulae = updated.Formulae[1:] // git removed
	loader := &fakeLoader{results: []fakeResult{{inv: fixtureInventory()}, {inv: updated}}}
	m := newModel(t, loader, 100)
	m = runCmd(t, m, m.Init())
	m, cmd := press(t, m, "r")  // refresh in flight
	m, _ = press(t, m, "j")     // firefox sorts before git
	m, _ = press(t, m, "enter") // open git meanwhile
	m = runCmd(t, m, cmd)
	if m.mode != viewList {
		t.Fatalf("mode = %v, expected list", m.mode)
	}
	if v := m.View(); !strings.Contains(v, "git is no longer installed") {
		t.Errorf("missing notice:\n%s", v)
	}
	m, _ = press(t, m, "j")
	if m.notice != "" {
		t.Errorf("notice not cleared by key press: %q", m.notice)
	}
}

func TestBannerKeepsSelectedRowVisible(t *testing.T) {
	inv := brew.Inventory{LoadedAt: fixedTime}
	for i := range 50 {
		inv.Formulae = append(inv.Formulae, brew.Package{Name: fmt.Sprintf("pkg%02d", i), Kind: brew.KindFormula})
	}
	loader := &fakeLoader{results: []fakeResult{{inv: inv}, {err: errors.New("boom")}}}
	m := newModel(t, loader, 80)
	m = runCmd(t, m, m.Init())
	// Put the cursor on the last row of the first page.
	for range m.listHeight() - 1 {
		m, _ = press(t, m, "j")
	}
	if m.offset != 0 {
		t.Fatalf("offset = %d, expected 0", m.offset)
	}
	inView := func(m model) {
		t.Helper()
		if m.cursor < m.offset || m.cursor >= m.offset+m.listHeight() {
			t.Fatalf("cursor %d outside viewport [%d,%d)", m.cursor, m.offset, m.offset+m.listHeight())
		}
		if v := m.View(); !strings.Contains(v, "> "+m.visible[m.cursor].Name) && !strings.Contains(v, m.visible[m.cursor].Name) {
			t.Fatalf("selected row not rendered:\n%s", v)
		}
	}
	m, cmd := press(t, m, "r") // stale banner appears
	inView(m)
	m = runCmd(t, m, cmd) // failure keeps an error banner
	inView(m)
}

func TestQuitCancelsLoad(t *testing.T) {
	var got context.Context
	loader := loaderFunc(func(ctx context.Context) (brew.Inventory, error) {
		got = ctx
		return fixtureInventory(), nil
	})
	m := New(loader).(model)
	msg := m.Init()()
	if got == nil || got.Err() != nil {
		t.Fatalf("load context should be live while the load runs: %v", got)
	}
	if _, _ = m.Update(msg); got.Err() == nil {
		t.Error("context not cancelled when the load completed")
	}

	var live context.Context
	m2 := New(loaderFunc(func(ctx context.Context) (brew.Inventory, error) {
		live = ctx
		return fixtureInventory(), nil
	})).(model)
	_ = m2.Init()()
	_, qcmd := m2.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if qcmd == nil || live.Err() == nil {
		t.Error("ctrl+c did not cancel the load context")
	}
}

type loaderFunc func(context.Context) (brew.Inventory, error)

func (f loaderFunc) Load(ctx context.Context) (brew.Inventory, error) { return f(ctx) }

func TestFailedRefreshKeepsInventory(t *testing.T) {
	loader := &fakeLoader{results: []fakeResult{
		{inv: fixtureInventory()},
		{err: &brew.CommandError{Args: []string{"outdated", "--json=v2"}, ExitCode: 1, Stderr: "Error: another brew process is running\n"}},
	}}
	m := newModel(t, loader, 100)
	m = runCmd(t, m, m.Init())
	m, cmd := press(t, m, "r")
	m = runCmd(t, m, cmd)

	if m.err == nil || !m.hasInv || len(m.visible) != 6 {
		t.Fatalf("after failed refresh: err=%v hasInv=%v visible=%v", m.err, m.hasInv, names(m))
	}
	v := m.View()
	for _, want := range []string{"Refresh failed", "brew command failed", "[error]", "git"} {
		if !strings.Contains(v, want) {
			t.Errorf("view missing %q:\n%s", want, v)
		}
	}
}

func TestErrorStates(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected string
	}{
		{name: "not found", err: fmt.Errorf("discover: %w", brew.ErrNotFound), expected: "Homebrew not found"},
		{name: "unsupported", err: fmt.Errorf("info: %w", brew.ErrUnsupported), expected: "Unsupported Homebrew version"},
		{name: "malformed", err: fmt.Errorf("parse: %w", brew.ErrMalformed), expected: "Unreadable Homebrew output"},
		{name: "timeout", err: fmt.Errorf("info: %w", context.DeadlineExceeded), expected: "Homebrew timed out"},
		{name: "command", err: fmt.Errorf("run: %w", &brew.CommandError{Args: []string{"info"}, ExitCode: 2, Stderr: "boom"}), expected: "brew info exited with status 2: boom"},
		{name: "other", err: errors.New("disk on fire"), expected: "disk on fire"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newModel(t, &fakeLoader{results: []fakeResult{{err: tt.err}}}, 80)
			m = runCmd(t, m, m.Init())
			if m.hasInv || m.err == nil {
				t.Fatalf("hasInv=%v err=%v", m.hasInv, m.err)
			}
			if v := m.View(); !strings.Contains(v, tt.expected) {
				t.Errorf("view missing %q:\n%s", tt.expected, v)
			}
		})
	}
}

func TestEmptyInventory(t *testing.T) {
	m := newModel(t, &fakeLoader{results: []fakeResult{{inv: brew.Inventory{LoadedAt: fixedTime}}}}, 80)
	m = runCmd(t, m, m.Init())
	if v := m.View(); !strings.Contains(v, "no formulae or casks are installed") {
		t.Errorf("empty view missing message:\n%s", v)
	}
	m, _ = press(t, m, "enter", "j", "G") // must not panic on an empty list
	if m.mode != viewList {
		t.Errorf("enter on empty list changed mode to %v", m.mode)
	}
}

func TestViewWidths(t *testing.T) {
	tests := []struct {
		width      int
		expected   []string
		unexpected []string
	}{
		{width: 60, expected: []string{"↑", "2.50.0 → 2.51.0", "pin", "NAME", "visual-studio-code"}, unexpected: []string{"KIND", "DESCRIPTION"}},
		{width: 120, expected: []string{"↑", "2.50.0 → 2.51.0", "pin", "KIND", "cask", "DESCRIPTION", "Internet file retriever"}},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("width %d", tt.width), func(t *testing.T) {
			m, _ := loaded(t, tt.width)
			for _, view := range []model{m, mustPress(t, m, "?"), mustPress(t, m, "enter")} {
				v := view.View()
				for _, line := range strings.Split(v, "\n") {
					if w := len([]rune(line)); w > tt.width {
						t.Errorf("line wider than %d (%d): %q", tt.width, w, line)
					}
				}
			}
			v := m.View()
			for _, want := range tt.expected {
				if !strings.Contains(v, want) {
					t.Errorf("view missing %q:\n%s", want, v)
				}
			}
			for _, notWant := range tt.unexpected {
				if strings.Contains(v, notWant) {
					t.Errorf("view should not contain %q at width %d:\n%s", notWant, tt.width, v)
				}
			}
		})
	}
}

func TestTruncatesLongNames(t *testing.T) {
	inv := brew.Inventory{LoadedAt: fixedTime, Formulae: []brew.Package{{
		Name: strings.Repeat("very-long-formula-name-", 5), Kind: brew.KindFormula, InstalledVersions: []string{"1.0"},
	}}}
	m := newModel(t, &fakeLoader{results: []fakeResult{{inv: inv}}}, 60)
	m = runCmd(t, m, m.Init())
	if v := m.View(); !strings.Contains(v, "…") {
		t.Errorf("long name not truncated with ellipsis:\n%s", v)
	}
}

func TestHelpOverlay(t *testing.T) {
	m, _ := loaded(t, 100)
	m, _ = press(t, m, "?")
	v := m.View()
	for _, want := range []string{"Keys", "toggle outdated-only", "refresh inventory", "quit"} {
		if !strings.Contains(v, want) {
			t.Errorf("help missing %q", want)
		}
	}
	m, cmd := press(t, m, "q")
	if m.isHelpVisible || cmd != nil {
		t.Errorf("q should close help without quitting: isHelpVisible=%v cmd=%v", m.isHelpVisible, cmd != nil)
	}
}

func TestQuit(t *testing.T) {
	m, _ := loaded(t, 100)
	_, cmd := press(t, m, "q")
	if cmd == nil {
		t.Fatal("q returned no command")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Error("q did not quit")
	}
}

func mustPress(t *testing.T, m model, keys ...string) model {
	t.Helper()
	m, _ = press(t, m, keys...)
	return m
}
