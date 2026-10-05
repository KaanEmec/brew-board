package brew

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
)

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return data
}

func findPkg(t *testing.T, pkgs []Package, name string) Package {
	t.Helper()
	for _, p := range pkgs {
		if p.Name == name {
			return p
		}
	}
	t.Fatalf("package %q not found", name)
	return Package{}
}

func TestParseInfo(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		input        []byte
		wantFormulae int
		wantCasks    int
		wantErr      error
	}{
		{name: "small fixture", input: readFixture(t, "info_small.json"), wantFormulae: 6, wantCasks: 3},
		{name: "empty inventory", input: readFixture(t, "info_empty.json")},
		{name: "casks only", input: []byte(`{"casks":[]}`)},
		{name: "truncated json", input: readFixture(t, "malformed.json"), wantErr: ErrMalformed},
		{name: "empty input", input: nil, wantErr: ErrMalformed},
		{name: "top-level array", input: []byte(`[]`), wantErr: ErrMalformed},
		{name: "object without known keys", input: []byte(`{"other":[]}`), wantErr: ErrMalformed},
		{name: "null keys", input: []byte(`{"formulae":null,"casks":null}`), wantErr: ErrMalformed},
		{name: "wrong field type", input: []byte(`{"formulae":[{"name":5}]}`), wantErr: ErrMalformed},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			formulae, casks, err := parseInfo(tc.input)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(formulae) != tc.wantFormulae || len(casks) != tc.wantCasks {
				t.Errorf("got %d formulae, %d casks; want %d, %d", len(formulae), len(casks), tc.wantFormulae, tc.wantCasks)
			}
		})
	}
}

func TestParseInfoMapping(t *testing.T) {
	t.Parallel()

	formulae, casks, err := parseInfo(readFixture(t, "info_small.json"))
	if err != nil {
		t.Fatalf("parseInfo: %v", err)
	}

	tests := []struct {
		name string
		pkgs []Package
		want Package
	}{
		{
			name: "ca-certificates",
			pkgs: formulae,
			want: Package{
				Name: "ca-certificates", Kind: KindFormula,
				Tap:               "homebrew/core",
				InstalledVersions: []string{"2026-07-16", "2026-08-13"},
				AvailableVersion:  "2026-09-25",
				Outdated:          true,
			},
		},
		{
			name: "go",
			pkgs: formulae,
			want: Package{
				Name: "go", Kind: KindFormula,
				Tap:                "homebrew/core",
				InstalledVersions:  []string{"1.27.1"},
				AvailableVersion:   "1.27.1",
				InstalledOnRequest: true,
			},
		},
		{
			name: "hashicorp/tap/terraform",
			pkgs: formulae,
			want: Package{
				Name: "hashicorp/tap/terraform", Kind: KindFormula,
				Tap:                "hashicorp/tap",
				InstalledVersions:  []string{"1.9.5"},
				AvailableVersion:   "1.9.8",
				Outdated:           true,
				InstalledOnRequest: true,
			},
		},
		{
			name: "cursor",
			pkgs: casks,
			want: Package{
				Name: "cursor", Kind: KindCask, DisplayName: "Cursor",
				Tap:               "homebrew/cask",
				InstalledVersions: []string{"3.9.8,4aa8ff1b7877ed7bd01bcba308698f71a6735380"},
				AvailableVersion:  "3.23.12,2d29876d567da1607532b23bbf2cd5ddbca496fe",
				Outdated:          true,
				AutoUpdates:       true,
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := findPkg(t, tc.pkgs, tc.name)
			// Description and homepage text come from brew; only check they
			// are mapped, so a wording change upstream is not a mapping bug.
			if got.Description == "" || got.Homepage == "" {
				t.Errorf("description %q or homepage %q is empty", got.Description, got.Homepage)
			}
			got.Description, got.Homepage = "", ""
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got  %+v\nwant %+v", got, tc.want)
			}
		})
	}

	if p := findPkg(t, formulae, "ada-url"); p.InstalledOnRequest {
		t.Errorf("ada-url InstalledOnRequest = true, want false (dependency)")
	}
	if p := findPkg(t, casks, "android-commandlinetools"); p.AutoUpdates || p.Outdated {
		t.Errorf("android-commandlinetools AutoUpdates=%v Outdated=%v, want false/false", p.AutoUpdates, p.Outdated)
	}
	if p := findPkg(t, casks, "brave-browser"); !p.AutoUpdates || p.Outdated {
		t.Errorf("brave-browser AutoUpdates=%v Outdated=%v, want true/false", p.AutoUpdates, p.Outdated)
	}
}

func TestParseInfoDependencies(t *testing.T) {
	t.Parallel()

	formulae, casks, err := parseInfo(readFixture(t, "info_small.json"))
	if err != nil {
		t.Fatalf("parseInfo: %v", err)
	}
	fixture := []struct {
		name string
		pkgs []Package
		want []string
	}{
		// ada-url's keg lists only fmt; the declared simdutf is not used.
		{name: "ada-url", pkgs: formulae, want: []string{"fmt"}},
		// node's keg also lists fmt, but only transitively (declared_directly false).
		{name: "node", pkgs: formulae, want: []string{
			"ada-url", "brotli", "c-ares", "hdrhistogram_c", "icu4c@78", "libffi", "libnghttp2",
			"libnghttp3", "libngtcp2", "libuv", "llhttp", "merve", "nbytes", "openssl@3",
			"simdjson", "sqlite", "uvwasi", "zstd",
		}},
		{name: "go", pkgs: formulae},
		{name: "android-commandlinetools", pkgs: casks, want: []string{"openjdk", "temurin"}},
		// depends_on with only a macos requirement has no package dependencies.
		{name: "cursor", pkgs: casks},
	}
	for _, tc := range fixture {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := findPkg(t, tc.pkgs, tc.name).Dependencies; !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Dependencies = %q, want %q", got, tc.want)
			}
		})
	}

	inline := []struct {
		name  string
		input string
		want  []string
	}{
		{
			name:  "formula without keg info falls back to declared dependencies",
			input: `{"formulae":[{"name":"a","dependencies":["z","b","b"],"installed":[]}]}`,
			want:  []string{"b", "z"},
		},
		{
			name:  "keg without runtime_dependencies falls back",
			input: `{"formulae":[{"name":"a","dependencies":["b"],"installed":[{"version":"1"}]}]}`,
			want:  []string{"b"},
		},
		{
			name: "empty runtime_dependencies means none, not fallback",
			input: `{"formulae":[{"name":"a","dependencies":["b"],` +
				`"installed":[{"version":"1","runtime_dependencies":[]}]}]}`,
		},
		{
			name: "newest keg without linked_keg; missing declared_directly kept; tap full names",
			input: `{"formulae":[{"name":"a","installed":[` +
				`{"version":"1","runtime_dependencies":[{"full_name":"old"}]},` +
				`{"version":"2","runtime_dependencies":[{"full_name":"user/tap/c"},{"full_name":"b","declared_directly":true},{"full_name":"b"}]}]}]}`,
			want: []string{"b", "user/tap/c"},
		},
		{
			name: "linked keg preferred over the newest",
			input: `{"formulae":[{"name":"a","linked_keg":"1","installed":[` +
				`{"version":"1","runtime_dependencies":[{"full_name":"linked"}]},` +
				`{"version":"2","runtime_dependencies":[{"full_name":"newest"}]}]}]}`,
			want: []string{"linked"},
		},
		{
			name: "linked_keg naming no installed keg falls back to the newest",
			input: `{"formulae":[{"name":"a","linked_keg":"9","installed":[` +
				`{"version":"1","runtime_dependencies":[{"full_name":"old"}]},` +
				`{"version":"2","runtime_dependencies":[{"full_name":"newest"}]}]}]}`,
			want: []string{"newest"},
		},
		{
			name:  "cask depends_on strings and lists merged",
			input: `{"casks":[{"token":"x","depends_on":{"cask":"y","formula":["b","a"],"macos":{">=":["13"]}}}]}`,
			want:  []string{"a", "b", "y"},
		},
		{
			name:  "cask depends_on of an unexpected shape is ignored",
			input: `{"casks":[{"token":"x","depends_on":["a"]}]}`,
		},
		{
			name:  "cask depends_on null",
			input: `{"casks":[{"token":"x","depends_on":null}]}`,
		},
	}
	for _, tc := range inline {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			formulae, casks, err := parseInfo([]byte(tc.input))
			if err != nil {
				t.Fatalf("parseInfo: %v", err)
			}
			all := append(formulae, casks...)
			if len(all) != 1 {
				t.Fatalf("got %d packages, want 1", len(all))
			}
			if got := all[0].Dependencies; !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Dependencies = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCurrentKeg(t *testing.T) {
	t.Parallel()

	var p infoPayload
	if err := json.Unmarshal(readFixture(t, "info_small.json"), &p); err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(*p.Formulae, func(f jsonFormula) bool { return f.Name == "ca-certificates" })
	if i < 0 {
		t.Fatal("ca-certificates not in fixture")
	}
	f := (*p.Formulae)[i]
	if len(f.Installed) != 2 {
		t.Fatalf("fixture has %d ca-certificates kegs, want 2", len(f.Installed))
	}
	// Kegs are listed oldest first; linked_keg names the newer one.
	if k := f.currentKeg(); k != 1 || f.Installed[k].Version != "2026-08-13" {
		t.Errorf("currentKeg = %d, want 1 (2026-08-13)", k)
	}
	f.LinkedKeg = ""
	if k := f.currentKeg(); k != 1 {
		t.Errorf("currentKeg without linked_keg = %d, want the last keg (1)", k)
	}
	f.LinkedKeg = "2026-07-16"
	if k := f.currentKeg(); k != 0 {
		t.Errorf("currentKeg linked to the older keg = %d, want 0", k)
	}
	if k := (jsonFormula{}).currentKeg(); k != -1 {
		t.Errorf("currentKeg with no kegs = %d, want -1", k)
	}
}

func TestParseInfoRuntimeAndCaskDependencies(t *testing.T) {
	t.Parallel()

	formulae, casks, err := parseInfo(readFixture(t, "info_small.json"))
	if err != nil {
		t.Fatalf("parseInfo: %v", err)
	}
	// node's closure includes fmt, which it only needs transitively.
	node := findPkg(t, formulae, "node")
	if !slices.Contains(node.RuntimeDependencies, "fmt") || slices.Contains(node.Dependencies, "fmt") {
		t.Errorf("node: RuntimeDependencies %q should include fmt, Dependencies %q should not", node.RuntimeDependencies, node.Dependencies)
	}
	for _, d := range node.Dependencies {
		if !slices.Contains(node.RuntimeDependencies, d) {
			t.Errorf("node: direct dependency %s missing from RuntimeDependencies", d)
		}
	}
	if got := findPkg(t, casks, "android-commandlinetools"); got.RuntimeDependencies != nil || !reflect.DeepEqual(got.CaskDependencies, []string{"temurin"}) {
		t.Errorf("android-commandlinetools: RuntimeDependencies %q, CaskDependencies %q", got.RuntimeDependencies, got.CaskDependencies)
	}

	inline := []struct {
		name                   string
		input                  string
		runtime, caskDeps, all []string
	}{
		{
			name: "closure of the linked keg",
			input: `{"formulae":[{"name":"a","linked_keg":"1","installed":[` +
				`{"version":"1","runtime_dependencies":[{"full_name":"c","declared_directly":false},{"full_name":"b","declared_directly":true}]},` +
				`{"version":"2","runtime_dependencies":[{"full_name":"z"}]}]}]}`,
			runtime: []string{"b", "c"},
			all:     []string{"b"},
		},
		{
			name:  "no keg information means no closure",
			input: `{"formulae":[{"name":"a","dependencies":["b"],"installed":[{"version":"1"}]}]}`,
			all:   []string{"b"},
		},
		{
			name:     "cask dependencies are told apart from formula dependencies",
			input:    `{"casks":[{"token":"x","depends_on":{"cask":["docker","y"],"formula":"docker"}}]}`,
			caskDeps: []string{"docker", "y"},
			all:      []string{"docker", "y"},
		},
	}
	for _, tc := range inline {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			formulae, casks, err := parseInfo([]byte(tc.input))
			if err != nil {
				t.Fatalf("parseInfo: %v", err)
			}
			got := append(formulae, casks...)[0]
			if !reflect.DeepEqual(got.RuntimeDependencies, tc.runtime) || !reflect.DeepEqual(got.CaskDependencies, tc.caskDeps) ||
				!reflect.DeepEqual(got.Dependencies, tc.all) {
				t.Errorf("RuntimeDependencies %q CaskDependencies %q Dependencies %q, want %q %q %q",
					got.RuntimeDependencies, got.CaskDependencies, got.Dependencies, tc.runtime, tc.caskDeps, tc.all)
			}
		})
	}
}

func TestParseOutdated(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		input        []byte
		wantFormulae int
		wantCasks    int
		wantErr      error
	}{
		{name: "fixture", input: readFixture(t, "outdated.json"), wantFormulae: 4, wantCasks: 1},
		{name: "nothing outdated", input: []byte(`{"formulae":[],"casks":[]}`)},
		{name: "malformed", input: readFixture(t, "malformed.json"), wantErr: ErrMalformed},
		{name: "missing keys", input: []byte(`{}`), wantErr: ErrMalformed},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := parseOutdated(tc.input)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got.Formulae) != tc.wantFormulae || len(got.Casks) != tc.wantCasks {
				t.Errorf("got %d formulae, %d casks; want %d, %d", len(got.Formulae), len(got.Casks), tc.wantFormulae, tc.wantCasks)
			}
		})
	}
}

func TestMergePrecedence(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   Package
		out  map[string]outdatedEntry
		want Package
	}{
		{
			name: "listed overrides version and marks outdated",
			in:   Package{Name: "node", AvailableVersion: "26.10.0"},
			out:  map[string]outdatedEntry{"node": {CurrentVersion: "26.10.0_2"}},
			want: Package{Name: "node", AvailableVersion: "26.10.0_2", Outdated: true},
		},
		{
			name: "listed overrides pinned",
			in:   Package{Name: "node", Pinned: false},
			out:  map[string]outdatedEntry{"node": {CurrentVersion: "2", Pinned: true}},
			want: Package{Name: "node", AvailableVersion: "2", Outdated: true, Pinned: true},
		},
		{
			name: "unlisted clears stale outdated flag",
			in:   Package{Name: "node", Outdated: true, AvailableVersion: "1"},
			out:  map[string]outdatedEntry{},
			want: Package{Name: "node", AvailableVersion: "1"},
		},
		{
			name: "unlisted keeps pinned from info",
			in:   Package{Name: "node", Pinned: true},
			out:  map[string]outdatedEntry{},
			want: Package{Name: "node", Pinned: true},
		},
		{
			name: "empty current version keeps info version",
			in:   Package{Name: "node", AvailableVersion: "5"},
			out:  map[string]outdatedEntry{"node": {}},
			want: Package{Name: "node", AvailableVersion: "5", Outdated: true},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			for _, kind := range []Kind{KindFormula, KindCask} {
				in, want := tc.in, tc.want
				in.Kind, want.Kind = kind, kind
				oi := outdatedInfo{Formulae: tc.out, Casks: tc.out}
				f, c := merge([]Package{in}, []Package{in}, oi)
				if kind == KindFormula && !reflect.DeepEqual(f[0], want) {
					t.Errorf("formula: got %+v, want %+v", f[0], want)
				}
				if kind == KindCask && !reflect.DeepEqual(c[0], want) {
					t.Errorf("cask: got %+v, want %+v", c[0], want)
				}
			}
		})
	}
}

func TestMergeTapCaskListedByToken(t *testing.T) {
	t.Parallel()

	oi := outdatedInfo{Casks: map[string]outdatedEntry{"widget": {CurrentVersion: "2"}}}
	_, c := merge(nil, []Package{{Name: "acme/tap/widget", Kind: KindCask, AvailableVersion: "1"}}, oi)
	if !c[0].Outdated || c[0].AvailableVersion != "2" || c[0].Name != "acme/tap/widget" {
		t.Errorf("tap cask = %+v, want outdated at 2 with full name", c[0])
	}
}

func TestParseFullNameFallback(t *testing.T) {
	t.Parallel()

	f, c, err := parseInfo([]byte(`{"formulae":[{"name":"a"}],"casks":[{"token":"b"},{"token":"c","full_token":"t/r/c"}]}`))
	if err != nil {
		t.Fatalf("parseInfo: %v", err)
	}
	if f[0].Name != "a" || c[0].Name != "b" || c[1].Name != "t/r/c" {
		t.Errorf("names = %q %q %q, want a b t/r/c", f[0].Name, c[0].Name, c[1].Name)
	}
}

func TestMergeSortsByName(t *testing.T) {
	t.Parallel()

	f, c := merge(
		[]Package{{Name: "zsh"}, {Name: "ada"}, {Name: "mid"}},
		[]Package{{Name: "b"}, {Name: "a"}},
		outdatedInfo{},
	)
	if got := []string{f[0].Name, f[1].Name, f[2].Name}; !reflect.DeepEqual(got, []string{"ada", "mid", "zsh"}) {
		t.Errorf("formulae order = %v", got)
	}
	if got := []string{c[0].Name, c[1].Name}; !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Errorf("casks order = %v", got)
	}
}
