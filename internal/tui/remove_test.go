package tui

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"

	"github.com/kaanemec/brew-board/internal/brew"
	"github.com/kaanemec/brew-board/internal/plan"
	"github.com/kaanemec/brew-board/internal/run"
)

// depInventory is the fixture plus a small dependency graph:
// yt-dlp → ffmpeg → lame, x264 (lame and x264 installed only as
// dependencies), and the cask gimp → gettext. Every other formula was
// installed on request, so nothing is an orphan before a removal.
func depInventory() brew.Inventory {
	inv := fixtureInventory()
	for i := range inv.Formulae {
		inv.Formulae[i].InstalledOnRequest = true
	}
	f := func(name string, deps []string, onRequest bool) brew.Package {
		return brew.Package{Name: name, Kind: brew.KindFormula, InstalledVersions: []string{"1.0"}, AvailableVersion: "1.0",
			Dependencies: deps, InstalledOnRequest: onRequest}
	}
	ytdlp := f("yt-dlp", []string{"ffmpeg"}, true)
	ytdlp.InstalledVersions, ytdlp.AvailableVersion, ytdlp.Outdated = []string{"2025.1"}, "2025.2", true
	inv.Formulae = append(inv.Formulae,
		f("ffmpeg", []string{"lame", "x264"}, true), f("gettext", nil, true),
		f("lame", nil, false), f("x264", nil, false), ytdlp)
	inv.Casks = append(inv.Casks, brew.Package{Name: "gimp", Kind: brew.KindCask, InstalledVersions: []string{"3.0"},
		AvailableVersion: "3.1", Outdated: true, Dependencies: []string{"gettext"}})
	sortInv(&inv)
	return inv
}

// withoutPkgs returns inv without the named packages.
func withoutPkgs(inv brew.Inventory, names ...string) brew.Inventory {
	drop := func(p brew.Package) bool { return slices.Contains(names, p.Name) }
	inv.Formulae = slices.DeleteFunc(slices.Clone(inv.Formulae), drop)
	inv.Casks = slices.DeleteFunc(slices.Clone(inv.Casks), drop)
	return inv
}

func sortInv(inv *brew.Inventory) {
	byName := func(a, b brew.Package) int { return strings.Compare(a.Name, b.Name) }
	slices.SortFunc(inv.Formulae, byName)
	slices.SortFunc(inv.Casks, byName)
}

// markRemoval moves the cursor to each named package and presses d.
func markRemoval(t *testing.T, m model, pkgs ...string) model {
	t.Helper()
	for _, name := range pkgs {
		m = cursorTo(t, m, name)
		m, _ = press(t, m, "d")
	}
	return m
}

var (
	gitKey    = pkgKey{name: "git", kind: brew.KindFormula}
	jqKey     = pkgKey{name: "jq", kind: brew.KindFormula}
	ffmpegKey = pkgKey{name: "ffmpeg", kind: brew.KindFormula}
)

// flat joins the words of the plain view so wrapped text can be matched.
func flat(v string) string { return strings.Join(strings.Fields(ansi.Strip(v)), " ") }

func commands(p plan.Plan) []string {
	out := make([]string, 0, len(p.Items))
	for _, it := range p.Items {
		out = append(out, it.Command())
	}
	return out
}

func TestRemovalMarkToggles(t *testing.T) {
	m, _ := loaded(t, 100)
	m = markRemoval(t, m, "jq")
	if !m.removing[jqKey] || len(m.selected) != 0 {
		t.Fatalf("d should mark jq for removal: removing=%v selected=%v", m.removing, m.selected)
	}
	v := m.View()
	for _, want := range []string{"[-] jq", "[ ] git", "1 to remove"} {
		if !strings.Contains(v, want) {
			t.Errorf("view missing %q:\n%s", want, v)
		}
	}
	if strings.Contains(v, "[ ] wget") || strings.Contains(v, "[-] wget") || strings.Contains(v, "to upgrade") {
		t.Errorf("unmarked up-to-date rows stay blank; no upgrade count:\n%s", v)
	}
	if m = markRemoval(t, m, "jq"); len(m.removing) != 0 {
		t.Errorf("second d should clear the mark: %v", m.removing)
	}

	// d and space switch a package between the two marks.
	m = stage(t, m, "git")
	m = markRemoval(t, m, "git")
	if m.selected[gitKey] || !m.removing[gitKey] {
		t.Fatalf("d on an upgrade row: selected=%v removing=%v", m.selected, m.removing)
	}
	if v := m.View(); !strings.Contains(v, "[-] git") || strings.Contains(v, "[x] git") {
		t.Errorf("git should render as a removal:\n%s", v)
	}
	m = stage(t, m, "git")
	if !m.selected[gitKey] || m.removing[gitKey] {
		t.Fatalf("space on a removal row: selected=%v removing=%v", m.selected, m.removing)
	}
	m = markRemoval(t, m, "jq")
	if v := m.View(); !strings.Contains(v, "1 to upgrade · 1 to remove") || !strings.Contains(v, "[x] git") {
		t.Errorf("status should count both marks:\n%s", v)
	}

	// space cannot turn a removal of an up-to-date package into an upgrade.
	m = stage(t, m, "jq")
	if !m.removing[jqKey] || m.selected[jqKey] || !strings.Contains(m.notice, "jq is up to date") {
		t.Errorf("space on up-to-date removal: removing=%v selected=%v notice=%q", m.removing, m.selected, m.notice)
	}

	// a stays upgrade-only and leaves removal marks alone.
	m = markRemoval(t, m, "git")
	m, _ = press(t, m, "a")
	vsc := pkgKey{name: "visual-studio-code", kind: brew.KindCask}
	if !m.removing[gitKey] || m.selected[gitKey] || !m.selected[vsc] || len(m.selected) != 1 {
		t.Errorf("a with git marked for removal: selected=%v removing=%v", m.selected, m.removing)
	}

	t.Run("narrow", func(t *testing.T) {
		m, _ := loaded(t, 40)
		if v := markRemoval(t, m, "jq").View(); !strings.Contains(v, "- jq") || strings.Contains(v, "[-]") {
			t.Errorf("narrow list should mark removal with -:\n%s", v)
		}
	})
}

func TestNoColorRemovalMarker(t *testing.T) {
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.Ascii)
	t.Cleanup(func() { lipgloss.SetColorProfile(prev) })
	m, _ := loaded(t, 100)
	if v := markRemoval(t, m, "jq").View(); !strings.Contains(v, "[-] jq") || strings.Contains(v, "\x1b[") {
		t.Errorf("removal must read without colour:\n%q", v)
	}
}

func TestRemovalNoticeNamesUnmarkedDependents(t *testing.T) {
	m := sessionModel(t, &fakeLoader{results: []fakeResult{{inv: depInventory()}}}, &fakeExecutor{}, 100)
	if m = markRemoval(t, m, "ffmpeg"); !strings.Contains(m.notice, "ffmpeg is required by yt-dlp") {
		t.Errorf("notice = %q", m.notice)
	}
	m = markRemoval(t, m, "ffmpeg", "yt-dlp")
	if m = markRemoval(t, m, "ffmpeg"); m.notice != "" || !m.removing[ffmpegKey] {
		t.Errorf("dependent already marked: notice=%q removing=%v", m.notice, m.removing)
	}
}

func TestRemovalRefusesPinned(t *testing.T) {
	m, _ := loaded(t, 100)
	m = markRemoval(t, m, "node")
	nodeKey := pkgKey{name: "node", kind: brew.KindFormula}
	if m.removing[nodeKey] || m.notice != "node is pinned; brew unpin node to allow removal" {
		t.Errorf("d on a pinned row: removing=%v notice=%q", m.removing, m.notice)
	}
	// A pinned removal that reaches the review is dropped, not the whole plan.
	m.removing = map[pkgKey]bool{nodeKey: true, jqKey: true}
	m = review(t, m)
	want := "node: pinned; unpin it first with brew unpin — removed from plan"
	if !slices.Contains(m.sess.problems, want) || !slices.Equal(commands(m.sess.plan), []string{"brew uninstall --formula jq"}) {
		t.Errorf("problems = %q plan = %q", m.sess.problems, commands(m.sess.plan))
	}
}

func TestRemovalNoticeMatchesKind(t *testing.T) {
	// A docker formula and a docker cask; desktop-tool depends_on the cask.
	inv := fixtureInventory()
	inv.Formulae = append(inv.Formulae, brew.Package{Name: "docker", Kind: brew.KindFormula, InstalledVersions: []string{"28"}, InstalledOnRequest: true})
	inv.Casks = append(inv.Casks,
		brew.Package{Name: "docker", Kind: brew.KindCask, InstalledVersions: []string{"4.40"}},
		brew.Package{Name: "desktop-tool", Kind: brew.KindCask, InstalledVersions: []string{"1"},
			Dependencies: []string{"docker"}, CaskDependencies: []string{"docker"}})
	sortInv(&inv)
	m := sessionModel(t, &fakeLoader{results: []fakeResult{{inv: inv}}}, &fakeExecutor{}, 100)
	pressOn := func(m model, k pkgKey) model {
		t.Helper()
		i := slices.IndexFunc(m.visible, func(p brew.Package) bool { return keyOf(p) == k })
		if i < 0 {
			t.Fatalf("%v not visible", k)
		}
		m.cursor = i
		m, _ = press(t, m, "d")
		return m
	}
	formulaDocker := pkgKey{name: "docker", kind: brew.KindFormula}
	caskDocker := pkgKey{name: "docker", kind: brew.KindCask}
	if m = pressOn(m, formulaDocker); !m.removing[formulaDocker] || m.notice != "" {
		t.Errorf("formula docker has no dependents: notice=%q", m.notice)
	}
	if m = pressOn(m, caskDocker); m.notice != "docker is required by desktop-tool; mark them too or the review drops it" {
		t.Errorf("cask docker: notice=%q", m.notice)
	}
	m = pressOn(m, caskDocker) // clear
	m = markRemoval(t, m, "desktop-tool")
	if m = pressOn(m, caskDocker); m.notice != "" || !m.removing[caskDocker] {
		t.Errorf("cask docker with its dependent marked: notice=%q", m.notice)
	}
	if v := mustPress(t, cursorTo(t, m, "desktop-tool"), "enter"); len(v.detailNeeders) != 0 {
		t.Errorf("desktop-tool required by %q", v.detailNeeders)
	}
}

func TestDetailsShowDependencies(t *testing.T) {
	m := sessionModel(t, &fakeLoader{results: []fakeResult{{inv: depInventory()}}}, &fakeExecutor{}, 100)
	field := func(v, label string) string {
		t.Helper()
		for _, l := range strings.Split(ansi.Strip(v), "\n") {
			if i := strings.Index(l, label); i >= 0 {
				return strings.Trim(strings.TrimSpace(l[i+len(label):]), "│ ")
			}
		}
		t.Fatalf("no %q line:\n%s", label, v)
		return ""
	}
	tests := []struct{ pkg, dependsOn, requiredBy string }{
		{"ffmpeg", "lame, x264", "yt-dlp"},
		{"lame", missing, "ffmpeg"},
		{"gettext", missing, "gimp"},
		{"jq", missing, missing},
	}
	for _, tt := range tests {
		v := mustPress(t, cursorTo(t, m, tt.pkg), "enter").View()
		if got := field(v, "Depends on"); got != tt.dependsOn {
			t.Errorf("%s: Depends on %q, expected %q", tt.pkg, got, tt.dependsOn)
		}
		if got := field(v, "Required by"); got != tt.requiredBy {
			t.Errorf("%s: Required by %q, expected %q", tt.pkg, got, tt.requiredBy)
		}
	}
}

func TestReviewBlocksRemovalWithUnmarkedDependent(t *testing.T) {
	loader := &fakeLoader{results: []fakeResult{{inv: depInventory()}}}
	exec := &fakeExecutor{}
	m := sessionModel(t, loader, exec, 100)
	m = review(t, stage(t, markRemoval(t, m, "ffmpeg"), "yt-dlp"))

	want := "ffmpeg: required by yt-dlp — mark them for removal too, or keep ffmpeg — removed from plan"
	if !slices.Contains(m.sess.problems, want) {
		t.Errorf("problems = %q, expected %q", m.sess.problems, want)
	}
	if got := commands(m.sess.plan); !slices.Equal(got, []string{"brew upgrade --formula yt-dlp"}) {
		t.Errorf("plan = %q, expected only the yt-dlp upgrade", got)
	}
	if m.removing[ffmpegKey] || !m.selected[pkgKey{name: "yt-dlp", kind: brew.KindFormula}] {
		t.Errorf("blocked removal should lose its mark only: removing=%v selected=%v", m.removing, m.selected)
	}
	v := flat(m.View())
	for _, s := range []string{"Dropped from the plan:", want, "Upgrade (1)", "y run this command · esc back"} {
		if !strings.Contains(v, s) {
			t.Errorf("review missing %q:\n%s", s, v)
		}
	}
	if strings.Contains(v, "Remove (") || strings.Contains(v, removalWarning) {
		t.Errorf("no removal is left, so no removal group or warning:\n%s", v)
	}

	t.Run("dropping a dependent blocks its dependencies", func(t *testing.T) {
		// ffmpeg's dependent yt-dlp is marked too, but yt-dlp is gone from the
		// fresh inventory while a new dependent appeared: both removals drop.
		fresh := depInventory()
		fresh.Formulae = append(fresh.Formulae, brew.Package{Name: "mpv", Kind: brew.KindFormula,
			InstalledVersions: []string{"0.40"}, Dependencies: []string{"ffmpeg"}, InstalledOnRequest: true})
		sortInv(&fresh)
		m := sessionModel(t, &fakeLoader{results: []fakeResult{{inv: depInventory()}, {inv: fresh}}}, exec, 100)
		m = review(t, markRemoval(t, m, "ffmpeg", "yt-dlp"))
		if len(m.sess.plan.Items) != 1 || m.sess.plan.Items[0].Package.Name != "yt-dlp" {
			t.Errorf("plan = %q, expected only yt-dlp", commands(m.sess.plan))
		}
		if !slices.ContainsFunc(m.sess.problems, func(p string) bool { return strings.HasPrefix(p, "ffmpeg: required by mpv") }) {
			t.Errorf("problems = %q", m.sess.problems)
		}
	})

	if exec.calls() != 0 {
		t.Errorf("nothing may run without y: %d calls", exec.calls())
	}
}

func TestReviewOrdersDependentsFirst(t *testing.T) {
	exec := &fakeExecutor{}
	m := sessionModel(t, &fakeLoader{results: []fakeResult{{inv: depInventory()}}}, exec, 100)
	m = stage(t, markRemoval(t, m, "ffmpeg", "yt-dlp"), "git")
	m = review(t, m)
	want := []string{"brew uninstall --formula yt-dlp", "brew uninstall --formula ffmpeg", "brew upgrade --formula git"}
	if got := commands(m.sess.plan); !slices.Equal(got, want) || len(m.sess.problems) != 0 {
		t.Fatalf("plan = %q problems = %q, expected %q", got, m.sess.problems, want)
	}
	plain := ansi.Strip(m.View())
	order := []string{"Remove (2)", " 1. " + want[0], " 2. " + want[1], "Upgrade (1)", " 3. " + want[2], removalWarning[:40], "y run these 3 commands · esc back"}
	last := -1
	for _, s := range order {
		i := strings.Index(plain, s)
		if i <= last {
			t.Errorf("%q missing or out of order (at %d, previous at %d):\n%s", s, i, last, plain)
		}
		last = i
	}
	if !strings.Contains(flat(plain), removalWarning) {
		t.Errorf("missing removal warning:\n%s", plain)
	}
	for _, s := range []string{"--force", "--ignore-dependencies"} {
		for _, it := range m.sess.plan.Items {
			if slices.Contains(it.Args(), s) {
				t.Errorf("%s passes %s", it.Command(), s)
			}
		}
	}
	if exec.calls() != 0 {
		t.Fatal("nothing may run before y")
	}
	_, execute, _ := confirm(t, m)
	execute()
	if got := commands(exec.plans[0]); !slices.Equal(got, want) {
		t.Errorf("executed %q, expected %q", got, want)
	}
}

func TestConflictNeverReachesBuild(t *testing.T) {
	m := sessionModel(t, &fakeLoader{results: []fakeResult{{inv: depInventory()}}}, &fakeExecutor{}, 100)
	m = cursorTo(t, m, "git")
	for _, k := range []string{"space", "d", "space", "d", "d", "space"} {
		m, _ = press(t, m, k)
		if m.selected[gitKey] && m.removing[gitKey] {
			t.Fatalf("after %s git is marked for both", k)
		}
	}
	m = markRemoval(t, m, "git")
	m = review(t, m)
	if got := commands(m.sess.plan); !slices.Equal(got, []string{"brew uninstall --formula git"}) || len(m.sess.problems) != 0 {
		t.Errorf("plan = %q problems = %q", got, m.sess.problems)
	}

	// Should both marks ever be set, the review drops the package instead of
	// letting plan.Build fail with ErrConflict.
	m, _ = press(t, m, "esc")
	m.selected, m.removing = map[pkgKey]bool{gitKey: true}, map[pkgKey]bool{gitKey: true, jqKey: true}
	m = review(t, m)
	want := "git: marked for both upgrade and removal — removed from plan"
	if !slices.Contains(m.sess.problems, want) || slices.ContainsFunc(m.sess.problems, func(p string) bool { return strings.Contains(p, "cannot build plan") }) {
		t.Errorf("problems = %q, expected %q", m.sess.problems, want)
	}
	if got := commands(m.sess.plan); !slices.Equal(got, []string{"brew uninstall --formula jq"}) || m.selected[gitKey] || m.removing[gitKey] {
		t.Errorf("plan = %q selected=%v removing=%v", got, m.selected, m.removing)
	}
}

// removalSession marks pkgs for removal, reviews, confirms, and runs to the
// receipt; the post-run inventory no longer has them.
func removalSession(t *testing.T, exec *fakeExecutor, width int, pkgs ...string) model {
	t.Helper()
	after := withoutPkgs(depInventory(), pkgs...)
	loader := &fakeLoader{results: []fakeResult{{inv: depInventory()}, {inv: depInventory()}, {inv: after}}}
	m := sessionModel(t, loader, exec, width)
	m = review(t, markRemoval(t, m, pkgs...))
	m, execute, wait := confirm(t, m)
	execute()
	return drain(t, m, wait)
}

func TestReceiptOffersAutoremove(t *testing.T) {
	exec := &fakeExecutor{}
	m := removalSession(t, exec, 100, "ffmpeg", "yt-dlp")
	if m.mode != viewReceipt || m.sess.receipt.Summary.Removed != 2 {
		t.Fatalf("mode=%v summary=%+v", m.mode, m.sess.receipt.Summary)
	}
	orphanLine := "2 formulae are installed only as dependencies and nothing installed needs them: lame, x264 — press a to review brew autoremove (Homebrew decides the final list)."
	v := flat(m.View())
	for _, want := range []string{"0 upgraded · 2 removed · 0 failed", orphanLine, "s save · c cleanup · a autoremove"} {
		if !strings.Contains(v, want) {
			t.Errorf("receipt missing %q:\n%s", want, v)
		}
	}

	m, cmd := press(t, m, "a")
	if cmd != nil || m.mode != viewReview || exec.calls() != 1 {
		t.Fatalf("a: cmd=%v mode=%v calls=%d", cmd != nil, m.mode, exec.calls())
	}
	v = flat(m.View())
	for _, want := range []string{"brew autoremove", "y run this command · esc back", "Nothing runs until you press y."} {
		if !strings.Contains(v, want) {
			t.Errorf("autoremove review missing %q:\n%s", want, v)
		}
	}
	if m, _ = press(t, m, "esc"); m.mode != viewReceipt || m.sess.plan.Items[0].Op != plan.OpUninstall || !strings.Contains(flat(m.View()), orphanLine) {
		t.Fatalf("esc should return to the removal receipt: mode=%v", m.mode)
	}

	// brew autoremove takes lame and x264 with it.
	loader := m.loader.(*fakeLoader)
	loader.results = append(loader.results, fakeResult{inv: withoutPkgs(depInventory(), "ffmpeg", "yt-dlp", "lame", "x264")})
	m, _ = press(t, m, "a")
	m, execute, wait := confirm(t, m)
	execute()
	m = drain(t, m, wait)
	if exec.calls() != 2 || !slices.Equal(commands(exec.plans[1]), []string{"brew autoremove"}) {
		t.Fatalf("autoremove plan = %q", commands(exec.plans[1]))
	}
	if v := flat(m.View()); m.mode != viewReceipt || !strings.Contains(v, "brew autoremove: completed") || strings.Contains(v, "a autoremove") {
		t.Errorf("autoremove receipt should not offer autoremove again:\n%s", v)
	}
	if m, _ = press(t, m, "a"); m.mode != viewReceipt || m.notice == "" {
		t.Errorf("a without orphans: mode=%v notice=%q", m.mode, m.notice)
	}

	t.Run("no orphans, no offer", func(t *testing.T) {
		exec := &fakeExecutor{}
		m := removalSession(t, exec, 100, "jq")
		if v := flat(m.View()); m.sess.receipt.Summary.Removed != 1 || strings.Contains(v, "nothing installed needs") || strings.Contains(v, "a autoremove") {
			t.Errorf("receipt offers autoremove without orphans:\n%s", v)
		}
		if m, _ = press(t, m, "a"); m.mode != viewReceipt || exec.calls() != 1 {
			t.Errorf("a without orphans: mode=%v calls=%d", m.mode, exec.calls())
		}
	})

	t.Run("one orphan reads singular", func(t *testing.T) {
		inv := depInventory()
		m := sessionModel(t, &fakeLoader{results: []fakeResult{{inv: inv}}}, &fakeExecutor{}, 100)
		after := withoutPkgs(inv, "gimp")
		for i := range after.Formulae { // gettext was pulled in only for gimp
			if after.Formulae[i].Name == "gettext" {
				after.Formulae[i].InstalledOnRequest = false
			}
		}
		m.inv = after
		m.sess.before = inv
		m.sess.plan = plan.Plan{Items: []plan.Item{{Op: plan.OpUninstall, Package: brew.Package{Name: "gimp", Kind: brew.KindCask}}}}
		m.sess.results = completeAllResults(m.sess.plan)
		if got := m.orphans(); !slices.Equal(got, []string{"gettext"}) {
			t.Fatalf("orphans = %q", got)
		}
		m.sess.isFinished = true
		m = m.openReceipt(nil)
		if v := flat(m.View()); !strings.Contains(v, "1 formula is installed only as a dependency and nothing installed needs it: gettext — press a to review brew autoremove") {
			t.Errorf("singular orphan line missing:\n%s", v)
		}
	})

	t.Run("completed removal still installed frees nothing", func(t *testing.T) {
		// brew reported success for ffmpeg, but the post-run inventory still
		// has it (uncertain), so lame and x264 are still needed.
		inv := depInventory()
		m := sessionModel(t, &fakeLoader{results: []fakeResult{{inv: inv}}}, &fakeExecutor{}, 100)
		m.sess.before = inv
		m.sess.plan = plan.Plan{Items: []plan.Item{
			{Op: plan.OpUninstall, Package: brew.Package{Name: "yt-dlp", Kind: brew.KindFormula}},
			{Op: plan.OpUninstall, Package: brew.Package{Name: "ffmpeg", Kind: brew.KindFormula}},
		}}
		m.sess.results = completeAllResults(m.sess.plan)
		m.inv = withoutPkgs(inv, "yt-dlp")
		if got := m.orphans(); len(got) != 0 {
			t.Errorf("orphans = %q, want none while ffmpeg is installed", got)
		}
	})

	t.Run("offered after an upgrade-only run", func(t *testing.T) {
		// stray was dependency-only and unneeded before the run.
		inv := depInventory()
		inv.Formulae = append(inv.Formulae, brew.Package{Name: "stray", Kind: brew.KindFormula, InstalledVersions: []string{"1"}})
		sortInv(&inv)
		exec := &fakeExecutor{}
		m := sessionModel(t, &fakeLoader{results: []fakeResult{{inv: inv}}}, exec, 100)
		m = review(t, stage(t, m, "git"))
		m, execute, wait := confirm(t, m)
		execute()
		m = drain(t, m, wait)
		if v := flat(m.View()); m.mode != viewReceipt || !strings.Contains(v, "1 formula is installed only as a dependency and nothing installed needs it: stray") ||
			!strings.Contains(v, "a autoremove") {
			t.Fatalf("upgrade receipt should offer autoremove:\n%s", v)
		}
		if m, _ = press(t, m, "a"); m.mode != viewReview || !slices.Equal(commands(m.sess.plan), []string{"brew autoremove"}) {
			t.Errorf("a: mode=%v plan=%q", m.mode, commands(m.sess.plan))
		}
	})
}

// completeAllResults reports every item of p as completed with exit 0.
func completeAllResults(p plan.Plan) []run.Result {
	out := make([]run.Result, len(p.Items))
	for i, it := range p.Items {
		out[i] = run.Result{Item: it, Args: it.Args(), Status: run.StatusCompleted, Started: fixedTime, Ended: fixedTime}
	}
	return out
}

func TestRemovalScreensFit(t *testing.T) {
	for _, width := range []int{60, 120} {
		t.Run(fmt.Sprintf("width %d", width), func(t *testing.T) {
			loader := &fakeLoader{results: []fakeResult{{inv: depInventory()}}}
			m := sessionModel(t, loader, &fakeExecutor{}, width)
			screens := map[string]model{}
			marked := stage(t, markRemoval(t, m, "ffmpeg", "jq"), "git")
			screens["list marked"] = marked
			screens["details deps"] = mustPress(t, cursorTo(t, m, "ffmpeg"), "enter")
			screens["review blocked"] = review(t, marked)
			screens["review mixed"] = review(t, stage(t, markRemoval(t, m, "ffmpeg", "yt-dlp", "gettext", "gimp"), "git"))
			receipt := removalSession(t, &fakeExecutor{}, width, "ffmpeg", "yt-dlp")
			screens["receipt orphans"] = receipt
			screens["autoremove review"] = mustPress(t, receipt, "a")
			for name, s := range screens {
				for _, h := range []int{30, 10} {
					checkFits(t, fmt.Sprintf("%s %dx%d", name, width, h), resize(s, width, h), width, h)
				}
			}
			if v := flat(screens["receipt orphans"].View()); !strings.Contains(v, "a autoremove") {
				t.Errorf("receipt footer at %d hides the autoremove key:\n%s", width, v)
			}
		})
	}
}
