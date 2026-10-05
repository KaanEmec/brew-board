package plan

import (
	"errors"
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
			p, err := Build(tt.selected, now)
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
	p, err := Build([]brew.Package{first, second}, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Items) != 1 || p.Items[0].Package.AvailableVersion != "2" {
		t.Errorf("items = %+v, want only the first glib", p.Items)
	}
}

func TestWithCleanup(t *testing.T) {
	p, err := Build([]brew.Package{formula("glib", "1", "2", true)}, now)
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
	staged, err := Build([]brew.Package{
		formula("glib", "2.88.3", "2.90.0", true),
		cask("firefox", "120", "121", true),
	}, now)
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
