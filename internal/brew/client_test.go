package brew

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const fakeBrewPath = "/fake/bin/brew"

var (
	infoKey     = "info --json=v2 --installed"
	outdatedKey = "outdated --json=v2"
)

type fakeResponse struct {
	stdout []byte
	stderr []byte
	err    error
}

// fakeRunner answers by the space-joined argv and records what it was asked.
type fakeRunner struct {
	responses map[string]fakeResponse
	calls     []string
	deadlines []bool
	// deadlineAt records each call's context deadline (zero if none).
	deadlineAt []time.Time
}

// blockingRunner waits for the context to end.
type blockingRunner struct{}

func (blockingRunner) Run(ctx context.Context, _ string, _ ...string) (stdout, stderr []byte, err error) {
	<-ctx.Done()
	return nil, nil, ctx.Err()
}

func (f *fakeRunner) Run(ctx context.Context, path string, args ...string) (stdout, stderr []byte, err error) {
	key := strings.Join(args, " ")
	f.calls = append(f.calls, key)
	d, hasDeadline := ctx.Deadline()
	f.deadlines = append(f.deadlines, hasDeadline)
	f.deadlineAt = append(f.deadlineAt, d)
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if path != fakeBrewPath {
		return nil, nil, errors.New("unexpected brew path " + path)
	}
	r, ok := f.responses[key]
	if !ok {
		return nil, nil, errors.New("unexpected command: " + key)
	}
	return r.stdout, r.stderr, r.err
}

func happyRunner(t *testing.T) *fakeRunner {
	t.Helper()
	return &fakeRunner{responses: map[string]fakeResponse{
		infoKey:     {stdout: readFixture(t, "info_small.json")},
		outdatedKey: {stdout: readFixture(t, "outdated.json")},
	}}
}

func TestLoad(t *testing.T) {
	t.Parallel()

	cmdErr := &CommandError{Args: []string{"info"}, ExitCode: 1, Stderr: "Error: boom"}
	jsonRejected := &CommandError{Args: []string{"info", "--json=v2"}, ExitCode: 1, Stderr: "Error: invalid option: --json=v2"}

	tests := []struct {
		name        string
		responses   map[string]fakeResponse
		wantErrIs   []error
		wantCmdErr  bool
		wantFormula int
		wantCask    int
	}{
		{
			name: "fixtures",
			responses: map[string]fakeResponse{
				infoKey:     {stdout: readFixture(t, "info_small.json")},
				outdatedKey: {stdout: readFixture(t, "outdated.json")},
			},
			wantFormula: 6, wantCask: 3,
		},
		{
			name: "empty inventory",
			responses: map[string]fakeResponse{
				infoKey:     {stdout: readFixture(t, "info_empty.json")},
				outdatedKey: {stdout: readFixture(t, "info_empty.json")},
			},
		},
		{
			name: "malformed info",
			responses: map[string]fakeResponse{
				infoKey:     {stdout: readFixture(t, "malformed.json")},
				outdatedKey: {stdout: readFixture(t, "outdated.json")},
			},
			wantErrIs: []error{ErrMalformed},
		},
		{
			name: "malformed outdated",
			responses: map[string]fakeResponse{
				infoKey:     {stdout: readFixture(t, "info_small.json")},
				outdatedKey: {stdout: []byte("not json")},
			},
			wantErrIs: []error{ErrMalformed},
		},
		{
			name: "info exits non-zero",
			responses: map[string]fakeResponse{
				infoKey: {err: cmdErr},
			},
			wantErrIs:  []error{cmdErr},
			wantCmdErr: true,
		},
		{
			name: "outdated exits non-zero",
			responses: map[string]fakeResponse{
				infoKey:     {stdout: readFixture(t, "info_small.json")},
				outdatedKey: {err: cmdErr},
			},
			wantErrIs:  []error{cmdErr},
			wantCmdErr: true,
		},
		{
			name: "json flag rejected",
			responses: map[string]fakeResponse{
				infoKey: {err: jsonRejected},
			},
			wantErrIs:  []error{ErrUnsupported, jsonRejected},
			wantCmdErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := NewClient(fakeBrewPath, &fakeRunner{responses: tc.responses})
			c.version = "Homebrew 7.0.7"

			inv, err := c.Load(context.Background())
			if len(tc.wantErrIs) > 0 {
				for _, want := range tc.wantErrIs {
					if !errors.Is(err, want) {
						t.Errorf("err = %v, want errors.Is %v", err, want)
					}
				}
				var ce *CommandError
				if got := errors.As(err, &ce); got != tc.wantCmdErr {
					t.Errorf("errors.As(*CommandError) = %v, want %v", got, tc.wantCmdErr)
				}
				if len(inv.Formulae)+len(inv.Casks) != 0 {
					t.Errorf("inventory should be empty on error, got %+v", inv)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if len(inv.Formulae) != tc.wantFormula || len(inv.Casks) != tc.wantCask {
				t.Errorf("got %d formulae, %d casks; want %d, %d", len(inv.Formulae), len(inv.Casks), tc.wantFormula, tc.wantCask)
			}
			if inv.BrewPath != fakeBrewPath || inv.BrewVersion != "Homebrew 7.0.7" || inv.LoadedAt.IsZero() {
				t.Errorf("metadata = %q %q %v", inv.BrewPath, inv.BrewVersion, inv.LoadedAt)
			}
		})
	}
}

func TestLoadMergesAndSorts(t *testing.T) {
	t.Parallel()

	inv, err := NewClient(fakeBrewPath, happyRunner(t)).Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	for _, pkgs := range [][]Package{inv.Formulae, inv.Casks} {
		for i := 1; i < len(pkgs); i++ {
			if pkgs[i-1].Name > pkgs[i].Name {
				t.Errorf("not sorted: %q before %q", pkgs[i-1].Name, pkgs[i].Name)
			}
		}
	}
	if got := len(inv.All()); got != 9 {
		t.Errorf("All() = %d packages, want 9", got)
	}

	node := findPkg(t, inv.Formulae, "node")
	if !node.Outdated || node.AvailableVersion != "26.10.0_2" {
		t.Errorf("node = outdated %v, available %q; want outdated from outdated.json", node.Outdated, node.AvailableVersion)
	}
	tf := findPkg(t, inv.Formulae, "hashicorp/tap/terraform")
	if !tf.Outdated || tf.AvailableVersion != "1.9.8" {
		t.Errorf("terraform = outdated %v, available %q; want outdated at 1.9.8", tf.Outdated, tf.AvailableVersion)
	}
	if p := findPkg(t, inv.Formulae, "go"); p.Outdated {
		t.Error("go should not be outdated")
	}
	if p := findPkg(t, inv.Casks, "cursor"); !p.Outdated {
		t.Error("cursor should be outdated")
	}
	if p := findPkg(t, inv.Casks, "brave-browser"); p.Outdated {
		t.Error("brave-browser (auto-updating, not in outdated list) should not be outdated")
	}
}

func TestLoadIssuesOnlyReadOnlyCommands(t *testing.T) {
	t.Parallel()

	r := happyRunner(t)
	if _, err := NewClient(fakeBrewPath, r).Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := []string{infoKey, outdatedKey}
	if strings.Join(r.calls, "|") != strings.Join(want, "|") {
		t.Errorf("calls = %v, want %v", r.calls, want)
	}
}

func TestLoadDeadline(t *testing.T) {
	t.Parallel()

	t.Run("default timeout applied", func(t *testing.T) {
		t.Parallel()
		r := happyRunner(t)
		if _, err := NewClient(fakeBrewPath, r).Load(context.Background()); err != nil {
			t.Fatalf("Load: %v", err)
		}
		for i, has := range r.deadlines {
			if !has {
				t.Errorf("call %d (%s) had no deadline", i, r.calls[i])
			}
		}
	})

	t.Run("caller deadline does not replace command timeout", func(t *testing.T) {
		t.Parallel()
		r := happyRunner(t)
		ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
		defer cancel()
		c := NewClient(fakeBrewPath, r)
		if _, err := c.Load(ctx); err != nil {
			t.Fatalf("Load: %v", err)
		}
		callerDeadline, _ := ctx.Deadline()
		for i, d := range r.deadlineAt {
			if !d.Before(callerDeadline) {
				t.Errorf("call %d (%s) deadline %v not tighter than caller's %v", i, r.calls[i], d, callerDeadline)
			}
		}
	})

	t.Run("command timeout fires", func(t *testing.T) {
		t.Parallel()
		c := NewClient(fakeBrewPath, blockingRunner{})
		c.timeout = time.Millisecond
		_, err := c.Load(context.Background())
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err = %v, want context.DeadlineExceeded", err)
		}
		if got, want := err.Error(), "brew info --json=v2 --installed: context deadline exceeded"; got != want {
			t.Errorf("err text = %q, want %q (prefix exactly once)", got, want)
		}
	})

	t.Run("cancelled context", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := NewClient(fakeBrewPath, happyRunner(t)).Load(ctx)
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
	})

	t.Run("expired deadline", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		defer cancel()
		_, err := NewClient(fakeBrewPath, happyRunner(t)).Load(ctx)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("err = %v, want context.DeadlineExceeded", err)
		}
	})
}

func TestFallbackPaths(t *testing.T) {
	t.Parallel()

	want := []string{"/opt/homebrew/bin/brew", "/usr/local/bin/brew", "/home/linuxbrew/.linuxbrew/bin/brew"}
	if strings.Join(fallbackPaths, "|") != strings.Join(want, "|") {
		t.Errorf("fallbackPaths = %v, want %v", fallbackPaths, want)
	}
}

func TestExecRunner(t *testing.T) {
	t.Parallel()

	ls, err := exec.LookPath("ls")
	if err != nil {
		t.Skip("ls not available")
	}
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("sleep not available")
	}

	t.Run("success captures stdout", func(t *testing.T) {
		t.Parallel()
		out, _, err := execRunner{}.Run(context.Background(), ls, t.TempDir())
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if len(out) != 0 {
			t.Errorf("empty dir listing = %q", out)
		}
	})

	t.Run("non-zero exit becomes CommandError", func(t *testing.T) {
		t.Parallel()
		missing := filepath.Join(t.TempDir(), "does-not-exist")
		_, _, err := execRunner{}.Run(context.Background(), ls, missing)
		var ce *CommandError
		if !errors.As(err, &ce) {
			t.Fatalf("err = %v, want *CommandError", err)
		}
		if ce.ExitCode == 0 || ce.Stderr == "" || ce.Stderr != strings.TrimSpace(ce.Stderr) {
			t.Errorf("CommandError = %+v", ce)
		}
		if len(ce.Args) != 1 || ce.Args[0] != missing {
			t.Errorf("Args = %v", ce.Args)
		}
	})

	t.Run("deadline", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		_, _, err := execRunner{}.Run(ctx, sleep, "5")
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("err = %v, want context.DeadlineExceeded", err)
		}
	})

	t.Run("cancel", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithCancel(context.Background())
		time.AfterFunc(50*time.Millisecond, cancel)
		_, _, err := execRunner{}.Run(ctx, sleep, "5")
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
	})

	t.Run("missing executable", func(t *testing.T) {
		t.Parallel()
		_, _, err := execRunner{}.Run(context.Background(), filepath.Join(t.TempDir(), "nope"))
		if err == nil {
			t.Fatal("expected error")
		}
		var ce *CommandError
		if errors.As(err, &ce) {
			t.Errorf("start failure should not be a CommandError: %v", err)
		}
	})
}

// writeFakeBrew creates an executable that prints a version banner.
func writeFakeBrew(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "brew")
	script := "#!/bin/sh\necho 'Homebrew 9.9.9'\necho 'Homebrew/homebrew-core (git revision abc)'\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake brew: %v", err)
	}
	return path
}

func TestExecRunnerDisablesAutoUpdate(t *testing.T) {
	env, err := exec.LookPath("env")
	if err != nil {
		t.Skip("env not available")
	}
	out, _, err := execRunner{}.Run(context.Background(), env)
	if err != nil {
		t.Fatalf("env: %v", err)
	}
	for _, want := range brewEnv {
		if !strings.Contains(string(out), want+"\n") {
			t.Errorf("environment missing %s", want)
		}
	}
}

func TestDiscover(t *testing.T) {
	// These tests mutate PATH and a package variable, so they are not parallel.
	origFallback := fallbackPaths
	t.Cleanup(func() { fallbackPaths = origFallback })

	t.Run("not found", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())
		fallbackPaths = nil
		c, err := Discover(context.Background())
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
		if c != nil {
			t.Errorf("client = %+v, want nil", c)
		}
	})

	t.Run("found on PATH", func(t *testing.T) {
		dir := t.TempDir()
		want := writeFakeBrew(t, dir)
		t.Setenv("PATH", dir)
		fallbackPaths = nil
		c, err := Discover(context.Background())
		if err != nil {
			t.Fatalf("Discover: %v", err)
		}
		if c.Path() != want || c.Version() != "Homebrew 9.9.9" {
			t.Errorf("path %q version %q", c.Path(), c.Version())
		}
	})

	t.Run("fallback prefix", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())
		want := writeFakeBrew(t, t.TempDir())
		fallbackPaths = []string{filepath.Join(t.TempDir(), "missing"), want}
		c, err := Discover(context.Background())
		if err != nil {
			t.Fatalf("Discover: %v", err)
		}
		if c.Path() != want {
			t.Errorf("path = %q, want %q", c.Path(), want)
		}
	})
}
