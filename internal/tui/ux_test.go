package tui

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"

	"github.com/kaanemec/brew-board/internal/brew"
	"github.com/kaanemec/brew-board/internal/run"
)

// longName is 60 runes, mostly multi-byte and partly double-width.
var longName = string([]rune(strings.Repeat("日本語-ünïcødé-", 6))[:60])

// uxInventory is the fixture plus an outdated package with a long name.
func uxInventory() brew.Inventory {
	inv := fixtureInventory()
	inv.Formulae = append(inv.Formulae, brew.Package{
		Name: longName, Kind: brew.KindFormula, Tap: "someone/very-long-tap-name",
		Description:       strings.Repeat("A long description with ünïcødé and 日本語 text. ", 6),
		InstalledVersions: []string{"1.0.0-very-long-version"}, AvailableVersion: "2.0.0-very-long-version", Outdated: true,
	})
	return inv
}

// longOutput mixes double-width runes, controls, and lines far wider than any terminal.
var longOutput = []string{
	strings.Repeat("long output line ", 30),
	strings.Repeat("日本語の出力 ", 40),
	"bell\a back\b\b space \x1b[31mred\x1b[0m\ttab",
}

// screenStates returns every screen state, reached at 100×30. Each model is
// resized by the caller.
func screenStates(t *testing.T) map[string]model {
	t.Helper()
	screens := map[string]model{}
	loader := &fakeLoader{results: []fakeResult{{inv: uxInventory()}, {inv: uxInventory()}, {err: errors.New("boom")}, {inv: uxInventory()}}}
	m := sessionModel(t, loader, &fakeExecutor{}, 100)
	screens["list"] = m
	screens["help"] = mustPress(t, m, "?")
	screens["details"] = mustPress(t, cursorTo(t, m, longName), "enter")
	screens["search"] = typeText(t, mustPress(t, m, "/"), "日本")
	stale, _ := press(t, m, "r")
	screens["list stale"] = stale
	screens["details stale"] = mustPress(t, cursorTo(t, stale, "git"), "enter")
	failed := m
	failed.err = errors.New("boom")
	screens["list error"] = failed

	m = stage(t, m, "git", longName)
	screens["list staged"] = m
	m = review(t, m)
	screens["review"] = m

	running, _, _ := confirm(t, m)
	t.Cleanup(running.sess.cancel)
	for _, ev := range []run.Event{run.ItemStarted{Index: 0}, run.OutputLine{Line: longOutput[0]}, run.OutputLine{Line: longOutput[1]}, run.OutputLine{Line: longOutput[2]}} {
		next, _ := running.Update(runEventMsg{ev: ev})
		running = next.(model)
	}
	screens["running"] = running
	screens["cancel prompt"] = mustPress(t, running, "x")

	m, execute, wait := confirm(t, m)
	execute()
	m = drain(t, m, wait) // the post-run refresh fails: an unverified receipt
	screens["receipt unverified"] = m
	screens["receipt saved"] = mustPress(t, m, "s")
	noHome := m
	noHome.homeDir = func() (string, error) { return "", errors.New("no home directory") }
	screens["receipt save error"] = mustPress(t, noHome, "s")
	screens["cleanup review"] = mustPress(t, m, "c")

	errM := newModel(t, &fakeLoader{results: []fakeResult{{err: fmt.Errorf("discover: %w", brew.ErrNotFound)}}}, 100)
	screens["loading"] = errM
	screens["load error"] = runCmd(t, errM, errM.Init())
	return screens
}

func checkFits(t *testing.T, name string, m model, w, h int) string {
	t.Helper()
	v := m.View()
	lines := strings.Split(v, "\n")
	if len(lines) > h {
		t.Errorf("%s: %d lines, terminal has %d", name, len(lines), h)
	}
	for _, l := range lines {
		if lw := ansi.StringWidth(l); lw > w {
			t.Errorf("%s: line wider than %d (%d): %q", name, w, lw, l)
		}
		if strings.ContainsAny(ansi.Strip(l), "\a\b\t\r") {
			t.Errorf("%s: control character in %q", name, l)
		}
	}
	return v
}

func TestScreensFitTerminalSizes(t *testing.T) {
	screens := screenStates(t)
	for _, size := range [][2]int{{40, 10}, {60, 24}, {200, 50}} {
		w, h := size[0], size[1]
		t.Run(fmt.Sprintf("%dx%d", w, h), func(t *testing.T) {
			for name, s := range screens {
				s = resize(s, w, h)
				v := checkFits(t, name, s, w, h)
				// Scroll every scrollable screen to its end: still no overflow.
				end, _ := press(t, s, "G", "pgdown", "pgdown", "pgdown", "pgdown", "pgdown", "pgdown", "pgdown", "pgdown")
				if end.mode == s.mode && end.isHelpVisible == s.isHelpVisible {
					checkFits(t, name+" scrolled", end, w, h)
				}
				plain := ansi.Strip(v)
				switch name {
				case "running":
					if !strings.Contains(plain, "Running 1/2") || !strings.Contains(plain, "  bell back space red    tab") {
						t.Errorf("running at %dx%d hides the state line or the output pane:\n%s", w, h, plain)
					}
				case "cancel prompt":
					if !strings.Contains(plain, "Cancel the run?") || !strings.Contains(plain, "y cancel the run") {
						t.Errorf("cancel prompt hidden at %dx%d:\n%s", w, h, plain)
					}
				case "receipt save error":
					if !strings.Contains(plain, "[error] Could not save") {
						t.Errorf("save error hidden at %dx%d:\n%s", w, h, plain)
					}
				case "details stale", "list stale":
					if !strings.Contains(plain, "[stale") {
						t.Errorf("stale banner hidden at %dx%d:\n%s", w, h, plain)
					}
				}
			}
		})
	}
}

func TestResizeShrinksAndGrows(t *testing.T) {
	for name, s := range screenStates(t) {
		for _, size := range [][2]int{{200, 50}, {40, 10}, {1, 1}, {0, 0}, {60, 24}} {
			s = resize(s, size[0], size[1])
			if size[0] > 1 {
				checkFits(t, name, s, size[0], max(size[1], 3))
			} else {
				_ = s.View() // degenerate sizes must not panic
			}
		}
	}
}

func TestScrollOffsetsShrinkWhenTerminalGrows(t *testing.T) {
	m, _ := loaded(t, 40)
	m = resize(m, 40, 10)
	m, _ = press(t, m, "?")
	m, _ = press(t, m, "pgdown", "pgdown", "pgdown", "pgdown", "pgdown", "pgdown", "pgdown", "pgdown", "pgdown")
	if !strings.Contains(m.View(), "Nothing changes") {
		t.Fatalf("end of help not reachable at 40x10:\n%s", m.View())
	}
	if m = resize(m, 200, 50); m.helpScroll != 0 || !strings.Contains(m.View(), "Keys") {
		t.Errorf("help scroll %d kept after growing:\n%s", m.helpScroll, m.View())
	}
}

func TestDetailsScroll(t *testing.T) {
	m, _ := loaded(t, 40)
	m = resize(m, 40, 10)
	m, _ = press(t, m, "enter")
	if strings.Contains(m.View(), "Homepage") || !strings.Contains(m.View(), "j/k scroll") {
		t.Fatalf("details should need scrolling at 40x10:\n%s", m.View())
	}
	var seen strings.Builder
	for range 30 {
		m, _ = press(t, m, "j")
		seen.WriteString(m.View())
	}
	if !strings.Contains(seen.String(), "Homepage") || !strings.Contains(m.View(), "did not report") || m.mode != viewDetails {
		t.Errorf("end of details not reachable:\n%s", m.View())
	}
	if m, _ = press(t, m, "esc", "enter"); m.detailScroll != 0 {
		t.Errorf("details reopened scrolled to %d", m.detailScroll)
	}
}

// TestNoColorMarkers renders with the ASCII profile (as with NO_COLOR or a
// dumb terminal): every state must still read from its text marker.
func TestNoColorMarkers(t *testing.T) {
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.Ascii)
	t.Cleanup(func() { lipgloss.SetColorProfile(prev) })

	screens := screenStates(t)
	tests := []struct {
		screen, line, marker string
	}{
		{"list", "git", "↑"},
		{"list staged", "git", "[x]"},
		{"list staged", longName[:10], "[x]"},
		{"list stale", "showing inventory loaded at", "[stale"},
		{"list stale", "formulae", "[stale"},
		{"list error", "Refresh failed", "[error]"},
		{"list error", "formulae", "[error]"},
		{"details", "Outdated", "↑ yes"},
		{"running", "brew upgrade --formula git", "[running]"},
		{"receipt unverified", "Could not refresh", "[error]"},
		{"receipt unverified", "post-run inventory refresh failed", "NOT VERIFIED"},
		{"receipt save error", "Could not save", "[error]"},
		{"load error", "Homebrew not found", "[error]"},
	}
	for _, tt := range tests {
		v := screens[tt.screen].View()
		if strings.Contains(v, "\x1b[3") || strings.Contains(v, "\x1b[9") {
			t.Errorf("%s: colour escape under the ASCII profile: %q", tt.screen, v)
		}
		idx := slices.IndexFunc(strings.Split(ansi.Strip(v), "\n"), func(l string) bool {
			return strings.Contains(l, tt.line) && strings.Contains(l, tt.marker)
		})
		if idx < 0 {
			t.Errorf("%s: no line with %q carries marker %q:\n%s", tt.screen, tt.line, tt.marker, ansi.Strip(v))
		}
	}
}

func TestRunningScreenIgnoresUnrelatedLoadError(t *testing.T) {
	screens := screenStates(t)
	m := screens["running"]
	next, _ := m.Update(inventoryFailedMsg{err: errors.New("unrelated refresh failed")})
	got := next.(model)
	v := ansi.Strip(got.View())
	if got.mode != viewRunning || !got.sess.isRunning() || strings.Contains(v, "[error]") || strings.Contains(v, "unrelated") {
		t.Fatalf("load error replaced the running screen:\n%s", v)
	}
	for _, want := range []string{"Running 1/2", "Output", "bell back space red    tab", "[running]"} {
		if !strings.Contains(v, want) {
			t.Errorf("running screen lost %q after a load error:\n%s", want, v)
		}
	}
}

func TestReceiptSaveOutcomeIsPinnedUnderFooter(t *testing.T) {
	screens := screenStates(t)
	for _, tt := range []struct{ screen, want string }{
		{"receipt save error", "[error] Could not save the receipt: find home directory: no home directory · s retry"},
		{"receipt saved", "Saved to "},
	} {
		m := resize(screens[tt.screen], 60, 12)
		m, _ = press(t, m, "pgdown", "pgdown", "pgdown", "pgdown", "pgdown") // the outcome must survive scrolling
		if m.sess.scroll == 0 {
			t.Fatalf("%s: receipt should scroll at 60x12", tt.screen)
		}
		lines := strings.Split(ansi.Strip(m.View()), "\n")
		footer := slices.IndexFunc(lines, func(l string) bool { return strings.Contains(l, "s save · c cleanup") })
		if footer < 0 {
			t.Fatalf("%s: footer missing:\n%s", tt.screen, strings.Join(lines, "\n"))
		}
		below := strings.Join(strings.Fields(strings.Join(lines[footer+1:], " ")), " ")
		if !strings.Contains(below, strings.Join(strings.Fields(tt.want), " ")) {
			t.Errorf("%s: %q not under the footer:\n%s", tt.screen, tt.want, strings.Join(lines, "\n"))
		}
	}
}

func TestConfirmationWording(t *testing.T) {
	screens := screenStates(t)
	if v := ansi.Strip(screens["review"].View()); !strings.Contains(v, "y run these 2 commands · esc back") ||
		!strings.Contains(v, "Brew Board will run 2 commands") || !strings.Contains(v, "Nothing runs until you press y.") {
		t.Errorf("review confirmation unclear:\n%s", v)
	}
	v := ansi.Strip(screens["cancel prompt"].View())
	for _, want := range []string{cancelPrompt, "y interrupts the running Homebrew command and skips the remaining ones", "n keeps it running", "y cancel the run · n keep running"} {
		if !strings.Contains(v, want) {
			t.Errorf("cancel prompt missing %q:\n%s", want, v)
		}
	}
}

// handlerKeys extracts every key the key handlers match: string case labels
// of switches on msg.String() and tea.Key constants compared with msg.Type.
func handlerKeys(t *testing.T) map[string]bool {
	t.Helper()
	typeKeys := map[string]string{"KeyEsc": "esc", "KeyEnter": "enter", "KeyUp": "up", "KeyDown": "down", "KeyCtrlC": "ctrl+c"}
	keys := map[string]bool{}
	fset := token.NewFileSet()
	for _, file := range []string{"model.go", "session.go"} {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		f, err := parser.ParseFile(fset, file, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			isHandler := ok && fn.Body != nil && (strings.HasSuffix(fn.Name.Name, "Key") || fn.Name.Name == "scrollDelta")
			if !isHandler {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				switch n := n.(type) {
				case *ast.BasicLit:
					if n.Kind == token.STRING {
						if s, err := strconv.Unquote(n.Value); err == nil && isKeyLiteral(fn, n) {
							keys[s] = true
						}
					}
				case *ast.SelectorExpr:
					if id, ok := n.X.(*ast.Ident); ok && id.Name == "tea" {
						if k, ok := typeKeys[n.Sel.Name]; ok {
							keys[k] = true
						} else if strings.HasPrefix(n.Sel.Name, "Key") {
							t.Errorf("%s: unmapped key constant tea.%s", fn.Name.Name, n.Sel.Name)
						}
					}
				}
				return true
			})
		}
	}
	return keys
}

// isKeyLiteral reports whether lit is a case label (not, say, a notice).
func isKeyLiteral(fn *ast.FuncDecl, lit *ast.BasicLit) bool {
	found := false
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if cc, ok := n.(*ast.CaseClause); ok && slices.ContainsFunc(cc.List, func(e ast.Expr) bool { return e == lit }) {
			found = true
		}
		return !found
	})
	return found
}

func TestHelpListsEveryBinding(t *testing.T) {
	inCode := handlerKeys(t)
	inHelp := map[string]bool{}
	for _, k := range keyHelp {
		for _, key := range k.keys {
			inHelp[key] = true
		}
	}
	for k := range inCode {
		if !inHelp[k] {
			t.Errorf("key %q is handled but missing from keyHelp", k)
		}
	}
	for k := range inHelp {
		if !inCode[k] {
			t.Errorf("keyHelp lists %q, which no handler matches", k)
		}
	}
	for _, want := range []string{"j", "k", "g", "G", "/", "f", "o", "r", "enter", " ", "a", "u", "d", "y", "x", "s", "c", "?", "q", "ctrl+c", "esc", "n", "pgdown", "ctrl+d"} {
		if !inCode[want] {
			t.Errorf("expected binding %q not found in the handlers (extraction broken?)", want)
		}
	}

	m, _ := loaded(t, 200)
	m = resize(m, 200, 50)
	v := ansi.Strip(mustPress(t, m, "?").View())
	for _, k := range keyHelp {
		if !strings.Contains(v, k.label) || !strings.Contains(v, k.desc) {
			t.Errorf("help does not render %q: %q", k.label, k.desc)
		}
	}
}

func TestOutputControlsAreDropped(t *testing.T) {
	m := screenStates(t)["running"]
	if got := m.sess.output[len(m.sess.output)-1]; got != "bell back space red    tab" {
		t.Errorf("sanitized output = %q", got)
	}
}
