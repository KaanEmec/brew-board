package brew

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"slices"
	"sort"
	"strings"
)

// infoPayload is the top level of `brew info --json=v2 --installed`. Pointers
// distinguish a missing key from an empty list.
type infoPayload struct {
	Formulae *[]jsonFormula `json:"formulae"`
	Casks    *[]jsonCask    `json:"casks"`
}

type jsonFormula struct {
	Name     string `json:"name"`
	FullName string `json:"full_name"`
	Tap      string `json:"tap"`
	Desc     string `json:"desc"`
	Homepage string `json:"homepage"`
	Versions struct {
		Stable string `json:"stable"`
	} `json:"versions"`
	// Dependencies are the formula's declared (direct) dependencies.
	Dependencies []string `json:"dependencies"`
	// Installed lists the kegs, oldest first.
	Installed []jsonKeg `json:"installed"`
	// LinkedKeg is the version of the keg linked into the prefix, if any.
	LinkedKeg  string `json:"linked_keg"`
	Outdated   bool   `json:"outdated"`
	Pinned     bool   `json:"pinned"`
	Deprecated bool   `json:"deprecated"`
	Disabled   bool   `json:"disabled"`
}

type jsonCask struct {
	Token       string   `json:"token"`
	FullToken   string   `json:"full_token"`
	Name        []string `json:"name"`
	Tap         string   `json:"tap"`
	Desc        string   `json:"desc"`
	Homepage    string   `json:"homepage"`
	Version     string   `json:"version"`
	Installed   string   `json:"installed"`
	Outdated    bool     `json:"outdated"`
	Pinned      bool     `json:"pinned"`
	Deprecated  bool     `json:"deprecated"`
	Disabled    bool     `json:"disabled"`
	AutoUpdates bool     `json:"auto_updates"`
	// DependsOn is decoded leniently (see caskDependencies) so an unexpected
	// shape never fails the whole inventory.
	DependsOn json.RawMessage `json:"depends_on"`
}

// jsonKeg is one installed version of a formula.
type jsonKeg struct {
	Version            string `json:"version"`
	InstalledOnRequest bool   `json:"installed_on_request"`
	// RuntimeDependencies is the keg's full runtime closure; a nil pointer
	// means brew reported no keg dependency information.
	RuntimeDependencies *[]jsonRuntimeDep `json:"runtime_dependencies"`
}

// jsonRuntimeDep is one entry of an installed keg's runtime_dependencies.
type jsonRuntimeDep struct {
	FullName string `json:"full_name"`
	// DeclaredDirectly is false for dependencies pulled in transitively.
	// Older Homebrew versions omit it; those entries are kept, erring on the
	// side of reporting more dependents.
	DeclaredDirectly *bool `json:"declared_directly"`
}

// outdatedPayload is the top level of `brew outdated --json=v2`.
type outdatedPayload struct {
	Formulae *[]jsonOutdated `json:"formulae"`
	Casks    *[]jsonOutdated `json:"casks"`
}

type jsonOutdated struct {
	Name           string `json:"name"`
	CurrentVersion string `json:"current_version"`
	Pinned         bool   `json:"pinned"`
}

// outdatedEntry is what the outdated command says about one package.
type outdatedEntry struct {
	CurrentVersion string
	Pinned         bool
}

// outdatedInfo indexes outdated entries by formula full name and cask token.
// `brew outdated --json=v2` reports formulae by full_name but casks by their
// short token.
type outdatedInfo struct {
	Formulae map[string]outdatedEntry
	Casks    map[string]outdatedEntry
}

// parseInfo decodes `brew info --json=v2 --installed` into formulae and casks.
func parseInfo(data []byte) (formulae, casks []Package, err error) {
	var p infoPayload
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, nil, fmt.Errorf("%w: info: %w", ErrMalformed, err)
	}
	if p.Formulae == nil && p.Casks == nil {
		return nil, nil, fmt.Errorf("%w: info: missing formulae and casks keys", ErrMalformed)
	}
	if p.Formulae != nil {
		for _, f := range *p.Formulae {
			formulae = append(formulae, f.toPackage())
		}
	}
	if p.Casks != nil {
		for _, c := range *p.Casks {
			casks = append(casks, c.toPackage())
		}
	}
	return formulae, casks, nil
}

func (f jsonFormula) toPackage() Package {
	pkg := Package{
		Name:             cmp.Or(f.FullName, f.Name),
		Kind:             KindFormula,
		Description:      f.Desc,
		Homepage:         f.Homepage,
		Tap:              f.Tap,
		AvailableVersion: f.Versions.Stable,
		Outdated:         f.Outdated,
		Pinned:           f.Pinned,
		Deprecated:       f.Deprecated,
		Disabled:         f.Disabled,
	}
	for _, in := range f.Installed {
		pkg.InstalledVersions = append(pkg.InstalledVersions, in.Version)
		if in.InstalledOnRequest {
			pkg.InstalledOnRequest = true
		}
	}
	pkg.Dependencies, pkg.RuntimeDependencies = f.dependencies()
	return pkg
}

// currentKeg returns the index of the keg Homebrew checks: the one whose
// version equals linked_keg, else the last listed (brew lists kegs oldest
// first). It returns -1 when nothing is installed.
func (f jsonFormula) currentKeg() int {
	if f.LinkedKeg != "" {
		if i := slices.IndexFunc(f.Installed, func(k jsonKeg) bool { return k.Version == f.LinkedKeg }); i >= 0 {
			return i
		}
	}
	return len(f.Installed) - 1
}

// dependencies returns the current keg's directly declared runtime
// dependencies and its full runtime closure. Without keg information the
// direct list falls back to the formula's declared dependencies and the
// closure is nil.
func (f jsonFormula) dependencies() (direct, closure []string) {
	i := f.currentKeg()
	if i < 0 || f.Installed[i].RuntimeDependencies == nil {
		return normalizeNames(f.Dependencies), nil
	}
	var all []string
	for _, d := range *f.Installed[i].RuntimeDependencies {
		all = append(all, d.FullName)
		if d.DeclaredDirectly != nil && !*d.DeclaredDirectly {
			continue
		}
		direct = append(direct, d.FullName)
	}
	return normalizeNames(direct), normalizeNames(all)
}

// caskDependencies extracts depends_on.formula and depends_on.cask. Each may
// be a string or a list of strings; anything else (including macos and arch
// requirements) is ignored.
func caskDependencies(raw json.RawMessage) (formulae, casks []string) {
	var obj map[string]json.RawMessage
	if len(raw) == 0 || json.Unmarshal(raw, &obj) != nil {
		return nil, nil
	}
	names := func(key string) []string {
		v, ok := obj[key]
		if !ok {
			return nil
		}
		var one string
		var many []string
		switch {
		case json.Unmarshal(v, &many) == nil:
			return normalizeNames(many)
		case json.Unmarshal(v, &one) == nil:
			return normalizeNames([]string{one})
		}
		return nil
	}
	return names("formula"), names("cask")
}

// normalizeNames sorts names and drops empties and duplicates. It returns nil
// for an empty result.
func normalizeNames(names []string) []string {
	out := make([]string, 0, len(names))
	for _, n := range names {
		if n != "" {
			out = append(out, n)
		}
	}
	slices.Sort(out)
	out = slices.Compact(out)
	if len(out) == 0 {
		return nil
	}
	return out
}

func (c jsonCask) toPackage() Package {
	pkg := Package{
		Name:             cmp.Or(c.FullToken, c.Token),
		Kind:             KindCask,
		Description:      c.Desc,
		Homepage:         c.Homepage,
		Tap:              c.Tap,
		AvailableVersion: c.Version,
		Outdated:         c.Outdated,
		Pinned:           c.Pinned,
		Deprecated:       c.Deprecated,
		Disabled:         c.Disabled,
		AutoUpdates:      c.AutoUpdates,
	}
	if len(c.Name) > 0 && c.Name[0] != c.Token {
		pkg.DisplayName = c.Name[0]
	}
	if c.Installed != "" {
		pkg.InstalledVersions = []string{c.Installed}
	}
	formulae, casks := caskDependencies(c.DependsOn)
	pkg.Dependencies = normalizeNames(append(slices.Clone(formulae), casks...))
	pkg.CaskDependencies = casks
	return pkg
}

// parseOutdated decodes `brew outdated --json=v2`.
func parseOutdated(data []byte) (outdatedInfo, error) {
	var p outdatedPayload
	if err := json.Unmarshal(data, &p); err != nil {
		return outdatedInfo{}, fmt.Errorf("%w: outdated: %w", ErrMalformed, err)
	}
	if p.Formulae == nil && p.Casks == nil {
		return outdatedInfo{}, fmt.Errorf("%w: outdated: missing formulae and casks keys", ErrMalformed)
	}
	info := outdatedInfo{
		Formulae: make(map[string]outdatedEntry),
		Casks:    make(map[string]outdatedEntry),
	}
	index := func(dst map[string]outdatedEntry, src *[]jsonOutdated) {
		if src == nil {
			return
		}
		for _, o := range *src {
			dst[o.Name] = outdatedEntry{CurrentVersion: o.CurrentVersion, Pinned: o.Pinned}
		}
	}
	index(info.Formulae, p.Formulae)
	index(info.Casks, p.Casks)
	return info, nil
}

// merge applies the outdated command's verdict to the info packages and sorts
// both slices by name. Outdated is authoritative: a package it lists is
// outdated with its current version and pinned state; a package it omits is
// not outdated, whatever the info flag said.
func merge(formulae, casks []Package, out outdatedInfo) (mergedFormulae, mergedCasks []Package) {
	apply := func(pkgs []Package, entries map[string]outdatedEntry) {
		for i := range pkgs {
			e, ok := entries[pkgs[i].Name]
			if !ok && pkgs[i].Kind == KindCask {
				// Tap casks are named "user/repo/token" here but listed by token.
				e, ok = entries[path.Base(pkgs[i].Name)]
			}
			if !ok {
				pkgs[i].Outdated = false
				continue
			}
			pkgs[i].Outdated = true
			pkgs[i].Pinned = e.Pinned
			if e.CurrentVersion != "" {
				pkgs[i].AvailableVersion = e.CurrentVersion
			}
		}
		sort.Slice(pkgs, func(a, b int) bool { return pkgs[a].Name < pkgs[b].Name })
	}
	apply(formulae, out.Formulae)
	apply(casks, out.Casks)
	return formulae, casks
}

// unsupportedMarkers are lowercase fragments of brew's complaint about an
// option it does not know.
var unsupportedMarkers = []string{"invalid option", "unknown option", "unrecognized option", "invalid switch"}

func isUnsupportedOption(err error) bool {
	var ce *CommandError
	if !errors.As(err, &ce) {
		return false
	}
	msg := strings.ToLower(ce.Stderr)
	for _, m := range unsupportedMarkers {
		if strings.Contains(msg, m) {
			return true
		}
	}
	return false
}
