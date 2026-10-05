package plan

import (
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kaanemec/brew-board/internal/brew"
)

var now = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

func formula(name, installed, available string, outdated bool) brew.Package {
	return brew.Package{Name: name, Kind: brew.KindFormula, InstalledVersions: []string{installed},
		AvailableVersion: available, Outdated: outdated}
}

func cask(name, installed, available string, outdated bool) brew.Package {
	p := formula(name, installed, available, outdated)
	p.Kind = brew.KindCask
	return p
}

// upgrades selects every package for upgrade.
func upgrades(pkgs ...brew.Package) []Selection {
	out := make([]Selection, len(pkgs))
	for i, p := range pkgs {
		out[i] = Selection{Package: p, Op: OpUpgrade}
	}
	return out
}

func TestItemArgsCommandExplain(t *testing.T) {
	tests := []struct {
		name    string
		item    Item
		args    []string
		command string
		explain string
	}{
		{
			name:    "formula",
			item:    Item{Op: OpUpgrade, Package: formula("glib", "2.88.3", "2.90.0", true)},
			args:    []string{"upgrade", "--formula", "glib"},
			command: "brew upgrade --formula glib",
			explain: "Upgrade formula glib from 2.88.3 to 2.90.0. Homebrew may also upgrade dependencies.",
		},
		{
			name:    "tap formula keeps full name as one arg",
			item:    Item{Op: OpUpgrade, Package: formula("hashicorp/tap/terraform", "1.9", "1.10", true)},
			args:    []string{"upgrade", "--formula", "hashicorp/tap/terraform"},
			command: "brew upgrade --formula hashicorp/tap/terraform",
			explain: "Upgrade formula hashicorp/tap/terraform from 1.9 to 1.10. Homebrew may also upgrade dependencies.",
		},
		{
			name:    "cask",
			item:    Item{Op: OpUpgrade, Package: cask("firefox", "120.0", "121.0", true)},
			args:    []string{"upgrade", "--cask", "firefox"},
			command: "brew upgrade --cask firefox",
			explain: "Upgrade cask firefox from 120.0 to 121.0.",
		},
		{
			name:    "uninstall formula",
			item:    Item{Op: OpUninstall, Package: formula("glib", "2.90.0", "2.90.0", false)},
			args:    []string{"uninstall", "--formula", "glib"},
			command: "brew uninstall --formula glib",
			explain: "Remove formula glib (installed 2.90.0). Nothing installed depends on it.",
		},
		{
			name:    "uninstall formula with dependents removed earlier",
			item:    Item{Op: OpUninstall, Package: formula("glib", "2.90.0", "2.90.0", false), Dependents: []string{"cairo", "harfbuzz"}},
			args:    []string{"uninstall", "--formula", "glib"},
			command: "brew uninstall --formula glib",
			explain: "Remove formula glib (installed 2.90.0). cairo and harfbuzz depend on it and are removed earlier in this plan.",
		},
		{
			name:    "uninstall cask",
			item:    Item{Op: OpUninstall, Package: cask("firefox", "121", "121", false)},
			args:    []string{"uninstall", "--cask", "firefox"},
			command: "brew uninstall --cask firefox",
			explain: "Remove cask firefox (installed 121). Nothing installed depends on it.",
		},
		{
			name:    "autoremove",
			item:    Item{Op: OpAutoremove},
			args:    []string{"autoremove"},
			command: "brew autoremove",
			explain: "Remove formulae that were installed only as dependencies and are no longer needed by anything installed.",
		},
		{
			name:    "cleanup",
			item:    Item{Op: OpCleanup},
			args:    []string{"cleanup"},
			command: "brew cleanup",
			explain: "Remove old versions and cached downloads Homebrew no longer needs.",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.item.Args(); !slices.Equal(got, tt.args) {
				t.Errorf("Args() = %q, want %q", got, tt.args)
			}
			if got := tt.item.Command(); got != tt.command {
				t.Errorf("Command() = %q, want %q", got, tt.command)
			}
			if got := tt.item.Explain(); got != tt.explain {
				t.Errorf("Explain() = %q, want %q", got, tt.explain)
			}
		})
	}
}

func TestBuild(t *testing.T) {
	pinned := formula("node", "22", "23", true)
	pinned.Pinned = true
	tests := []struct {
		name     string
		selected []brew.Package
		wantErr  error
		errName  string
		wantArgs [][]string
	}{
		{name: "empty", selected: nil, wantErr: ErrEmpty},
		{
			name:     "keeps order",
			selected: []brew.Package{cask("firefox", "1", "2", true), formula("glib", "1", "2", true)},
			wantArgs: [][]string{{"upgrade", "--cask", "firefox"}, {"upgrade", "--formula", "glib"}},
		},
		{
			name:     "not outdated",
			selected: []brew.Package{formula("glib", "1", "2", true), formula("jq", "1.7", "1.7", false)},
			wantErr:  ErrNotUpgradable,
			errName:  "jq",
		},
		{
			name:     "duplicates collapse keeping first",
			selected: []brew.Package{formula("glib", "1", "2", true), cask("glib", "1", "2", true), formula("glib", "1", "3", true)},
			wantArgs: [][]string{{"upgrade", "--formula", "glib"}, {"upgrade", "--cask", "glib"}},
		},
		{name: "pinned", selected: []brew.Package{pinned}, wantErr: ErrNotUpgradable, errName: "node"},
		{
			name:     "unknown kind",
			selected: []brew.Package{{Name: "x", Outdated: true}},
			wantErr:  ErrNotUpgradable,
			errName:  "x",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := Build(upgrades(tt.selected...), brew.Inventory{}, now)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v, want %v", err, tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.errName) {
					t.Errorf("err %q does not name %q", err, tt.errName)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !p.CreatedAt.Equal(now) {
				t.Errorf("CreatedAt = %v", p.CreatedAt)
			}
			var got [][]string
			for _, it := range p.Items {
				if it.Op == OpCleanup {
					t.Errorf("Build added cleanup")
				}
				got = append(got, it.Args())
			}
			if !slices.EqualFunc(got, tt.wantArgs, slices.Equal[[]string]) {
				t.Errorf("args = %q, want %q", got, tt.wantArgs)
			}
		})
	}
}

func TestBuildDuplicateKeepsFirst(t *testing.T) {
	first := formula("glib", "1", "2", true)
	second := formula("glib", "1", "3", true)
	p, err := Build(upgrades(first, second), brew.Inventory{}, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Items) != 1 || p.Items[0].Package.AvailableVersion != "2" {
		t.Errorf("items = %+v, want only the first glib", p.Items)
	}
}

func TestWithCleanup(t *testing.T) {
	p, err := Build(upgrades(formula("glib", "1", "2", true)), brew.Inventory{}, now)
	if err != nil {
		t.Fatal(err)
	}
	c := p.WithCleanup().WithCleanup()
	if len(p.Items) != 1 {
		t.Errorf("original plan modified: %d items", len(p.Items))
	}
	if len(c.Items) != 2 || c.Items[1].Op != OpCleanup {
		t.Errorf("WithCleanup items = %+v", c.Items)
	}
}

func TestValidate(t *testing.T) {
	staged, err := Build(upgrades(
		formula("glib", "2.88.3", "2.90.0", true),
		cask("firefox", "120", "121", true),
	), brew.Inventory{}, now)
	if err != nil {
		t.Fatal(err)
	}
	staged = staged.WithCleanup()
	pinnedGlib := formula("glib", "2.88.3", "2.90.0", true)
	pinnedGlib.Pinned = true

	tests := []struct {
		name  string
		fresh brew.Inventory
		want  map[string]string // package name -> reason
	}{
		{
			name: "current",
			fresh: brew.Inventory{
				Formulae: []brew.Package{formula("glib", "2.88.3", "2.90.0", true)},
				Casks:    []brew.Package{cask("firefox", "120", "121", true)},
			},
			want: map[string]string{},
		},
		{
			name:  "missing",
			fresh: brew.Inventory{Formulae: []brew.Package{formula("glib", "2.88.3", "2.90.0", true)}},
			want:  map[string]string{"firefox": "no longer installed"},
		},
		{
			name: "same name different kind is missing",
			fresh: brew.Inventory{
				Formulae: []brew.Package{formula("glib", "2.88.3", "2.90.0", true), formula("firefox", "120", "121", true)},
			},
			want: map[string]string{"firefox": "no longer installed"},
		},
		{
			name: "pinned since staging",
			fresh: brew.Inventory{
				Formulae: []brew.Package{pinnedGlib},
				Casks:    []brew.Package{cask("firefox", "120", "121", true)},
			},
			want: map[string]string{"glib": "pinned since staging"},
		},
		{
			name: "up to date and changed",
			fresh: brew.Inventory{
				Formulae: []brew.Package{formula("glib", "2.90.0", "2.90.0", false)},
				Casks:    []brew.Package{cask("firefox", "120", "122", true)},
			},
			want: map[string]string{
				"glib":    "already up to date (installed 2.90.0)",
				"firefox": "available version changed from 121 to 122",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := map[string]string{}
			for _, pr := range staged.Validate(tt.fresh) {
				if pr.Item.Op == OpCleanup {
					t.Errorf("cleanup produced problem %q", pr.Reason)
				}
				got[pr.Item.Package.Name] = pr.Reason
			}
			if len(got) != len(tt.want) {
				t.Errorf("problems = %v, want %v", got, tt.want)
			}
			for name, reason := range tt.want {
				if got[name] != reason {
					t.Errorf("%s: reason = %q, want %q", name, got[name], reason)
				}
			}
		})
	}
}

// dep returns a formula installed only as a dependency, with direct dependencies.
func dep(name string, deps ...string) brew.Package {
	p := formula(name, "1", "1", false)
	p.Dependencies = deps
	return p
}

func uninstall(pkgs ...brew.Package) []Selection {
	out := make([]Selection, len(pkgs))
	for i, p := range pkgs {
		out[i] = Selection{Package: p, Op: OpUninstall}
	}
	return out
}

func names(items []Item) []string {
	var out []string
	for _, it := range items {
		out = append(out, string(it.Op)+":"+it.Package.Name)
	}
	return out
}

func TestBuildUninstallOrder(t *testing.T) {
	a, b, c := dep("a", "b"), dep("b", "c"), dep("c")
	other := dep("other")
	jq := formula("jq", "1", "2", true)
	inv := brew.Inventory{Formulae: []brew.Package{a, b, c, jq, other}}
	sel := append(uninstall(c, other, b), Selection{Package: jq, Op: OpUpgrade})
	sel = append(sel, uninstall(a, b)...) // b duplicated: collapsed
	p, err := Build(sel, inv, now)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"uninstall:other", "uninstall:a", "uninstall:b", "uninstall:c", "upgrade:jq"}
	if got := names(p.Items); !slices.Equal(got, want) {
		t.Errorf("order = %q, want %q", got, want)
	}
	if got := p.Items[3].Dependents; !slices.Equal(got, []string{"b"}) {
		t.Errorf("c.Dependents = %q, want [b]", got)
	}
	if got, want := p.Items[3].Explain(), "Remove formula c (installed 1). b depends on it and is removed earlier in this plan."; got != want {
		t.Errorf("Explain = %q, want %q", got, want)
	}
	if got := p.Items[0].Explain(); !strings.HasSuffix(got, "Nothing installed depends on it.") {
		t.Errorf("Explain = %q", got)
	}
	if len(p.Validate(inv)) != 0 {
		t.Errorf("Validate = %+v, want no problems when dependents are also removed", p.Validate(inv))
	}
}

func TestBuildUninstallUsesInventoryDependencies(t *testing.T) {
	// The selection snapshots carry no Dependencies; inv does.
	inv := brew.Inventory{Formulae: []brew.Package{dep("a", "b"), dep("b")}}
	p, err := Build(uninstall(formula("b", "1", "1", false), formula("a", "1", "1", false)), inv, now)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := names(p.Items), []string{"uninstall:a", "uninstall:b"}; !slices.Equal(got, want) {
		t.Errorf("order = %q, want %q", got, want)
	}
}

func TestBuildUninstallCaskBeforeFormula(t *testing.T) {
	jdk := dep("openjdk")
	tools := cask("android-commandlinetools", "1", "1", false)
	tools.Dependencies = []string{"openjdk"}
	// A formula never depends on a cask, even one sharing a dependency's name.
	sameName := cask("openjdk", "1", "1", false)
	inv := brew.Inventory{Formulae: []brew.Package{jdk}, Casks: []brew.Package{tools, sameName}}
	p, err := Build(uninstall(jdk, tools), inv, now)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := names(p.Items), []string{"uninstall:android-commandlinetools", "uninstall:openjdk"}; !slices.Equal(got, want) {
		t.Errorf("order = %q, want %q", got, want)
	}
	if got := Dependents(inv, jdk); !slices.Equal(got, []string{"android-commandlinetools"}) {
		t.Errorf("Dependents = %q", got)
	}
}

func TestTransitiveDependents(t *testing.T) {
	// x → y → d: x's keg closure includes d although it declares only y.
	x, y, d := dep("x", "y"), dep("y", "d"), dep("d")
	x.RuntimeDependencies = []string{"d", "y"}
	y.RuntimeDependencies = []string{"d"}
	x.InstalledOnRequest = true
	inv := brew.Inventory{Formulae: []brew.Package{d, x, y}}

	if got, want := Dependents(inv, d), []string{"x", "y"}; !slices.Equal(got, want) {
		t.Errorf("Dependents(d) = %q, want %q", got, want)
	}
	p, err := Build(uninstall(d), inv, now)
	if err != nil {
		t.Fatal(err)
	}
	probs := p.Validate(inv)
	if len(probs) != 1 || probs[0].Reason != "required by x, y — mark them for removal too, or keep d" {
		t.Errorf("Validate(d only) = %+v", probs)
	}
	// y is removed too, but x still needs d through its closure.
	p, err = Build(uninstall(d, y), inv, now)
	if err != nil {
		t.Fatal(err)
	}
	if probs := p.Validate(inv); len(probs) != 2 || !strings.HasPrefix(probs[0].Reason, "required by x") {
		t.Errorf("Validate(d, y) = %+v", probs)
	}

	p, err = Build(uninstall(d, y, x), inv, now)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := names(p.Items), []string{"uninstall:x", "uninstall:y", "uninstall:d"}; !slices.Equal(got, want) {
		t.Errorf("order = %q, want %q", got, want)
	}
	if probs := p.Validate(inv); len(probs) != 0 {
		t.Errorf("Validate(x, y, d) = %+v", probs)
	}

	// Without a closure (casks, or no keg information) direct Dependencies count.
	y.RuntimeDependencies = nil
	if got := Dependents(brew.Inventory{Formulae: []brew.Package{d, y}}, d); !slices.Equal(got, []string{"y"}) {
		t.Errorf("Dependents fallback = %q", got)
	}
}

func TestDependentsMatchKind(t *testing.T) {
	// A docker formula and a docker cask share a name.
	formulaDocker := dep("docker")
	caskDocker := cask("docker", "1", "1", false)
	compose := dep("docker-compose", "docker") // formula: needs the formula
	desktopTool := cask("desktop-tool", "1", "1", false)
	desktopTool.Dependencies = []string{"docker"}
	desktopTool.CaskDependencies = []string{"docker"} // needs the cask
	cliTool := cask("cli-tool", "1", "1", false)
	cliTool.Dependencies = []string{"docker"} // depends_on formula docker
	inv := brew.Inventory{
		Formulae: []brew.Package{formulaDocker, compose},
		Casks:    []brew.Package{caskDocker, cliTool, desktopTool},
	}
	if got, want := Dependents(inv, formulaDocker), []string{"cli-tool", "docker-compose"}; !slices.Equal(got, want) {
		t.Errorf("Dependents(formula docker) = %q, want %q", got, want)
	}
	if got, want := Dependents(inv, caskDocker), []string{"desktop-tool"}; !slices.Equal(got, want) {
		t.Errorf("Dependents(cask docker) = %q, want %q", got, want)
	}
	p, err := Build(uninstall(caskDocker, desktopTool), inv, now)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := names(p.Items), []string{"uninstall:desktop-tool", "uninstall:docker"}; !slices.Equal(got, want) {
		t.Errorf("order = %q, want %q", got, want)
	}
	if probs := p.Validate(inv); len(probs) != 0 {
		t.Errorf("removing the cask and its cask dependent: Validate = %+v", probs)
	}
	p, err = Build(uninstall(formulaDocker), inv, now)
	if err != nil {
		t.Fatal(err)
	}
	if probs := p.Validate(inv); len(probs) != 1 || probs[0].Reason != "required by cli-tool, docker-compose — mark them for removal too, or keep docker" {
		t.Errorf("Validate(formula docker) = %+v", probs)
	}
}

func TestBuildErrors(t *testing.T) {
	glib := formula("glib", "1", "2", true)
	tests := []struct {
		name     string
		selected []Selection
		wantErr  error
	}{
		{name: "conflict upgrade then uninstall", selected: append(upgrades(glib), uninstall(glib)...), wantErr: ErrConflict},
		{name: "conflict uninstall then upgrade", selected: append(uninstall(glib), upgrades(glib)...), wantErr: ErrConflict},
		{name: "unknown op", selected: []Selection{{Package: glib, Op: OpCleanup}}, wantErr: ErrInvalidSelection},
		{name: "uninstall unknown kind", selected: uninstall(brew.Package{Name: "glib"}), wantErr: ErrInvalidSelection},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Build(tt.selected, brew.Inventory{}, now)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if !strings.Contains(err.Error(), "glib") {
				t.Errorf("err %q does not name glib", err)
			}
		})
	}
	// Same package, different kinds, different ops: no conflict.
	if _, err := Build(append(upgrades(glib), uninstall(cask("glib", "1", "1", false))...), brew.Inventory{}, now); err != nil {
		t.Errorf("formula upgrade + cask uninstall: %v", err)
	}
	// Uninstall does not require the package to be outdated, but brew
	// refuses to uninstall a pinned formula.
	if _, err := Build(uninstall(formula("node", "22", "22", false)), brew.Inventory{}, now); err != nil {
		t.Errorf("uninstall up to date: %v", err)
	}
	pinned := formula("node", "22", "22", false)
	pinned.Pinned = true
	_, err := Build(uninstall(pinned), brew.Inventory{}, now)
	if !errors.Is(err, ErrPinned) || errors.Is(err, ErrNotUpgradable) || err.Error() != "formula node: pinned; unpin it first with brew unpin" {
		t.Errorf("uninstall pinned: err = %v, want ErrPinned", err)
	}
	// Pinned after staging: Validate blocks it.
	p, err := Build(uninstall(formula("node", "22", "22", false)), brew.Inventory{}, now)
	if err != nil {
		t.Fatal(err)
	}
	if probs := p.Validate(brew.Inventory{Formulae: []brew.Package{pinned}}); len(probs) != 1 || probs[0].Reason != ErrPinned.Error() {
		t.Errorf("Validate pinned since staging = %+v", probs)
	}
}

func TestValidateUninstall(t *testing.T) {
	glib, cairo, harfbuzz := dep("glib"), dep("cairo", "glib"), dep("harfbuzz", "glib")
	gimp := cask("gimp", "1", "1", false)
	gimp.Dependencies = []string{"cairo"}
	inv := brew.Inventory{Formulae: []brew.Package{cairo, glib, harfbuzz}, Casks: []brew.Package{gimp}}

	tests := []struct {
		name     string
		selected []Selection
		fresh    brew.Inventory
		want     map[string]string
		explain  map[string]string
	}{
		{
			name:     "blocked by dependents",
			selected: uninstall(glib),
			fresh:    inv,
			want:     map[string]string{"glib": "required by cairo, harfbuzz — mark them for removal too, or keep glib"},
			explain:  map[string]string{"glib": "Remove formula glib (installed 1). cairo and harfbuzz depend on it and stay installed."},
		},
		{
			name:     "partly selected dependents still block",
			selected: uninstall(glib, harfbuzz),
			fresh:    inv,
			want:     map[string]string{"glib": "required by cairo — mark them for removal too, or keep glib"},
			explain:  map[string]string{"glib": "Remove formula glib (installed 1). cairo depends on it and stays installed."},
		},
		{
			name:     "cask dependent blocks a formula",
			selected: uninstall(glib, harfbuzz, cairo),
			fresh:    inv,
			want:     map[string]string{"cairo": "required by gimp — mark them for removal too, or keep cairo"},
		},
		{
			name:     "all dependents selected",
			selected: uninstall(glib, harfbuzz, cairo, gimp),
			fresh:    inv,
			want:     map[string]string{},
			explain: map[string]string{
				"glib": "Remove formula glib (installed 1). cairo and harfbuzz depend on it and are removed earlier in this plan.",
				"gimp": "Remove cask gimp (installed 1). Nothing installed depends on it.",
			},
		},
		{
			name:     "already uninstalled",
			selected: uninstall(harfbuzz),
			fresh:    brew.Inventory{Formulae: []brew.Package{cairo, glib}},
			want:     map[string]string{"harfbuzz": "already uninstalled"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := Build(tt.selected, inv, now)
			if err != nil {
				t.Fatal(err)
			}
			got := map[string]string{}
			for _, pr := range p.WithAutoremove().WithCleanup().Validate(tt.fresh) {
				got[pr.Item.Package.Name] = pr.Reason
			}
			if !maps.Equal(got, tt.want) {
				t.Errorf("problems = %q, want %q", got, tt.want)
			}
			for _, it := range p.Items {
				if want, ok := tt.explain[it.Package.Name]; ok && it.Explain() != want {
					t.Errorf("Explain = %q, want %q", it.Explain(), want)
				}
			}
		})
	}
}

func TestOrphans(t *testing.T) {
	app := dep("app", "lib", "shared")
	app.InstalledOnRequest = true
	keeper := dep("keeper", "shared")
	keeper.InstalledOnRequest = true
	inv := brew.Inventory{Formulae: []brew.Package{
		app,
		dep("lib", "base"),  // freed when app goes
		dep("base", "core"), // freed once lib goes
		dep("core"),         // end of the chain
		dep("shared"),       // still needed by keeper
		dep("stray"),        // already unneeded: autoremove would take it too
		dep("caskdep"),      // needed by a cask
		keeper,
	}}
	c := cask("tool", "1", "1", false)
	c.Dependencies = []string{"caskdep"}
	inv.Casks = []brew.Package{c}

	// An empty plan previews what brew autoremove would remove now.
	if got, want := Orphans(inv, Plan{}), []string{"stray"}; !slices.Equal(got, want) {
		t.Errorf("Orphans(empty plan) = %q, want %q", got, want)
	}
	// A formula needed only through a keg's runtime closure is not an orphan.
	closure := inv
	keeper2 := dep("keeper2")
	keeper2.InstalledOnRequest, keeper2.RuntimeDependencies = true, []string{"stray"}
	closure.Formulae = append(slices.Clone(inv.Formulae), keeper2)
	if got := Orphans(closure, Plan{}); len(got) != 0 {
		t.Errorf("Orphans with stray in a closure = %q, want none", got)
	}

	p, err := Build(uninstall(app), inv, now)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := Orphans(inv, p), []string{"base", "core", "lib", "stray"}; !slices.Equal(got, want) {
		t.Errorf("Orphans = %q, want %q", got, want)
	}
	// Removing the cask frees its formula dependency.
	p, err = Build(uninstall(c), inv, now)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := Orphans(inv, p), []string{"caskdep", "stray"}; !slices.Equal(got, want) {
		t.Errorf("Orphans = %q, want %q", got, want)
	}
	// A dependency-only formula that is itself selected is not an orphan.
	p, err = Build(uninstall(dep("stray")), inv, now)
	if err != nil {
		t.Fatal(err)
	}
	if got := Orphans(inv, p); len(got) != 0 {
		t.Errorf("Orphans = %q, want none", got)
	}
}

func TestWithAutoremove(t *testing.T) {
	p, err := Build(uninstall(dep("a")), brew.Inventory{}, now)
	if err != nil {
		t.Fatal(err)
	}
	got := p.WithCleanup().WithAutoremove().WithAutoremove()
	if want := []string{"uninstall:a", "autoremove:", "cleanup:"}; !slices.Equal(names(got.Items), want) {
		t.Errorf("items = %q, want %q", names(got.Items), want)
	}
	got = p.WithAutoremove().WithCleanup()
	if want := []string{"uninstall:a", "autoremove:", "cleanup:"}; !slices.Equal(names(got.Items), want) {
		t.Errorf("items = %q, want %q", names(got.Items), want)
	}
	if len(p.Items) != 1 {
		t.Errorf("original plan modified: %d items", len(p.Items))
	}
	if probs := got.Validate(brew.Inventory{Formulae: []brew.Package{dep("a")}}); len(probs) != 0 {
		t.Errorf("Validate = %+v", probs)
	}
}
