package brew

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"path"
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
	Installed []struct {
		Version            string `json:"version"`
		InstalledOnRequest bool   `json:"installed_on_request"`
	} `json:"installed"`
	Outdated   bool `json:"outdated"`
	Pinned     bool `json:"pinned"`
	Deprecated bool `json:"deprecated"`
	Disabled   bool `json:"disabled"`
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
	return pkg
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
