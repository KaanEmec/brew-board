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
	// OpUninstall removes one formula or cask. It never forces and never
	// ignores dependencies, so Homebrew still refuses to remove a package
	// something installed needs.
	OpUninstall Op = "uninstall"
	// OpAutoremove runs `brew autoremove`. It is only added explicitly via
	// WithAutoremove.
	OpAutoremove Op = "autoremove"
)

// Sentinel errors returned (wrapped) by Build.
var (
	// ErrEmpty means nothing was selected.
	ErrEmpty = errors.New("no packages selected")
	// ErrNotUpgradable means a selected package cannot be upgraded right now.
	ErrNotUpgradable = errors.New("package is not upgradable")
	// ErrConflict means a package was selected for both upgrade and uninstall.
	ErrConflict = errors.New("package selected for both upgrade and uninstall")
	// ErrPinned means a pinned package was selected for uninstall.
	ErrPinned = errors.New("pinned; unpin it first with brew unpin")
	// ErrInvalidSelection means a selection has an operation other than
	// OpUpgrade or OpUninstall, or a package of unknown kind for uninstall.
	ErrInvalidSelection = errors.New("invalid selection")
)

// Selection is one package the user marked, with the operation they chose.
// Op is OpUpgrade or OpUninstall.
type Selection struct {
	Package brew.Package
	Op      Op
}

// Item is one operation in a plan.
type Item struct {
	Op Op
	// Package is a snapshot of the package at staging time; zero for cleanup
	// and autoremove.
	Package brew.Package
	// Dependents, for uninstall items, are the installed packages that need
	// Package at staging time, directly or through their runtime dependency
	// closure (sorted); Build fills it.
	Dependents []string
	// kept are the Dependents not selected for removal in the same plan.
	kept []string
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
	case OpUninstall:
		flag := "--formula"
		if i.Package.Kind == brew.KindCask {
			flag = "--cask"
		}
		return []string{"uninstall", flag, i.Package.Name}
	case OpCleanup:
		return []string{"cleanup"}
	case OpAutoremove:
		return []string{"autoremove"}
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
	case OpUninstall:
		p := i.Package
		s := fmt.Sprintf("Remove %s %s (installed %s).", p.Kind, p.Name, installedLabel(p))
		switch {
		case len(i.kept) > 0:
			s += fmt.Sprintf(" %s on it and %s installed.", depend(i.kept), verb(i.kept, "stays", "stay"))
		case len(i.Dependents) > 0:
			s += fmt.Sprintf(" %s on it and %s removed earlier in this plan.", depend(i.Dependents), verb(i.Dependents, "is", "are"))
		default:
			s += " Nothing installed depends on it."
		}
		return s
	case OpCleanup:
		return "Remove old versions and cached downloads Homebrew no longer needs."
	case OpAutoremove:
		return "Remove formulae that were installed only as dependencies and are no longer needed by anything installed."
	default:
		return "Unknown operation."
	}
}

// depend renders "a depends" or "a and b depend".
func depend(names []string) string {
	return joinAnd(names) + " " + verb(names, "depends", "depend")
}

func verb(names []string, one, many string) string {
	if len(names) == 1 {
		return one
	}
	return many
}

// joinAnd renders "a", "a and b", or "a, b and c".
func joinAnd(names []string) string {
	if len(names) <= 1 {
		return strings.Join(names, "")
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
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

// Build creates one item per selection. Uninstall items come first, ordered
// so that every package is removed before the packages it depends on
// (considering only dependencies among the selected uninstalls; otherwise
// selection order is kept). Upgrade items follow in selection order.
//
// It rejects an empty selection with ErrEmpty; a package selected for both
// upgrade and uninstall with ErrConflict; an upgrade of a package that is not
// outdated, is pinned, or has an unknown kind with ErrNotUpgradable; an
// uninstall of a pinned package with ErrPinned; and an unknown operation, or
// an uninstall of an unknown kind, with ErrInvalidSelection. Errors name the package. Duplicate selections (same
// Name, Kind and Op) are silently collapsed, keeping the first occurrence.
//
// inv is the inventory the selections were made from. It supplies the
// dependency graph (Item.Dependents); a package's entry in inv takes
// precedence over the selection's snapshot for its dependencies. Cleanup and
// autoremove are never added here; see WithCleanup and WithAutoremove.
func Build(selected []Selection, inv brew.Inventory, now time.Time) (Plan, error) {
	if len(selected) == 0 {
		return Plan{}, ErrEmpty
	}
	ops := make(map[pkgKey]Op, len(selected))
	var uninstalls, upgrades []Selection
	for _, sel := range selected {
		p := sel.Package
		k := keyOf(p)
		switch sel.Op {
		case OpUpgrade:
			if reason := notUpgradable(p); reason != "" {
				return Plan{}, fmt.Errorf("%s %s (%s): %w", p.Kind, p.Name, reason, ErrNotUpgradable)
			}
		case OpUninstall:
			if p.Kind != brew.KindFormula && p.Kind != brew.KindCask {
				return Plan{}, fmt.Errorf("%s %s (unknown kind %q): %w", p.Kind, p.Name, p.Kind, ErrInvalidSelection)
			}
			if p.Pinned {
				return Plan{}, fmt.Errorf("%s %s: %w", p.Kind, p.Name, ErrPinned)
			}
		default:
			return Plan{}, fmt.Errorf("%s %s (unknown operation %q): %w", p.Kind, p.Name, sel.Op, ErrInvalidSelection)
		}
		if prev, ok := ops[k]; ok {
			if prev != sel.Op {
				return Plan{}, fmt.Errorf("%s %s: %w", p.Kind, p.Name, ErrConflict)
			}
			continue
		}
		ops[k] = sel.Op
		if sel.Op == OpUninstall {
			uninstalls = append(uninstalls, sel)
		} else {
			upgrades = append(upgrades, sel)
		}
	}

	items := make([]Item, 0, len(uninstalls)+len(upgrades))
	removing := make(map[pkgKey]bool, len(uninstalls))
	for _, sel := range uninstalls {
		removing[keyOf(sel.Package)] = true
	}
	for _, sel := range orderUninstalls(uninstalls, inv) {
		it := Item{Op: OpUninstall, Package: sel.Package}
		for _, d := range dependentsOf(inv, sel.Package) {
			it.Dependents = append(it.Dependents, d.Name)
			if !removing[keyOf(d)] {
				it.kept = append(it.kept, d.Name)
			}
		}
		items = append(items, it)
	}
	for _, sel := range upgrades {
		items = append(items, Item{Op: OpUpgrade, Package: sel.Package})
	}
	return Plan{Items: items, CreatedAt: now}, nil
}

func notUpgradable(p brew.Package) string {
	switch {
	case p.Kind != brew.KindFormula && p.Kind != brew.KindCask:
		return fmt.Sprintf("unknown kind %q", p.Kind)
	case !p.Outdated:
		return "not outdated"
	case p.Pinned:
		return "pinned"
	}
	return ""
}

type pkgKey struct {
	name string
	kind brew.Kind
}

func keyOf(p brew.Package) pkgKey { return pkgKey{p.Name, p.Kind} }

// current returns p's entry in inv, or p itself when inv lacks it.
func current(inv brew.Inventory, p brew.Package) brew.Package {
	if cur, ok := Find(inv, p.Name, p.Kind); ok {
		return cur
	}
	return p
}

// requires returns the packages p needs installed, with their kinds. For a
// formula that is its keg's full runtime closure (RuntimeDependencies, or
// Dependencies when brew reported no closure), all formulae: this is what
// Homebrew's "required by" check and autoremove use. For a cask it is
// Dependencies, where the names in CaskDependencies are casks and the rest
// formulae.
func requires(p brew.Package) []pkgKey {
	if p.Kind == brew.KindFormula {
		deps := p.RuntimeDependencies
		if len(deps) == 0 {
			deps = p.Dependencies
		}
		out := make([]pkgKey, len(deps))
		for i, d := range deps {
			out[i] = pkgKey{d, brew.KindFormula}
		}
		return out
	}
	out := make([]pkgKey, len(p.Dependencies))
	for i, d := range p.Dependencies {
		kind := brew.KindFormula
		if slices.Contains(p.CaskDependencies, d) {
			kind = brew.KindCask
		}
		out[i] = pkgKey{d, kind}
	}
	return out
}

// dependsOn reports whether a needs b, given a's requirements aReq. Names are
// matched together with their kind, so a formula and a cask of the same name
// stay apart.
func dependsOn(a brew.Package, aReq []pkgKey, b brew.Package) bool {
	return keyOf(a) != keyOf(b) && slices.Contains(aReq, keyOf(b))
}

// orderUninstalls sorts uninstall selections so dependents come before their
// dependencies (Kahn's algorithm, always taking the earliest-selected ready
// package). A dependency cycle, which Homebrew does not allow, falls back to
// selection order for the packages involved.
func orderUninstalls(sel []Selection, inv brew.Inventory) []Selection {
	n := len(sel)
	deps := make([][]pkgKey, n)
	for i, s := range sel {
		deps[i] = requires(current(inv, s.Package))
	}
	// waiting[j] counts selected packages that depend on j and are not yet placed.
	waiting := make([]int, n)
	for i := range sel {
		for j := range sel {
			if dependsOn(sel[i].Package, deps[i], sel[j].Package) {
				waiting[j]++
			}
		}
	}
	placed := make([]bool, n)
	out := make([]Selection, 0, n)
	for len(out) < n {
		next := -1
		for i := range sel {
			if !placed[i] && waiting[i] == 0 {
				next = i
				break
			}
		}
		if next < 0 { // cycle: take the earliest remaining
			next = slices.Index(placed, false)
		}
		placed[next] = true
		out = append(out, sel[next])
		for j := range sel {
			if !placed[j] && dependsOn(sel[next].Package, deps[next], sel[j].Package) {
				waiting[j]--
			}
		}
	}
	return out
}

// Dependents returns the names of installed packages (formulae and casks)
// that need pkg, sorted and de-duplicated: formulae whose runtime closure
// includes it (formulae only ever need formulae) and casks whose depends_on
// names it with its kind. It is the same rule Build and Validate use, and
// the rule Homebrew applies before it uninstalls a formula.
func Dependents(inv brew.Inventory, pkg brew.Package) []string {
	var out []string
	for _, p := range dependentsOf(inv, pkg) {
		out = append(out, p.Name)
	}
	return slices.Compact(out)
}

// dependentsOf returns the installed packages that need target (by Name and
// Kind), sorted by name.
func dependentsOf(inv brew.Inventory, target brew.Package) []brew.Package {
	var out []brew.Package
	for _, p := range inv.All() {
		if dependsOn(p, requires(p), target) {
			out = append(out, p)
		}
	}
	slices.SortStableFunc(out, func(a, b brew.Package) int { return strings.Compare(a.Name, b.Name) })
	return out
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

// WithAutoremove returns a copy of the plan with a single autoremove item.
// It is placed before any cleanup item (so cleanup also sweeps what
// autoremove removed) and otherwise appended. It is a no-op if the plan
// already contains autoremove.
func (p Plan) WithAutoremove() Plan {
	if slices.ContainsFunc(p.Items, func(i Item) bool { return i.Op == OpAutoremove }) {
		return p
	}
	at := slices.IndexFunc(p.Items, func(i Item) bool { return i.Op == OpCleanup })
	if at < 0 {
		at = len(p.Items)
	}
	items := make([]Item, 0, len(p.Items)+1)
	items = append(items, p.Items[:at]...)
	items = append(items, Item{Op: OpAutoremove})
	p.Items = append(items, p.Items[at:]...)
	return p
}

// Problem explains why a plan item no longer matches the installation.
type Problem struct {
	Item   Item
	Reason string
}

// Validate compares each upgrade and uninstall item with the same package (by
// Name and Kind) in a freshly loaded inventory. An empty result means the plan
// is current. Every problem is blocking: the item should be dropped or the
// plan rebuilt. Cleanup and autoremove items never produce problems.
//
// An uninstall item is a problem when the package is already gone, is
// pinned, or when an installed package that is not also being uninstalled in
// this plan still needs it (see Dependents), which is exactly when Homebrew
// would refuse the uninstall.
func (p Plan) Validate(fresh brew.Inventory) []Problem {
	removing := p.uninstallSet()
	var problems []Problem
	for _, it := range p.Items {
		var reason string
		switch it.Op {
		case OpUpgrade:
			reason = upgradeProblem(fresh, it)
		case OpUninstall:
			reason = uninstallProblem(fresh, it, removing)
		default:
			continue
		}
		if reason != "" {
			problems = append(problems, Problem{Item: it, Reason: reason})
		}
	}
	return problems
}

func upgradeProblem(fresh brew.Inventory, it Item) string {
	cur, ok := Find(fresh, it.Package.Name, it.Package.Kind)
	switch {
	case !ok:
		return "no longer installed"
	case !cur.Outdated:
		return fmt.Sprintf("already up to date (installed %s)", installedLabel(cur))
	case cur.Pinned:
		return "pinned since staging"
	case cur.AvailableVersion != it.Package.AvailableVersion:
		return fmt.Sprintf("available version changed from %s to %s",
			it.Package.AvailableVersion, cur.AvailableVersion)
	}
	return ""
}

func uninstallProblem(fresh brew.Inventory, it Item, removing map[pkgKey]bool) string {
	cur, ok := Find(fresh, it.Package.Name, it.Package.Kind)
	switch {
	case !ok:
		return "already uninstalled"
	case cur.Pinned:
		return ErrPinned.Error()
	}
	var blocking []string
	for _, d := range dependentsOf(fresh, it.Package) {
		if !removing[keyOf(d)] {
			blocking = append(blocking, d.Name)
		}
	}
	if len(blocking) == 0 {
		return ""
	}
	return fmt.Sprintf("required by %s — mark them for removal too, or keep %s",
		strings.Join(blocking, ", "), it.Package.Name)
}

func (p Plan) uninstallSet() map[pkgKey]bool {
	set := make(map[pkgKey]bool)
	for _, it := range p.Items {
		if it.Op == OpUninstall {
			set[keyOf(it.Package)] = true
		}
	}
	return set
}

// Orphans previews which formulae `brew autoremove` would remove once the
// plan's uninstall items have run: formulae not installed on request that
// no remaining installed package needs (see Dependents). It iterates to a
// fixed point, so a chain of dependency-only formulae freed by the plan is
// included in full, and it includes dependency-only formulae that were
// already unneeded; with an empty plan it previews what `brew autoremove`
// would remove now. The plan's own uninstall targets are not listed. Sorted.
//
// This is only a preview from the dependency data brew reported; Homebrew's
// own `brew autoremove` is authoritative and may remove more or less.
func Orphans(inv brew.Inventory, p Plan) []string {
	gone := p.uninstallSet()
	all := inv.All()
	reqs := make([][]pkgKey, len(all))
	for i, d := range all {
		reqs[i] = requires(d)
	}
	var orphans []string
	for changed := true; changed; {
		changed = false
		for _, f := range inv.Formulae {
			if f.InstalledOnRequest || gone[keyOf(f)] {
				continue
			}
			needed := false
			for i, d := range all {
				if !gone[keyOf(d)] && dependsOn(d, reqs[i], f) {
					needed = true
					break
				}
			}
			if !needed {
				gone[keyOf(f)] = true
				orphans = append(orphans, f.Name)
				changed = true
			}
		}
	}
	slices.Sort(orphans)
	return slices.Compact(orphans)
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
