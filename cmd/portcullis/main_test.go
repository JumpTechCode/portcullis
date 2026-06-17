package main

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestRunRejectsMissingConfig(t *testing.T) {
	err := run("does-not-exist.yaml", false, 0)
	if err == nil {
		t.Fatal("expected run to fail for a missing config file")
	}
	if !strings.Contains(err.Error(), "does-not-exist.yaml") {
		t.Errorf("error should name the config path, got %v", err)
	}
}

func TestRunRejectsNonPositiveWatchInterval(t *testing.T) {
	// The config path is never read: the interval guard returns before any file
	// access, so the path is a sentinel.
	err := run("unused.yaml", true, 0)
	if err == nil {
		t.Fatal("expected run to fail when --watch is set with a non-positive interval")
	}
	if !strings.Contains(err.Error(), "watch-interval") {
		t.Errorf("error should name the watch-interval flag, got %v", err)
	}
}

func TestNewReloadTriggerCoalesces(t *testing.T) {
	reloads := make(chan struct{}, 1)
	trigger := newReloadTrigger(reloads)
	trigger()
	trigger()
	trigger()
	if got := len(reloads); got != 1 {
		t.Errorf("coalesced trigger queued %d reloads, want 1", got)
	}
}

func TestServeReloadsRunsAndStopsOnCancel(t *testing.T) {
	reloads := make(chan struct{}, 1)
	fired := make(chan struct{}, 1)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		serveReloads(ctx, reloads, func() { fired <- struct{}{} })
		close(done)
	}()

	reloads <- struct{}{}
	select {
	case <-fired:
	case <-time.After(time.Second):
		t.Fatal("serveReloads did not run the reload callback")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("serveReloads did not return after ctx was cancelled")
	}
}
