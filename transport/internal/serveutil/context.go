// Package serveutil contains lifecycle helpers shared by built-in transports.
package serveutil

import "context"

// ShutdownOnCancel invokes shutdown when ctx is cancelled. The watcher exits
// without invoking shutdown when the current serve call completes first. A nil
// context, a context without a Done channel, or a nil callback starts no
// goroutine. The returned channel closes when a watcher that was started exits.
func ShutdownOnCancel(ctx context.Context, serveDone <-chan struct{}, shutdown func()) <-chan struct{} {
	if ctx == nil || ctx.Done() == nil || serveDone == nil || shutdown == nil {
		return nil
	}
	watcherDone := make(chan struct{})
	go func() {
		defer close(watcherDone)
		select {
		case <-ctx.Done():
			shutdown()
		case <-serveDone:
			// A context-aware Serve call may return because ctx was cancelled
			// before this watcher was scheduled. Preserve the cancellation's
			// shutdown ownership instead of losing it to a simultaneous serve exit.
			select {
			case <-ctx.Done():
				shutdown()
			default:
			}
		}
	}()
	return watcherDone
}

// FinishContextWatcher marks the serve call complete and waits for any
// cancellation-triggered shutdown callback to return.
func FinishContextWatcher(serveDone chan struct{}, watcherDone <-chan struct{}) {
	close(serveDone)
	if watcherDone != nil {
		<-watcherDone
	}
}
