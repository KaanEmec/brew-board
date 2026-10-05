// Package plan turns the user's selections into an ordered, typed list of
// Homebrew operations and checks that list against a fresh inventory before
// anything runs. It never executes brew itself.
package plan

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/kaanemec/brew-board/internal/brew"
)

// Op is the kind of operation a plan item performs.
type Op string

const (
	// OpUpgrade upgrades one formula or cask.
	OpUpgrade Op = "upgrade"
	// OpCleanup runs `brew cleanup`. It is only added explicitly via WithCleanup.
	OpCleanup Op = "cleanup"
)

// Sentinel errors returned (wrapped) by Build.
var (
	// ErrEmpty means nothing was selected.
	ErrEmpty = errors.New("no packages selected")
	// ErrNotUpgradable means a selected package cannot be upgraded right now.
	ErrNotUpgradable = errors.New("package is not upgradable")
)

// Item is one operation in a plan.
type Item struct {
	Op Op
	// Package is a snapshot of the package at staging time; zero for cleanup.
	Package brew.Package
}

// Args returns the exact brew argv (without the executable) for the item.
func (i Item) Args() []string {
	switch i.Op {
	case OpUpgrade:
		flag := "--formula"
		if i.Package.Kind == brew.KindCask {
			flag = "--cask"
		}
		return []string{"upgrade", flag, i.Package.Name}
	case OpCleanup:
		return []string{"cleanup"}
	default:
		return nil
	}
}

// Command renders the item as a command line for display only. It must never
// be passed to a shell; use Args to execute.
func (i Item) Command() string {
	return strings.Join(append([]string{"brew"}, i.Args()...), " ")
}

// Explain returns a plain-language, one-line description of the item.
func (i Item) Explain() string {
	switch i.Op {
	case OpUpgrade:
		p := i.Package
		s := fmt.Sprintf("Upgrade %s %s from %s to %s.", p.Kind, p.Name, installedLabel(p), p.AvailableVersion)
		if p.Kind == brew.KindFormula {
			s += " Homebrew may also upgrade dependencies."
		}
		return s
	case OpCleanup:
		return "Remove old versions and cached downloads Homebrew no longer needs."
	default:
		return "Unknown operation."
	}
}

func installedLabel(p brew.Package) string {
	if len(p.InstalledVersions) == 0 {
		return "an unknown version"
	}
	return strings.Join(p.InstalledVersions, ", ")
}

// Plan is an ordered list of items the user reviews before confirming.
type Plan struct {
	Items     []Item
	CreatedAt time.Time
}

// Build creates one upgrade item per selected package, preserving order.
// It rejects an empty selection with ErrEmpty and any package that is not
// outdated, is pinned, or has an unknown kind with an error wrapping
// ErrNotUpgradable. Duplicate selections (same Name and Kind) are silently
// collapsed, keeping the first occurrence. Cleanup is never added here; see
// WithCleanup.
func Build(selected []brew.Package, now time.Time) (Plan, error) {
	if len(selected) == 0 {
		return Plan{}, ErrEmpty
	}
	items := make([]Item, 0, len(selected))
	type key struct {
		name string
		kind brew.Kind
	}
	seen := make(map[key]bool, len(selected))
	for _, p := range selected {
		var reason string
		switch {
		case p.Kind != brew.KindFormula && p.Kind != brew.KindCask:
			reason = fmt.Sprintf("unknown kind %q", p.Kind)
		case !p.Outdated:
			reason = "not outdated"
		case p.Pinned:
			reason = "pinned"
		}
		if reason != "" {
			return Plan{}, fmt.Errorf("%s %s (%s): %w", p.Kind, p.Name, reason, ErrNotUpgradable)
		}
		k := key{p.Name, p.Kind}
		if seen[k] {
			continue
		}
		seen[k] = true
		items = append(items, Item{Op: OpUpgrade, Package: p})
	}
	return Plan{Items: items, CreatedAt: now}, nil
}

// WithCleanup returns a copy of the plan with a single cleanup item appended.
// It is a no-op if the plan already contains cleanup.
func (p Plan) WithCleanup() Plan {
	if slices.ContainsFunc(p.Items, func(i Item) bool { return i.Op == OpCleanup }) {
		return p
	}
	items := make([]Item, 0, len(p.Items)+1)
	items = append(items, p.Items...)
	p.Items = append(items, Item{Op: OpCleanup})
	return p
}

// Problem explains why a plan item no longer matches the installation.
type Problem struct {
	Item   Item
	Reason string
}

// Validate compares each upgrade item with the same package (by Name and Kind)
// in a freshly loaded inventory. An empty result means the plan is current.
// Cleanup items never produce problems.
func (p Plan) Validate(fresh brew.Inventory) []Problem {
	var problems []Problem
	for _, it := range p.Items {
		if it.Op != OpUpgrade {
			continue
		}
		cur, ok := Find(fresh, it.Package.Name, it.Package.Kind)
		var reason string
		switch {
		case !ok:
			reason = "no longer installed"
		case !cur.Outdated:
			reason = fmt.Sprintf("already up to date (installed %s)", installedLabel(cur))
		case cur.Pinned:
			reason = "pinned since staging"
		case cur.AvailableVersion != it.Package.AvailableVersion:
			reason = fmt.Sprintf("available version changed from %s to %s",
				it.Package.AvailableVersion, cur.AvailableVersion)
		}
		if reason != "" {
			problems = append(problems, Problem{Item: it, Reason: reason})
		}
	}
	return problems
}

// Find returns the package with the given name and kind from inv.
func Find(inv brew.Inventory, name string, kind brew.Kind) (brew.Package, bool) {
	list := inv.Formulae
	if kind == brew.KindCask {
		list = inv.Casks
	}
	for _, p := range list {
		if p.Name == name && p.Kind == kind {
			return p, true
		}
	}
	return brew.Package{}, false
}
