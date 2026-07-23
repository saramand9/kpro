package serveutil

import (
	"context"
	"testing"
	"time"
)

func TestShutdownOnCancelInvokesCallback(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	serveDone := make(chan struct{})
	called := make(chan struct{})
	ShutdownOnCancel(ctx, serveDone, func() { close(called) })
	cancel()
	select {
	case <-called:
	case <-time.After(time.Second):
		t.Fatal("shutdown callback was not invoked")
	}
}

func TestShutdownOnCancelStopsWithServe(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	serveDone := make(chan struct{})
	called := make(chan struct{}, 1)
	watcherDone := ShutdownOnCancel(ctx, serveDone, func() { called <- struct{}{} })
	close(serveDone)
	select {
	case <-watcherDone:
	case <-time.After(time.Second):
		t.Fatal("context watcher did not stop with serve")
	}
	cancel()
	select {
	case <-called:
		t.Fatal("shutdown callback ran after serve completed")
	case <-time.After(20 * time.Millisecond):
	}
}

func TestShutdownOnCancelPreservesCancellationWhenServeAlsoEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	serveDone := make(chan struct{})
	called := make(chan struct{}, 1)
	cancel()
	close(serveDone)
	watcherDone := ShutdownOnCancel(ctx, serveDone, func() { called <- struct{}{} })
	select {
	case <-watcherDone:
	case <-time.After(time.Second):
		t.Fatal("context watcher did not stop")
	}
	select {
	case <-called:
	default:
		t.Fatal("shutdown callback was lost to simultaneous serve completion")
	}
}

func TestFinishContextWatcherWaitsForShutdownCallback(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	serveDone := make(chan struct{})
	release := make(chan struct{})
	watcherDone := ShutdownOnCancel(ctx, serveDone, func() { <-release })
	cancel()
	finished := make(chan struct{})
	go func() {
		FinishContextWatcher(serveDone, watcherDone)
		close(finished)
	}()
	select {
	case <-finished:
		t.Fatal("FinishContextWatcher returned before shutdown completed")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("FinishContextWatcher did not return after shutdown completed")
	}
}

func TestShutdownOnCancelIgnoresBackgroundAndNilContext(t *testing.T) {
	called := make(chan struct{}, 1)
	ShutdownOnCancel(context.Background(), make(chan struct{}), func() { called <- struct{}{} })
	ShutdownOnCancel(nil, make(chan struct{}), func() { called <- struct{}{} })
	select {
	case <-called:
		t.Fatal("shutdown callback ran without a cancellable context")
	case <-time.After(20 * time.Millisecond):
	}
}
