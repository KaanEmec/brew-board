// Command screenshots renders the README screenshots of Brew Board. It drives
// the real TUI model with an invented sample inventory and a scripted
// executor (Homebrew is never run or read) and writes one ANSI frame per
// screen to docs/screenshots/<name>.ansi; `make screenshots` turns them into
// PNGs with charmbracelet/freeze.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/kaanemec/brew-board/internal/brew"
	"github.com/kaanemec/brew-board/internal/plan"
	"github.com/kaanemec/brew-board/internal/run"
	"github.com/kaanemec/brew-board/internal/tui"
)

const outDir = "docs/screenshots"

var (
	start = time.Date(2026, 10, 5, 9, 41, 0, 0, time.UTC)
	now   = start // advanced between screens; read by the model's clock
)

// formula builds an installed-on-request formula; installed lists its kegs,
// comma-separated, and deps are both its direct and runtime dependencies.
func formula(name, desc, installed, available string, deps ...string) brew.Package {
	kegs := strings.Split(installed, ",")
	return brew.Package{
		Name: name, Kind: brew.KindFormula, Description: desc, Tap: "homebrew/core",
		Homepage: "https://formulae.brew.sh/formula/" + name, InstalledOnRequest: true,
		InstalledVersions: kegs, AvailableVersion: available, Outdated: available != kegs[len(kegs)-1],
		Dependencies: deps, RuntimeDependencies: deps,
	}
}

// dep is a formula installed only as a dependency.
func dep(name, desc, installed, available string, deps ...string) brew.Package {
	p := formula(name, desc, installed, available, deps...)
	p.InstalledOnRequest = false
	return p
}

func cask(name, display, desc, installed, available string) brew.Package {
	return brew.Package{
		Name: name, Kind: brew.KindCask, DisplayName: display, Description: desc, Tap: "homebrew/cask",
		Homepage:          "https://formulae.brew.sh/cask/" + name,
		InstalledVersions: []string{installed}, AvailableVersion: available, Outdated: installed != available,
	}
}

func sampleInventory() brew.Inventory {
	node := formula("node", "Open-source, cross-platform JavaScript runtime environment", "22.20.0,24.9.0", "24.10.0", "icu4c@78", "openssl@3")
	node.Pinned = true
	vscode := cask("visual-studio-code", "Microsoft Visual Studio Code", "Open-source code editor", "1.104.2", "1.105.0")
	vscode.AutoUpdates = true
	return brew.Inventory{
		BrewPath: "/opt/homebrew/bin/brew", BrewVersion: "4.6.16", LoadedAt: now,
		Formulae: []brew.Package{
			dep("cairo", "Vector graphics library with cross-device output support", "1.18.4", "1.18.4", "freetype", "glib", "libpng"),
			formula("curl", "Get a file from an HTTP, HTTPS or FTP server", "8.16.0", "8.16.0", "openssl@3"),
			dep("freetype", "Software library to render fonts", "2.14.1", "2.14.1", "libpng"),
			formula("gh", "GitHub command-line tool", "2.80.0", "2.81.0"),
			formula("git", "Distributed revision control system", "2.51.0", "2.51.1", "pcre2"),
			dep("glib", "Core application library for C", "2.86.0", "2.86.0", "pcre2"),
			formula("go", "Open source programming language to build simple/reliable/efficient software", "1.25.1", "1.25.2"),
			formula("graphviz", "Graph visualization software from AT&T and Bell Labs", "13.1.2", "13.1.2", "cairo", "glib", "harfbuzz", "pango"),
			dep("harfbuzz", "OpenType text shaping engine", "11.5.0", "12.1.0", "cairo", "freetype", "glib", "icu4c@78"),
			dep("icu4c@78", "C/C++ and Java libraries for Unicode and globalization", "78.1", "78.1"),
			formula("jq", "Lightweight and flexible command-line JSON processor", "1.8.1", "1.8.1", "oniguruma"),
			dep("libidn2", "International domain name library (IDNA2008, Punycode and TR46)", "2.3.8", "2.3.8", "libunistring"),
			dep("libpng", "Library for manipulating PNG images", "1.6.50", "1.6.50"),
			dep("libunistring", "C string library for manipulating Unicode strings", "1.3", "1.3"),
			node,
			dep("oniguruma", "Regular expressions library", "6.9.10", "6.9.10"),
			dep("openssl@3", "Cryptography and SSL/TLS Toolkit", "3.5.3", "3.5.4"),
			dep("pango", "Framework for layout and rendering of i18n text", "1.57.0", "1.57.0", "cairo", "glib", "harfbuzz"),
			dep("pcre2", "Perl compatible regular expressions library with a new API", "10.46", "10.46"),
			formula("python@3.13", "Interpreted, interactive, object-oriented programming language", "3.13.6,3.13.7", "3.13.8", "openssl@3"),
			formula("ripgrep", "Search tool like grep and The Silver Searcher", "14.1.1", "15.0.0", "pcre2"),
			formula("wget", "Internet file retriever", "1.25.0", "1.25.0", "libidn2", "openssl@3"),
		},
		Casks: []brew.Package{
			cask("docker-desktop", "Docker Desktop", "App to build and share containerised applications", "4.46.0", "4.47.0"),
			cask("firefox", "Mozilla Firefox", "Web browser", "143.0.4", "143.0.4"),
			cask("iterm2", "iTerm2", "Terminal emulator as alternative to Apple's Terminal app", "3.6.4", "3.6.4"),
			cask("raycast", "Raycast", "Control your tools with a few keystrokes", "1.103.2", "1.103.2"),
			cask("rectangle", "Rectangle", "Move and resize windows using keyboard shortcuts or snap areas", "0.91", "0.91"),
			vscode,
		},
	}
}

// afterRun is the inventory once wget is removed and git, go and ripgrep upgraded.
func afterRun(inv brew.Inventory) brew.Inventory {
	inv.Formulae = slices.DeleteFunc(slices.Clone(inv.Formulae), func(p brew.Package) bool { return p.Name == "wget" })
	for i, p := range inv.Formulae {
		if p.Name == "git" || p.Name == "go" || p.Name == "ripgrep" {
			inv.Formulae[i].InstalledVersions, inv.Formulae[i].Outdated = []string{p.AvailableVersion}, false
		}
	}
	return inv
}

// loader serves the sample inventory, or the post-run one once a run finished.
type loader struct{ ran bool }

func (l *loader) Load(context.Context) (brew.Inventory, error) {
	inv := sampleInventory()
	if l.ran {
		return afterRun(inv), nil
	}
	return inv, nil
}

// executor plays a scripted run: item 1 completes, item 2 starts and prints
// some output, then it waits for resume before finishing the rest.
type executor struct{ resume chan struct{} }

func (e *executor) Execute(_ context.Context, p plan.Plan, events chan<- run.Event) []run.Result {
	results := make([]run.Result, len(p.Items))
	output := [][]string{
		{"Uninstalling /opt/homebrew/Cellar/wget/1.25.0... (92 files, 4.6MB)"},
		{"==> Upgrading 1 outdated package:", "git 2.51.0 -> 2.51.1", "==> Fetching downloads for: git",
			"==> Downloading https://ghcr.io/v2/homebrew/core/git/manifests/2.51.1",
			"==> Downloading https://ghcr.io/v2/homebrew/core/git/blobs/sha256:4f1c9e0a7b52",
			"==> Upgrading git", "  2.51.0 -> 2.51.1"},
	}
	for i, it := range p.Items {
		t0 := start.Add(time.Duration(10+20*i) * time.Second)
		events <- run.ItemStarted{Index: i}
		lines := []string{"==> Upgrading " + it.Package.Name}
		if i < len(output) {
			lines = output[i]
		}
		for _, l := range lines {
			events <- run.OutputLine{Index: i, Line: l}
		}
		results[i] = run.Result{Item: it, Args: it.Args(), Status: run.StatusCompleted, Started: t0, Ended: t0.Add(14 * time.Second)}
		if i == 1 {
			<-e.resume // pause mid-item for the running screenshot
		}
		events <- run.ItemFinished{Index: i, Result: results[i]}
	}
	events <- run.Finished{Results: results}
	return results
}

// eventsBeforePause is how many events the script sends before it waits:
// item 1 (start, one line, finish) and item 2 (start, seven lines).
const eventsBeforePause = 3 + 1 + 7

// step runs cmd and feeds its message to the model.
func step(m tea.Model, cmd tea.Cmd) (tea.Model, tea.Cmd) { return m.Update(cmd()) }

// drain runs commands until the model returns none.
func drain(m tea.Model, cmd tea.Cmd) tea.Model {
	for cmd != nil {
		m, cmd = step(m, cmd)
	}
	return m
}

// press sends keys; "space" is the space bar, anything else is typed.
func press(m tea.Model, keys ...string) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	for _, k := range keys {
		msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
		if k == "space" {
			msg = tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}}
		}
		m, cmd = m.Update(msg)
	}
	return m, cmd
}

// cursorTo moves the list cursor to name; the list is sorted by name.
func cursorTo(m tea.Model, name string) tea.Model {
	all := sampleInventory().All()
	slices.SortStableFunc(all, func(a, b brew.Package) int { return strings.Compare(a.Name, b.Name) })
	i := slices.IndexFunc(all, func(p brew.Package) bool { return p.Name == name })
	if i < 0 {
		log.Fatalf("no sample package %q", name)
	}
	m, _ = press(m, "g")
	for range i {
		m, _ = press(m, "j")
	}
	return m
}

func save(name string, m tea.Model) {
	path := filepath.Join(outDir, name+".ansi")
	if err := os.WriteFile(path, []byte(m.View()), 0o644); err != nil {
		log.Fatal(err)
	}
	fmt.Println("wrote", path)
}

func main() {
	lipgloss.SetColorProfile(termenv.TrueColor)
	lipgloss.SetHasDarkBackground(true)
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		log.Fatal(err)
	}

	l := &loader{}
	exec := &executor{resume: make(chan struct{})}
	m := tui.New(l, tui.WithExecutor(exec), tui.WithClock(func() time.Time { return now }),
		// freeze's font has no Braille, so the default MiniDot would draw as boxes.
		tui.WithSpinner(spinner.Spinner{Frames: []string{"◌", "●"}, FPS: time.Second / 4}),
		tui.WithHomeDir(func() (string, error) { return os.TempDir(), nil }))
	m, _ = m.Update(tea.WindowSizeMsg{Width: 110, Height: 32})
	m = drain(m, m.Init())

	for _, name := range []string{"git", "go", "ripgrep"} {
		m, _ = press(cursorTo(m, name), "space")
	}
	m, _ = press(cursorTo(m, "wget"), "d")
	m = cursorTo(m, "gh")
	save("list", m)

	details, _ := cursorTo(m, "harfbuzz").Update(tea.KeyMsg{Type: tea.KeyEnter})
	save("details", details)

	m = drain(press(m, "c"))
	save("review", m)

	// y starts the executor, the event wait and the spinner tick.
	now = start.Add(10 * time.Second)
	m, cmd := press(m, "y")
	batch, ok := cmd().(tea.BatchMsg)
	if !ok || len(batch) != 3 {
		log.Fatalf("y: unexpected command %#v", cmd)
	}
	executed := make(chan struct{})
	go func() { batch[0](); close(executed) }()
	wait := batch[1]
	for range eventsBeforePause {
		m, wait = step(m, wait)
	}
	m, _ = step(m, batch[2]) // one spinner tick: the glyph shows its second frame
	save("running", m)

	now = start.Add(75 * time.Second)
	l.ran = true // the loader is next called for the post-run refresh
	close(exec.resume)
	m = drain(m, wait)
	<-executed
	save("receipt", m)
}
