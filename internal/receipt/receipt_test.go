package receipt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kaanemec/brew-board/internal/brew"
	"github.com/kaanemec/brew-board/internal/plan"
	"github.com/kaanemec/brew-board/internal/run"
)

var now = time.Date(2026, 10, 5, 14, 3, 0, 0, time.UTC)

func pkg(name string, kind brew.Kind, available string, installed ...string) brew.Package {
	return brew.Package{Name: name, Kind: kind, InstalledVersions: installed, AvailableVersion: available, Outdated: true}
}

func result(it plan.Item, st run.Status, exit int) run.Result {
	r := run.Result{Item: it, Args: it.Args(), Status: st, ExitCode: exit}
	if st != run.StatusPending {
		r.Started, r.Ended = now, now.Add(time.Second)
	}
	return r
}

func TestBuild(t *testing.T) {
	glib := pkg("glib", brew.KindFormula, "2.90.0", "2.88.3")
	jq := pkg("jq", brew.KindFormula, "1.8", "1.7")
	gone := pkg("gone", brew.KindFormula, "2", "1")
	ff := pkg("firefox", brew.KindCask, "121", "120")
	p, err := plan.Build([]brew.Package{glib, jq, gone, ff}, now)
	if err != nil {
		t.Fatal(err)
	}
	p = p.WithCleanup()
	before := brew.Inventory{BrewVersion: "4.6.0", Formulae: []brew.Package{glib, jq, gone}, Casks: []brew.Package{ff}}
	after := brew.Inventory{
		BrewVersion: "4.6.0",
		Formulae:    []brew.Package{pkg("glib", brew.KindFormula, "2.90.0", "2.88.3", "2.90.0"), jq},
		Casks:       []brew.Package{ff},
	}

	tests := []struct {
		name        string
		statuses    []run.Status
		wantVerdict []string
		wantSummary Summary
	}{
		{
			name:        "all completed reconciles versions",
			statuses:    []run.Status{run.StatusCompleted, run.StatusCompleted, run.StatusCompleted, run.StatusCompleted, run.StatusCompleted},
			wantVerdict: []string{VerdictUpgraded, VerdictUncertain, VerdictUncertain, VerdictUncertain, VerdictCompleted},
			wantSummary: Summary{Upgraded: 1, Uncertain: 3},
		},
		{
			name:        "failure leaves rest not run",
			statuses:    []run.Status{run.StatusCompleted, run.StatusFailed, run.StatusPending, run.StatusPending, run.StatusPending},
			wantVerdict: []string{VerdictUpgraded, VerdictFailed, VerdictNotRun, VerdictNotRun, VerdictNotRun},
			wantSummary: Summary{Upgraded: 1, Failed: 1, NotRun: 3},
		},
		{
			name:        "cancelled",
			statuses:    []run.Status{run.StatusCancelled, run.StatusPending, run.StatusPending, run.StatusPending, run.StatusPending},
			wantVerdict: []string{VerdictCancelled, VerdictNotRun, VerdictNotRun, VerdictNotRun, VerdictNotRun},
			wantSummary: Summary{Cancelled: 1, NotRun: 4},
		},
		{
			name:        "missing results are not run",
			statuses:    []run.Status{run.StatusCompleted},
			wantVerdict: []string{VerdictUpgraded, VerdictNotRun, VerdictNotRun, VerdictNotRun, VerdictNotRun},
			wantSummary: Summary{Upgraded: 1, NotRun: 4},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var results []run.Result
			for i, st := range tt.statuses {
				results = append(results, result(p.Items[i], st, 0))
			}
			r := Build(p, results, before, after, now)
			if len(r.Changes) != len(p.Items) {
				t.Fatalf("got %d changes", len(r.Changes))
			}
			for i, c := range r.Changes {
				if c.Verdict != tt.wantVerdict[i] {
					t.Errorf("%s: verdict %q, want %q", c.Item.Command(), c.Verdict, tt.wantVerdict[i])
				}
			}
			if r.Summary != tt.wantSummary {
				t.Errorf("summary = %+v, want %+v", r.Summary, tt.wantSummary)
			}
			if r.BrewVersion != "4.6.0" || !r.CreatedAt.Equal(now) {
				t.Errorf("header = %q %v", r.BrewVersion, r.CreatedAt)
			}
			if c := r.Changes[2]; c.After != nil || strings.Join(c.Before, ",") != "1" {
				t.Errorf("gone: before %v after %v", c.Before, c.After)
			}
			if c := r.Changes[4]; c.Before != nil || c.After != nil {
				t.Errorf("cleanup has versions: %v %v", c.Before, c.After)
			}
		})
	}
}

func TestText(t *testing.T) {
	glib := pkg("glib", brew.KindFormula, "2.90.0", "2.88.3")
	ff := pkg("firefox", brew.KindCask, "121", "120")
	p, err := plan.Build([]brew.Package{glib, ff}, now)
	if err != nil {
		t.Fatal(err)
	}
	p = p.WithCleanup()
	results := []run.Result{
		result(p.Items[0], run.StatusCompleted, 0),
		result(p.Items[1], run.StatusFailed, 1),
		result(p.Items[2], run.StatusPending, 0),
	}
	after := brew.Inventory{
		BrewVersion: "4.6.0",
		Formulae:    []brew.Package{pkg("glib", brew.KindFormula, "2.90.0", "2.90.0")},
		Casks:       []brew.Package{ff},
	}
	text := Build(p, results, brew.Inventory{}, after, now).Text()

	for _, want := range []string{
		"Created:  2026-10-05 14:03:00 UTC\n",
		"Homebrew: 4.6.0\n",
		"1. brew upgrade --formula glib: upgraded, exit 0, 2.88.3 -> 2.90.0\n",
		"2. brew upgrade --cask firefox: failed, exit 1, 120 (unchanged)\n",
		"3. brew cleanup: not run\n",
		"Summary: 1 upgraded, 1 failed, 0 cancelled, 1 not run, 0 uncertain\n",
		"Re-run the command in a terminal to see Homebrew's full message",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("text missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "cancelled. ") || strings.Contains(text, "uncertain: ") {
		t.Errorf("unexpected hints:\n%s", text)
	}
}

func TestSave(t *testing.T) {
	dir := t.TempDir()
	r := Receipt{CreatedAt: now, BrewVersion: "4.6.0"}
	path, err := r.Save(dir)
	if err != nil {
		t.Fatal(err)
	}
	if ok, _ := filepath.Match(filepath.Join(dir, "brewboard-receipt-20261005-140300-??????.txt"), path); !ok {
		t.Errorf("path = %q, want timestamped name with random suffix", path)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", info.Mode().Perm())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != r.Text() {
		t.Errorf("file content differs from Text()")
	}
	path2, err := r.Save(dir)
	if err != nil || path2 == path {
		t.Errorf("second Save in the same second = %q, %v; want a distinct file", path2, err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 2 {
		t.Errorf("dir has %d entries, want exactly the 2 receipts (no temp files)", len(entries))
	}
	if _, err := r.Save(""); err == nil {
		t.Errorf("Save with empty dir succeeded")
	}
	missing := filepath.Join(dir, "missing")
	if _, err := r.Save(missing); err == nil {
		t.Errorf("Save into missing dir succeeded")
	}
}

func TestTextVerification(t *testing.T) {
	glib := pkg("glib", brew.KindFormula, "2.90.0", "2.88.3")
	ff := pkg("firefox", brew.KindCask, "121", "120")
	p, err := plan.Build([]brew.Package{glib, ff}, now)
	if err != nil {
		t.Fatal(err)
	}
	results := []run.Result{result(p.Items[0], run.StatusCompleted, 0), result(p.Items[1], run.StatusCompleted, 0)}
	after := brew.Inventory{
		Formulae: []brew.Package{pkg("glib", brew.KindFormula, "2.90.0", "2.90.0")},
		Casks:    []brew.Package{ff},
	}
	verified := Build(p, results, brew.Inventory{}, after, now)
	if !verified.Verified || strings.Contains(verified.Text(), "NOT VERIFIED") {
		t.Errorf("Build should be verified:\n%s", verified.Text())
	}
	if !strings.Contains(verified.Text(), "glib: upgraded") {
		t.Errorf("verified text:\n%s", verified.Text())
	}

	unverified := verified.Unverified("Homebrew timed out")
	text := unverified.Text()
	for _, want := range []string{
		"NOT VERIFIED: the post-run inventory refresh failed (Homebrew timed out); versions below are from before the run\n",
		"1. brew upgrade --formula glib: uncertain, exit 0, 2.88.3 before the run (after not verified)\n",
		"2. brew upgrade --cask firefox: uncertain, exit 0, 120 before the run (after not verified)\n",
		"Summary: 0 upgraded, 0 failed, 0 cancelled, 0 not run, 2 uncertain\n",
		"versions could not be checked after the run",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("unverified text missing %q:\n%s", want, text)
		}
	}
	for _, bad := range []string{"glib: upgraded", "(unchanged)", "->"} {
		if strings.Contains(text, bad) {
			t.Errorf("unverified text contains %q:\n%s", bad, text)
		}
	}
	if verified.Changes[0].Verdict != VerdictUpgraded || verified.Summary.Upgraded != 1 {
		t.Error("Unverified must not modify the original receipt")
	}
}
