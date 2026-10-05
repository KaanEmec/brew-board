// Package brew is the read-only adapter around the user's Homebrew executable.
// It discovers brew, runs structured (JSON) commands, and normalizes the output
// into small Go types. Nothing in this package modifies the installation.
package brew

import (
	"context"
	"errors"
	"strings"
	"time"
)

// Kind distinguishes formulae from casks.
type Kind string

const (
	KindFormula Kind = "formula"
	KindCask    Kind = "cask"
)

// Package is a normalized installed formula or cask.
type Package struct {
	// Name is the fully qualified name Homebrew accepts on the command line,
	// e.g. "hashicorp/tap/terraform". For homebrew/core formulae and
	// homebrew/cask casks it equals the short name or token.
	Name string
	Kind Kind
	// DisplayName is the cask's human name when it differs from the token; empty for formulae.
	DisplayName string
	Description string
	Homepage    string
	Tap         string
	// InstalledVersions lists every installed version (formulae may keep several kegs).
	InstalledVersions []string
	// AvailableVersion is the version Homebrew would install now (stable/current).
	AvailableVersion string
	Outdated         bool
	Pinned           bool
	// Deprecated or disabled upstream.
	Deprecated bool
	Disabled   bool
	// AutoUpdates is true for casks that update themselves outside Homebrew.
	AutoUpdates bool
	// InstalledOnRequest is false for formulae pulled in only as dependencies.
	InstalledOnRequest bool
}

// Inventory is a snapshot of the local Homebrew installation.
type Inventory struct {
	BrewPath    string
	BrewVersion string
	Formulae    []Package
	Casks       []Package
	LoadedAt    time.Time
}

// All returns formulae followed by casks.
func (inv Inventory) All() []Package {
	out := make([]Package, 0, len(inv.Formulae)+len(inv.Casks))
	out = append(out, inv.Formulae...)
	out = append(out, inv.Casks...)
	return out
}

// Loader is what the TUI depends on. *Client implements it; tests use fakes.
type Loader interface {
	// Load returns a snapshot of the installation. Formulae and Casks are
	// sorted by Name, so consumers need not re-sort.
	Load(ctx context.Context) (Inventory, error)
}

// Sentinel errors. Wrap them so callers can use errors.Is.
var (
	// ErrNotFound: no brew executable on PATH or in the standard prefixes.
	ErrNotFound = errors.New("homebrew not found")
	// ErrUnsupported: brew is present but does not provide the structured output we need.
	ErrUnsupported = errors.New("unsupported homebrew version or output")
	// ErrMalformed: brew ran but its output could not be parsed.
	ErrMalformed = errors.New("malformed homebrew output")
)

// CommandError reports a brew invocation that exited non-zero.
type CommandError struct {
	Args     []string
	ExitCode int
	Stderr   string
}

func (e *CommandError) Error() string {
	return "brew " + strings.Join(e.Args, " ") + " failed: " + e.Stderr
}
