// Command brewboard is a keyboard-first terminal UI for Homebrew maintenance.
// It only reads the installation until the user reviews a plan and confirms
// it with y.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"slices"
	"sync"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/kaanemec/brew-board/internal/brew"
	"github.com/kaanemec/brew-board/internal/plan"
	"github.com/kaanemec/brew-board/internal/run"
	"github.com/kaanemec/brew-board/internal/tui"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println("brewboard", version)
		return
	}

	ctx := context.Background()
	homebrew := &discoveringLoader{}
	program := tea.NewProgram(
		tui.New(homebrew, tui.WithContext(ctx), tui.WithExecutor(homebrew)),
		tea.WithAltScreen(),
		// Bubble Tea's own handler would turn SIGINT/SIGTERM into an exit that
		// bypasses the model; forward them so a running brew is not orphaned.
		tea.WithoutSignalHandler(),
	)
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	go func() {
		for range signals {
			program.Send(tui.InterruptMsg{})
		}
	}()

	final, err := program.Run()
	signal.Stop(signals)
	if !tui.StopRun(final, shutdownWait) {
		fmt.Fprintln(os.Stderr, "brewboard: a Homebrew command was still running at exit and did not stop in time")
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "brewboard:", err)
		os.Exit(1)
	}
}

// shutdownWait bounds how long main waits for a cancelled run to return. It
// exceeds the executor's 10s SIGINT-to-kill delay so brew is killed, not
// orphaned, if it ignores the interrupt.
const shutdownWait = 12 * time.Second

// discoveringLoader finds Homebrew lazily, inside the first load, so the TUI
// starts immediately. Discovery is retried on every Load until it succeeds
// (so "press r" works after the user fixes their PATH); the client is then
// cached. It also executes confirmed plans with the discovered brew.
var (
	_ brew.Loader  = (*discoveringLoader)(nil)
	_ tui.Executor = (*discoveringLoader)(nil)
)

// errNotDiscovered is reported for every item of a plan executed before
// Homebrew was found. A plan needs a loaded inventory, so this is unexpected.
var errNotDiscovered = errors.New("homebrew has not been located yet; refresh the inventory and try again")

type discoveringLoader struct {
	mu     sync.Mutex
	client *brew.Client
}

func (l *discoveringLoader) Load(ctx context.Context) (brew.Inventory, error) {
	client, err := l.discover(ctx)
	if err != nil {
		return brew.Inventory{}, err
	}
	return client.Load(ctx)
}

func (l *discoveringLoader) discover(ctx context.Context) (*brew.Client, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.client != nil {
		return l.client, nil
	}
	client, err := brew.Discover(ctx)
	if err != nil {
		return nil, err
	}
	l.client = client
	return client, nil
}

// Execute runs p with the discovered brew executable. If brew has not been
// discovered, nothing runs: every item is reported failed with
// errNotDiscovered.
func (l *discoveringLoader) Execute(ctx context.Context, p plan.Plan, events chan<- run.Event) []run.Result {
	l.mu.Lock()
	client := l.client
	l.mu.Unlock()
	if client != nil {
		return run.Executor{BrewPath: client.Path()}.Execute(ctx, p, events)
	}

	results := make([]run.Result, len(p.Items))
	for i, it := range p.Items {
		results[i] = run.Result{
			Item:     it,
			Args:     it.Args(),
			Status:   run.StatusFailed,
			ExitCode: -1,
			Err:      errNotDiscovered,
		}
	}
	if events != nil {
		events <- run.Finished{Results: slices.Clone(results)}
	}
	return results
}
