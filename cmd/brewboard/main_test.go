package main

import (
	"context"
	"errors"
	"testing"

	"github.com/kaanemec/brew-board/internal/plan"
	"github.com/kaanemec/brew-board/internal/run"
)

func TestDiscoveringLoaderRetriesAfterNotFound(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	l := &discoveringLoader{}
	_, err := l.Load(context.Background())
	if err == nil {
		t.Skip("brew found in a standard prefix; cannot exercise the failure path")
	}
	if l.client != nil {
		t.Error("client cached after a failed discovery")
	}
	if _, err2 := l.Load(context.Background()); err2 == nil {
		t.Error("second Load should rediscover and fail again, not succeed")
	}
}

func TestExecuteBeforeDiscoveryFails(t *testing.T) {
	l := &discoveringLoader{}
	p := plan.Plan{}.WithCleanup()
	events := make(chan run.Event, 1)
	results := l.Execute(context.Background(), p, events)
	if len(results) != 1 || results[0].Status != run.StatusFailed || !errors.Is(results[0].Err, errNotDiscovered) {
		t.Fatalf("results = %+v, expected one failed result", results)
	}
	fin, ok := (<-events).(run.Finished)
	if !ok || len(fin.Results) != 1 {
		t.Errorf("expected Finished with one result, got %#v", fin)
	}
}
