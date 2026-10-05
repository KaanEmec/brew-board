package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/kaanemec/brew-board/internal/brew"
)

// largeInventory generates n packages with long, multi-byte names (up to 60
// runes), long descriptions, and a mix of kinds and states.
func largeInventory(n int) brew.Inventory {
	stems := []string{"lib", "ünïcødé", "日本語ツール", "emoji-📦", "very-long-formula-name", "x"}
	inv := brew.Inventory{BrewPath: "/opt/homebrew/bin/brew", BrewVersion: "4.6.0", LoadedAt: fixedTime}
	for i := range n {
		name := fmt.Sprintf("%s-%05d", stems[i%len(stems)], i)
		for len([]rune(name)) < 20+i%41 {
			name += "-" + stems[(i/7)%len(stems)]
		}
		name = string([]rune(name)[:min(len([]rune(name)), 60)])
		p := brew.Package{
			Name:              name,
			Kind:              brew.KindFormula,
			Description:       strings.Repeat(fmt.Sprintf("Description %d with ünïcødé and 日本語 text. ", i), 1+i%5),
			Homepage:          "https://example.com/" + name,
			InstalledVersions: []string{fmt.Sprintf("1.%d.0", i%97)},
			AvailableVersion:  fmt.Sprintf("1.%d.0", i%97),
			Pinned:            i%53 == 0,
		}
		if i%3 == 0 {
			p.Outdated = true
			p.AvailableVersion = fmt.Sprintf("2.%d.0-very-long-version-string", i%97)
		}
		if i%4 == 0 {
			p.Kind = brew.KindCask
			inv.Casks = append(inv.Casks, p)
			continue
		}
		inv.Formulae = append(inv.Formulae, p)
	}
	return inv
}

func largeModel(tb testing.TB, n, width int) model {
	tb.Helper()
	loader := &fakeLoader{results: []fakeResult{{inv: largeInventory(n)}}}
	m := New(loader, WithClock(func() time.Time { return fixedTime })).(model)
	next, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: 40})
	m = next.(model)
	next, _ = m.Update(m.Init()())
	m = next.(model)
	if len(m.visible) != n {
		tb.Fatalf("visible = %d, expected %d", len(m.visible), n)
	}
	return m
}

var (
	keyDown = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")}
	keyUp   = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")}
)

// navigate presses j then k so the cursor stays put across iterations.
func navigate(m model) model {
	next, _ := m.Update(keyDown)
	next, _ = next.Update(keyUp)
	return next.(model)
}

func BenchmarkLargeInventory(b *testing.B) {
	for _, width := range []int{60, 120} {
		m := largeModel(b, 5000, width)
		m = navigate(m)
		b.Run(fmt.Sprintf("View/width=%d", width), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_ = m.View()
			}
		})
		b.Run(fmt.Sprintf("Navigate/width=%d", width), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				m = navigate(m)
			}
		})
	}
	m := largeModel(b, 5000, 120)
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
	m = next.(model)
	b.Run("SearchKeystroke", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
			next, _ = next.Update(tea.KeyMsg{Type: tea.KeyBackspace})
			_ = next
		}
	})
}

// TestLargeInventoryStaysResponsive checks that navigation and rendering cost
// depends on the rows on screen, not on the inventory size, and that only
// search and filter keys rebuild the visible list.
func TestLargeInventoryStaysResponsive(t *testing.T) {
	for _, width := range []int{60, 120} {
		t.Run(fmt.Sprintf("width %d", width), func(t *testing.T) {
			small, large := largeModel(t, 500, width), largeModel(t, 5000, width) // same kind of rows on screen

			// refilter always allocates a fresh visible slice, so an unchanged
			// backing array proves navigation did not re-filter.
			before := &large.visible[0]
			if got := navigate(navigate(large)); &got.visible[0] != before {
				t.Error("j/k navigation rebuilt the visible list")
			}
			if got, _ := press(t, large, "/", "x"); &got.visible[0] == before {
				t.Error("a search keystroke did not rebuild the visible list")
			}

			allocs := func(m model, f func(model)) float64 { return testing.AllocsPerRun(20, func() { f(m) }) }
			view := func(m model) { _ = m.View() }
			nav := func(m model) { _ = navigate(m) }
			if s, l := allocs(small, view), allocs(large, view); l > s*1.2+20 {
				t.Errorf("View allocations grow with the inventory: %v for 500 packages, %v for 5000", s, l)
			}
			if s, l := allocs(small, nav), allocs(large, nav); l > s+2 {
				t.Errorf("navigation allocations grow with the inventory: %v for 500 packages, %v for 5000", s, l)
			}

			// A generous bound that holds under -race; the benchmark has the real numbers.
			const runs = 20
			start := time.Now()
			for range runs {
				_ = navigate(large).View()
			}
			if per := time.Since(start) / runs; per > 20*time.Millisecond {
				t.Errorf("keypress plus View took %v with 5000 packages", per)
			}
		})
	}
}
