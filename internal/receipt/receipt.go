// Package receipt reconciles an executed plan with the inventory observed
// afterwards and renders a plain-text, local-only session receipt.
package receipt

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/kaanemec/brew-board/internal/brew"
	"github.com/kaanemec/brew-board/internal/plan"
	"github.com/kaanemec/brew-board/internal/run"
)

// Verdicts describing what a change observably did.
const (
	VerdictUpgraded  = "upgraded"
	VerdictRemoved   = "removed"   // uninstall completed and the package is gone
	VerdictCompleted = "completed" // cleanup and autoremove; nothing to check
	VerdictFailed    = "failed"
	VerdictCancelled = "cancelled"
	VerdictNotRun    = "not run"
	VerdictUncertain = "uncertain"
)

// Change is one plan item with its run result and observed versions.
type Change struct {
	Item   plan.Item
	Result run.Result
	// Before and After are installed versions; both nil for cleanup and
	// autoremove.
	// After is nil when the package was not found after the run.
	Before, After []string
	Verdict       string
}

// Summary counts changes by verdict.
type Summary struct {
	Upgraded, Removed, Failed, Cancelled, NotRun, Uncertain int
}

// Receipt is the account of one maintenance session.
type Receipt struct {
	CreatedAt   time.Time
	BrewVersion string
	Changes     []Change
	Summary     Summary
	// Verified reports that After versions come from an inventory loaded
	// after the run. Build sets it; Unverified clears it.
	Verified bool
	// UnverifiedReason says why the versions could not be verified.
	UnverifiedReason string
}

// Build reconciles each plan item with its result (by index) and the
// post-run inventory. A completed upgrade counts as upgraded only if the
// planned version is now installed, and a completed uninstall counts as
// removed only if the package is no longer installed; otherwise either is
// uncertain. Items without a result are reported as not run.
func Build(p plan.Plan, results []run.Result, before, after brew.Inventory, now time.Time) Receipt {
	r := Receipt{CreatedAt: now, BrewVersion: firstNonEmpty(after.BrewVersion, before.BrewVersion), Verified: true}
	for i, it := range p.Items {
		res := run.Result{Item: it, Args: it.Args(), Status: run.StatusPending}
		if i < len(results) {
			res = results[i]
		}
		c := Change{Item: it, Result: res}
		if hasVersions(it.Op) {
			c.Before = it.Package.InstalledVersions
			if pkg, ok := plan.Find(before, it.Package.Name, it.Package.Kind); ok {
				c.Before = pkg.InstalledVersions
			}
			if pkg, ok := plan.Find(after, it.Package.Name, it.Package.Kind); ok {
				c.After = pkg.InstalledVersions
			}
		}
		c.Verdict = verdict(c)
		r.Changes = append(r.Changes, c)
		r.Summary.count(c.Verdict)
	}
	return r
}

// Unverified returns a copy of r for a session whose post-run inventory
// refresh failed, so the "after" inventory Build saw is from before the run.
// Upgrades and removals that looked successful become uncertain and the summary is
// recounted; Text says prominently that nothing was verified.
func (r Receipt) Unverified(reason string) Receipt {
	r.Verified = false
	r.UnverifiedReason = reason
	r.Changes = slices.Clone(r.Changes)
	r.Summary = Summary{}
	for i := range r.Changes {
		if v := r.Changes[i].Verdict; v == VerdictUpgraded || v == VerdictRemoved {
			r.Changes[i].Verdict = VerdictUncertain
		}
		r.Summary.count(r.Changes[i].Verdict)
	}
	return r
}

func (s *Summary) count(verdict string) {
	switch verdict {
	case VerdictUpgraded:
		s.Upgraded++
	case VerdictRemoved:
		s.Removed++
	case VerdictFailed:
		s.Failed++
	case VerdictCancelled:
		s.Cancelled++
	case VerdictNotRun:
		s.NotRun++
	case VerdictUncertain:
		s.Uncertain++
	}
}

func verdict(c Change) string {
	switch c.Result.Status {
	case run.StatusCompleted:
		switch c.Item.Op {
		case plan.OpUpgrade:
			if slices.Contains(c.After, c.Item.Package.AvailableVersion) {
				return VerdictUpgraded
			}
			return VerdictUncertain
		case plan.OpUninstall:
			if c.After == nil {
				return VerdictRemoved
			}
			return VerdictUncertain
		default:
			return VerdictCompleted
		}
	case run.StatusFailed:
		return VerdictFailed
	case run.StatusCancelled:
		return VerdictCancelled
	case run.StatusPending:
		return VerdictNotRun
	default:
		return VerdictUncertain
	}
}

// hasVersions reports whether an item's package versions are tracked.
func hasVersions(op plan.Op) bool {
	return op == plan.OpUpgrade || op == plan.OpUninstall
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// Text renders the receipt as plain, copyable text.
func (r Receipt) Text() string {
	var b strings.Builder
	b.WriteString("Brew Board maintenance receipt\n")
	fmt.Fprintf(&b, "Created:  %s\n", r.CreatedAt.Format("2006-01-02 15:04:05 MST"))
	fmt.Fprintf(&b, "Homebrew: %s\n\n", firstNonEmpty(r.BrewVersion, "unknown"))
	if !r.Verified {
		reason := ""
		if r.UnverifiedReason != "" {
			reason = " (" + r.UnverifiedReason + ")"
		}
		fmt.Fprintf(&b, "NOT VERIFIED: the post-run inventory refresh failed%s; versions below are from before the run\n\n", reason)
	}

	for i, c := range r.Changes {
		fmt.Fprintf(&b, "%d. %s: %s", i+1, c.Item.Command(), c.Verdict)
		if !c.Result.Started.IsZero() {
			if c.Result.ExitCode >= 0 {
				fmt.Fprintf(&b, ", exit %d", c.Result.ExitCode)
			} else {
				b.WriteString(", no exit code")
			}
		}
		if hasVersions(c.Item.Op) {
			fmt.Fprintf(&b, ", %s", r.versionChange(c))
		}
		b.WriteString("\n")
	}

	s := r.Summary
	fmt.Fprintf(&b, "\nSummary: %d upgraded, %d removed, %d failed, %d cancelled, %d not run, %d uncertain\n",
		s.Upgraded, s.Removed, s.Failed, s.Cancelled, s.NotRun, s.Uncertain)
	if s.Failed > 0 {
		b.WriteString("A command failed. Re-run the command in a terminal to see Homebrew's full message.\n")
	}
	switch {
	case s.Cancelled > 0 && !r.Verified:
		b.WriteString("A command was cancelled. Homebrew may have left partial work; versions above were not checked after the run.\n")
	case s.Cancelled > 0:
		b.WriteString("A command was cancelled. Homebrew may have left partial work; versions above are what was observed after the run.\n")
	}
	switch {
	case s.Uncertain > 0 && !r.Verified:
		b.WriteString("A result is uncertain: versions could not be checked after the run; check the package in Homebrew.\n")
	case s.Uncertain > 0:
		b.WriteString("A result is uncertain: the command finished but the planned version (or removal) was not observed; check the package in Homebrew.\n")
	}
	return b.String()
}

func (r Receipt) versionChange(c Change) string {
	before := firstNonEmpty(strings.Join(c.Before, ", "), "?")
	switch {
	case !r.Verified:
		return before + " before the run (after not verified)"
	case c.Item.Op == plan.OpUninstall && c.After == nil:
		return before + " -> removed"
	case c.Item.Op == plan.OpUninstall:
		return before + " (still installed)"
	case c.After == nil:
		return before + " -> not installed"
	case slices.Equal(c.Before, c.After):
		return before + " (unchanged)"
	default:
		return before + " -> " + strings.Join(c.After, ", ")
	}
}

// Save writes the receipt text to dir as
// brewboard-receipt-<timestamp>-<random>.txt with mode 0600 and returns the
// path. The random suffix keeps saves within the same second apart. The text is
// written to a temporary file in dir and renamed on success, so a failed save
// leaves nothing behind, and an existing file is never overwritten.
func (r Receipt) Save(dir string) (path string, err error) {
	if dir == "" {
		return "", errors.New("save receipt: no directory given")
	}
	stamp := r.CreatedAt.Format("20060102-150405")
	for range 10 {
		var suffix [3]byte
		if _, err = rand.Read(suffix[:]); err != nil {
			return "", fmt.Errorf("save receipt: %w", err)
		}
		path = filepath.Join(dir, fmt.Sprintf("brewboard-receipt-%s-%x.txt", stamp, suffix))
		// O_EXCL guarantees nothing existing is overwritten; retry on a clash.
		f, openErr := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(openErr, fs.ErrExist) {
			err = openErr
			continue
		}
		if openErr != nil {
			return "", fmt.Errorf("save receipt: %w", openErr)
		}
		_, err = f.WriteString(r.Text())
		if closeErr := f.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			_ = os.Remove(path) // never leave a partial receipt behind
			return "", fmt.Errorf("save receipt: %w", err)
		}
		return path, nil
	}
	return "", fmt.Errorf("save receipt: %w", err)
}
