// Package dispatchgate provides the admission boundary used while a message
// transport transitions from serving to quiesced.
package dispatchgate

import (
	"context"
	"sync"
	"sync/atomic"
)

// Gate linearizes dispatch admission with a one-way stop boundary. Work that
// began before Stop remains active and can be waited for with Wait; no work can
// begin after Stop returns.
type Gate struct {
	stopped atomic.Bool
	active  atomic.Int64
	stop    sync.Once
	done    chan struct{}

	idleMu     sync.Mutex
	idle       chan struct{}
	idleClosed bool
}

// New returns an open dispatch gate.
func New() *Gate {
	return &Gate{
		done: make(chan struct{}),
		idle: make(chan struct{}),
	}
}

// Begin admits one dispatch unless Stop has already linearized.
func (g *Gate) Begin() bool {
	if g.stopped.Load() {
		return false
	}
	g.active.Add(1)
	// Stop can linearize between the first check and the increment. Roll the
	// provisional admission back so Wait can never miss such a contender.
	if g.stopped.Load() {
		g.End()
		return false
	}
	return true
}

// End completes one dispatch admitted by Begin.
func (g *Gate) End() {
	remaining := g.active.Add(-1)
	if remaining < 0 {
		panic("dispatchgate: End without matching Begin")
	}
	if remaining != 0 || !g.stopped.Load() {
		return
	}
	g.idleMu.Lock()
	// A provisional pre-stop Begin can finish its rollback after another End
	// has already published idle, so closing is guarded as well as rechecked.
	if g.active.Load() == 0 && !g.idleClosed {
		close(g.idle)
		g.idleClosed = true
	}
	g.idleMu.Unlock()
}

// Stop closes admission and returns a channel closed at the stop boundary.
func (g *Gate) Stop() <-chan struct{} {
	g.stop.Do(func() {
		g.stopped.Store(true)
		close(g.done)
	})
	return g.done
}

// Done is closed once Stop has linearized.
func (g *Gate) Done() <-chan struct{} {
	return g.done
}

// Stopped reports whether admission is closed.
func (g *Gate) Stopped() bool {
	return g.stopped.Load()
}

// Wait waits for dispatches admitted before Stop to complete.
func (g *Gate) Wait(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		if g.active.Load() == 0 {
			return nil
		}
		g.idleMu.Lock()
		if g.active.Load() == 0 {
			g.idleMu.Unlock()
			return nil
		}
		idle := g.idle
		g.idleMu.Unlock()
		select {
		case <-idle:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
