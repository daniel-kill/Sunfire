package main

import (
	"context"
	"sync"
	"time"
)

func runPeriodic(ctx context.Context, initialDelay, interval time.Duration, task func()) {
	if ctx.Err() != nil {
		return
	}
	if initialDelay > 0 {
		timer := time.NewTimer(initialDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
	if ctx.Err() != nil {
		return
	}

	task()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			task()
		}
	}
}

type workerLifecycle struct {
	mu         sync.Mutex
	started    bool
	cancelFunc context.CancelFunc
	wg         sync.WaitGroup
}

func (w *workerLifecycle) start(parent context.Context, workers ...func(context.Context)) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.started {
		return
	}
	ctx, cancel := context.WithCancel(parent)
	w.cancelFunc = cancel
	w.started = true
	for _, worker := range workers {
		worker := worker
		w.wg.Add(1)
		go func() {
			defer w.wg.Done()
			worker(ctx)
		}()
	}
}

func (w *workerLifecycle) stop(timeout time.Duration) {
	w.cancel()
	w.wait(timeout)
}

func (w *workerLifecycle) cancel() {
	w.mu.Lock()
	cancel := w.cancelFunc
	w.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (w *workerLifecycle) wait(timeout time.Duration) {
	done := make(chan struct{})
	go func() {
		w.wg.Wait()
		close(done)
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
	}
}
