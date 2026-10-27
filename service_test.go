package main

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestRuntimePathsCanBeConfigured(t *testing.T) {
	home := filepath.Join(t.TempDir(), "app")
	data := filepath.Join(t.TempDir(), "state")
	config := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("SUNFIRE_HOME", home)
	t.Setenv("SUNFIRE_DATA_DIR", data)
	t.Setenv("SUNFIRE_CONFIG_FILE", config)

	if got, want := applicationPath("Pictures"), filepath.Join(home, "Pictures"); got != want {
		t.Fatalf("applicationPath = %q; want %q", got, want)
	}
	if got, want := dataPath("Reminders", "reminders.json"), filepath.Join(data, "Reminders", "reminders.json"); got != want {
		t.Fatalf("dataPath = %q; want %q", got, want)
	}
	if got, want := configPath(), config; got != want {
		t.Fatalf("configPath = %q; want %q", got, want)
	}
}

func TestWorkerLifecycleStartsOnceAndStops(t *testing.T) {
	var lifecycle workerLifecycle
	ctx := context.Background()
	var starts atomic.Int32
	stopped := make(chan struct{})
	worker := func(ctx context.Context) {
		starts.Add(1)
		<-ctx.Done()
		close(stopped)
	}

	lifecycle.start(ctx, worker)
	lifecycle.start(ctx, worker)
	lifecycle.stop(time.Second)

	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("worker did not observe cancellation")
	}
	if got := starts.Load(); got != 1 {
		t.Fatalf("worker starts = %d; want exactly one", got)
	}
}

func TestRunPeriodicCanBeCancelledDuringInitialDelay(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var runs atomic.Int32
	done := make(chan struct{})
	go func() {
		defer close(done)
		runPeriodic(ctx, time.Hour, time.Hour, func() { runs.Add(1) })
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("periodic worker did not stop during initial delay")
	}
	if got := runs.Load(); got != 0 {
		t.Fatalf("task runs = %d; want 0 after cancellation", got)
	}
}
