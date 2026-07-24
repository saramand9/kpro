// File: core/router.go
// Purpose: Defines shared KPro primitives used by transports, routers, and middleware.

package core

import (
	"context"
	"errors"
	"fmt"
	"log"
	"runtime"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// DefaultSyncQueueSize is the default per-session sync dispatch queue size.
	DefaultSyncQueueSize = 64
	// DefaultAsyncWorkers lets the router derive worker count from GOMAXPROCS.
	DefaultAsyncWorkers = 0
	// DefaultAsyncQueueSize is the default shared async dispatch queue size.
	DefaultAsyncQueueSize = 1024
	// DefaultMaxInflightPerSession limits outstanding work per session.
	DefaultMaxInflightPerSession = 128
	// DefaultSessionIdleTimeout removes idle session executors.
	DefaultSessionIdleTimeout = time.Minute
	// DefaultRouterShutdownTimeout bounds graceful router shutdown.
	DefaultRouterShutdownTimeout = 5 * time.Second
)

// DispatchMode controls whether a route is serialized per session or run by async workers.
type DispatchMode uint8

const (
	// DispatchSync serializes handler execution per session.
	DispatchSync DispatchMode = iota
	// DispatchAsync runs handler execution through shared async workers.
	DispatchAsync
)

// RouterConfig tunes message dispatch queues, worker counts, and shutdown behavior.
type RouterConfig struct {
	// SyncQueueSize is the per-session queue length for sync handlers.
	SyncQueueSize int
	// AsyncWorkers is the shared worker count for async handlers. Values <= 0 use a runtime-derived default.
	AsyncWorkers int
	// AsyncQueueSize is the shared queue length for async handler tasks.
	AsyncQueueSize int
	// MaxInflightPerSession caps queued or running work for one session.
	MaxInflightPerSession int
	// MaxInflight caps accepted work across every session. Values <= 0 use
	// DefaultMaxInflight.
	MaxInflight int
	// Backpressure selects reject or context-cancelable wait behavior when an
	// inflight or queue limit is reached. The zero value preserves rejection.
	// Handler-initiated reentrant dispatch under Wait must use DispatchContext
	// with a deadline because the current handler may itself own needed capacity.
	Backpressure BackpressurePolicy
	// SessionIdleTimeout removes a session executor after it becomes idle.
	SessionIdleTimeout time.Duration
	// ShutdownTimeout is used when Shutdown receives a context without deadline.
	ShutdownTimeout time.Duration
}

type route struct {
	handler           Handler
	invocationHandler InvocationHandler
	mode              DispatchMode
}

type dispatchTask struct {
	ctx     Context
	handler InvocationHandler
	session *sessionExecutor
}

// Router maps message ids to handlers and owns dispatch scheduling.
type Router struct {
	mu          sync.RWMutex
	routes      map[uint32]route
	middlewares []Middleware
	revision    uint64
	config      RouterConfig

	done      chan struct{}
	closeOnce sync.Once
	wg        sync.WaitGroup
	drainOnce sync.Once
	drained   chan struct{}
	state     atomic.Uint32

	asyncQueue     chan dispatchTask
	asyncSlots     chan struct{}
	asyncStartOnce sync.Once
	asyncMu        sync.Mutex

	globalSlots    chan struct{}
	globalInflight atomic.Int64
	syncQueued     atomic.Int64
	asyncQueued    atomic.Int64
	accepted       atomic.Uint64
	completed      atomic.Uint64
	dropped        atomic.Uint64
	rejected       atomic.Uint64
	waited         atomic.Uint64

	sessionsCreateMu sync.Mutex
	sessions         sync.Map
}

// NewRouter creates a Router with normalized defaults.
func NewRouter(config ...RouterConfig) *Router {
	cfg := normalizeRouterConfig(firstRouterConfig(config))
	r := &Router{
		routes:      make(map[uint32]route),
		config:      cfg,
		done:        make(chan struct{}),
		drained:     make(chan struct{}),
		asyncQueue:  make(chan dispatchTask, cfg.AsyncQueueSize),
		asyncSlots:  newCapacity(cfg.AsyncQueueSize),
		globalSlots: newCapacity(cfg.MaxInflight),
	}
	return r
}

// Use appends middleware that wraps all future dispatches through the router.
func (r *Router) Use(middlewares ...Middleware) {
	if len(middlewares) == 0 {
		return
	}
	r.mu.Lock()
	r.middlewares = append(r.middlewares, middlewares...)
	r.revision++
	r.mu.Unlock()
	r.rebuildMessageInvocationHandlers()
}

// Handle registers a sync handler for msgID.
func (r *Router) Handle(msgID uint32, h Handler) {
	if err := r.HandleChecked(msgID, h); err != nil {
		panic(err)
	}
}

// HandleSync registers a handler that is serialized per session.
func (r *Router) HandleSync(msgID uint32, h Handler) {
	if err := r.HandleSyncChecked(msgID, h); err != nil {
		panic(err)
	}
}

// HandleAsync registers a handler that is run by shared async workers.
func (r *Router) HandleAsync(msgID uint32, h Handler) {
	if err := r.HandleAsyncChecked(msgID, h); err != nil {
		panic(err)
	}
}

// HandleChecked registers a sync handler and reports invalid, duplicate, or late registration.
func (r *Router) HandleChecked(msgID uint32, h Handler) error {
	return r.HandleSyncChecked(msgID, h)
}

// HandleSyncChecked registers a serialized handler and reports invalid or late registration.
func (r *Router) HandleSyncChecked(msgID uint32, h Handler) error {
	return r.handle(msgID, h, DispatchSync)
}

// HandleAsyncChecked registers an async handler and reports invalid or late registration.
func (r *Router) HandleAsyncChecked(msgID uint32, h Handler) error {
	return r.handle(msgID, h, DispatchAsync)
}

// Dispatch submits a decoded message context using the configured
// Backpressure policy. With BackpressureWait, use DispatchContext when the
// caller needs to bound the wait; this compatibility method waits until the
// Router or Session closes.
func (r *Router) Dispatch(ctx Context) error {
	return r.DispatchContext(context.Background(), ctx)
}

// DispatchContext submits a decoded message context. Reject mode returns
// immediately when a global, per-session, or queue limit is reached. Wait mode
// waits for capacity until waitCtx, the Router, or the Session is canceled.
// The Router owns one ctx reference after this call and releases it on every
// success or failure path. Transport-managed contexts retain their caller
// reference until ReleaseTransportContext is called.
func (r *Router) DispatchContext(waitCtx context.Context, ctx Context) error {
	if waitCtx == nil {
		waitCtx = context.Background()
	}
	retainRouterContext(ctx)
	if err := r.ensureRunning(); err != nil {
		return r.rejectDispatch(ctx, err, false)
	}

	msg := ctx.Message()
	r.mu.RLock()
	rt := r.routes[msg.ID]
	r.mu.RUnlock()
	if rt.handler == nil {
		return r.rejectDispatch(ctx, ErrNoHandler, false)
	}

	waited, err := r.acquireGlobal(waitCtx, SessionDone(ctx.Session()))
	if err != nil {
		return r.rejectDispatch(ctx, err, waited)
	}

	session, sessionWaited, err := r.sessionForContext(waitCtx, ctx.Session())
	waited = waited || sessionWaited
	if err != nil {
		r.releaseGlobal()
		return r.rejectDispatch(ctx, err, waited)
	}

	task := dispatchTask{
		ctx:     ctx,
		handler: rt.invocationHandler,
		session: session,
	}
	var queueWaited bool
	switch rt.mode {
	case DispatchAsync:
		queueWaited, err = r.submitAsync(waitCtx, task)
	default:
		queueWaited, err = task.session.enqueueReserved(waitCtx, task, r.config.Backpressure)
	}
	waited = waited || queueWaited
	if err != nil {
		task.session.release()
		r.releaseGlobal()
		if err == errSessionExecutorClosed {
			err = ErrClosed
		}
		return r.rejectDispatch(ctx, err, waited)
	}

	if waited {
		r.waited.Add(1)
	}
	r.accepted.Add(1)
	return nil
}

func (r *Router) rejectDispatch(ctx Context, err error, waited bool) error {
	if waited {
		r.waited.Add(1)
	}
	r.rejected.Add(1)
	_ = ctx.Error(PublicErrorMessage(err))
	releaseContext(ctx)
	return err
}

// Close stops accepting dispatch work and releases queued contexts.
func (r *Router) Close() {
	r.beginDraining()
	r.closeOnce.Do(func() {
		close(r.done)
		r.asyncMu.Lock()
		close(r.asyncQueue)
		r.asyncMu.Unlock()

		r.sessionsCreateMu.Lock()
		sessions := make([]*sessionExecutor, 0)
		r.sessions.Range(func(_, value any) bool {
			sessions = append(sessions, value.(*sessionExecutor))
			return true
		})
		r.sessionsCreateMu.Unlock()

		for _, session := range sessions {
			session.close()
		}
	})
	r.drainDone()
}

// Shutdown closes the router and waits for in-flight handlers to finish or ctx to expire.
func (r *Router) Shutdown(ctx context.Context) error {
	r.Close()
	if ctx == nil {
		ctx = context.Background()
	}
	if _, ok := ctx.Deadline(); !ok && r.config.ShutdownTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.config.ShutdownTimeout)
		defer cancel()
	}

	select {
	case <-r.drainDone():
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *Router) handle(msgID uint32, h Handler, mode DispatchMode) error {
	if h == nil {
		return ErrNilHandler
	}
	if r.closed() {
		return ErrRouterClosed
	}
	for {
		r.mu.RLock()
		if _, exists := r.routes[msgID]; exists {
			r.mu.RUnlock()
			return fmt.Errorf("%w: message id %d", ErrHandlerAlreadyRegistered, msgID)
		}
		revision := r.revision
		middlewares := append([]Middleware(nil), r.middlewares...)
		r.mu.RUnlock()

		invocationHandler := buildMessageInvocationHandler(h, middlewares)

		r.mu.Lock()
		if revision != r.revision && !r.closed() {
			r.mu.Unlock()
			continue
		}
		if r.closed() {
			r.mu.Unlock()
			return ErrRouterClosed
		}
		if _, exists := r.routes[msgID]; exists {
			r.mu.Unlock()
			return fmt.Errorf("%w: message id %d", ErrHandlerAlreadyRegistered, msgID)
		}
		r.routes[msgID] = route{
			handler:           h,
			invocationHandler: invocationHandler,
			mode:              mode,
		}
		r.revision++
		r.mu.Unlock()
		return nil
	}
}

func (r *Router) rebuildMessageInvocationHandlers() {
	for {
		r.mu.RLock()
		revision := r.revision
		middlewares := append([]Middleware(nil), r.middlewares...)
		routes := make(map[uint32]route, len(r.routes))
		for msgID, registered := range r.routes {
			routes[msgID] = registered
		}
		r.mu.RUnlock()

		compiled := make(map[uint32]InvocationHandler, len(routes))
		for msgID, registered := range routes {
			compiled[msgID] = buildMessageInvocationHandler(registered.handler, middlewares)
		}

		r.mu.Lock()
		if revision != r.revision {
			r.mu.Unlock()
			continue
		}
		for msgID, invocationHandler := range compiled {
			registered := r.routes[msgID]
			registered.invocationHandler = invocationHandler
			r.routes[msgID] = registered
		}
		r.mu.Unlock()
		return
	}
}

func buildMessageInvocationHandler(handler Handler, middlewares []Middleware) InvocationHandler {
	if handler == nil {
		return nil
	}
	invocationHandler := InvocationHandler(func(invocation Invocation) error {
		ctx, ok := invocation.Native().(Context)
		if !ok {
			return fmt.Errorf("message invocation native context has type %T", invocation.Native())
		}
		return handler(withMessageContext(ctx, invocation.Context()))
	})
	if len(middlewares) > 0 {
		invocationHandler = Chain(middlewares...)(invocationHandler)
	}
	return invocationHandler
}

func (r *Router) submitAsync(ctx context.Context, task dispatchTask) (bool, error) {
	if !r.startAsyncWorkers() {
		return false, ErrRouterClosed
	}
	waited, err := acquireCapacity(
		ctx,
		r.asyncSlots,
		r.config.Backpressure,
		ErrDispatchQueueFull,
		r.done,
		SessionDone(task.ctx.Session()),
		nil,
		nil,
	)
	if err != nil {
		return waited, err
	}
	r.asyncMu.Lock()
	defer r.asyncMu.Unlock()
	if r.closed() {
		releaseCapacity(r.asyncSlots)
		return waited, ErrRouterClosed
	}
	if channelClosed(SessionDone(task.ctx.Session())) {
		releaseCapacity(r.asyncSlots)
		return waited, ErrClosed
	}
	r.asyncQueued.Add(1)
	select {
	case r.asyncQueue <- task:
		return waited, nil
	default:
		r.asyncQueued.Add(-1)
		releaseCapacity(r.asyncSlots)
		return waited, ErrDispatchQueueFull
	}
}

func (r *Router) drainDone() <-chan struct{} {
	r.drainOnce.Do(func() {
		go func() {
			r.wg.Wait()
			r.state.Store(uint32(StateStopped))
			close(r.drained)
		}()
	})
	return r.drained
}

// State returns the current observable Router lifecycle state.
func (r *Router) State() LifecycleState {
	return LifecycleState(r.state.Load())
}

// Snapshot returns lightweight admission, queue, and completion statistics.
func (r *Router) Snapshot() RouterStats {
	sessions := 0
	r.sessions.Range(func(_, _ any) bool {
		sessions++
		return true
	})
	return RouterStats{
		State:                       r.State(),
		Backpressure:                r.config.Backpressure,
		Inflight:                    r.globalInflight.Load(),
		MaxInflight:                 r.config.MaxInflight,
		MaxInflightPerSession:       r.config.MaxInflightPerSession,
		Sessions:                    sessions,
		SyncQueueDepth:              r.syncQueued.Load(),
		SyncQueueCapacityPerSession: r.config.SyncQueueSize,
		AsyncQueueDepth:             r.asyncQueued.Load(),
		AsyncQueueCapacity:          r.config.AsyncQueueSize,
		Accepted:                    r.accepted.Load(),
		Completed:                   r.completed.Load(),
		Dropped:                     r.dropped.Load(),
		Rejected:                    r.rejected.Load(),
		Waited:                      r.waited.Load(),
	}
}

func (r *Router) ensureRunning() error {
	for {
		switch state := r.State(); state {
		case StateCreated:
			if r.state.CompareAndSwap(uint32(StateCreated), uint32(StateStarting)) {
				return r.publishRunning()
			}
		case StateStarting:
			runtime.Gosched()
		case StateRunning:
			return nil
		case StateDraining, StateStopped:
			return ErrRouterClosed
		default:
			return ErrRouterClosed
		}
	}
}

func (r *Router) publishRunning() error {
	// Close may claim Starting and move the Router to Draining. A conditional
	// transition prevents startup from reviving a Router after draining began.
	if r.state.CompareAndSwap(uint32(StateStarting), uint32(StateRunning)) {
		return nil
	}
	return ErrRouterClosed
}

func (r *Router) beginDraining() {
	for {
		state := r.State()
		switch state {
		case StateCreated, StateStarting, StateRunning:
			if r.state.CompareAndSwap(uint32(state), uint32(StateDraining)) {
				return
			}
		case StateDraining, StateStopped:
			return
		default:
			return
		}
	}
}

func (r *Router) startAsyncWorkers() bool {
	r.asyncMu.Lock()
	defer r.asyncMu.Unlock()
	if r.closed() {
		return false
	}
	r.asyncStartOnce.Do(func() {
		for i := 0; i < r.config.AsyncWorkers; i++ {
			r.wg.Add(1)
			go r.asyncWorker()
		}
	})
	return true
}

func (r *Router) asyncWorker() {
	defer r.wg.Done()
	for task := range r.asyncQueue {
		r.asyncQueued.Add(-1)
		releaseCapacity(r.asyncSlots)
		r.execute(task)
	}
}

func (r *Router) execute(task dispatchTask) {
	// Completion is the final publication for an accepted task: observers that
	// see it must also see the context and all admission capacity released.
	defer r.completed.Add(1)
	defer releaseContext(task.ctx)
	defer r.releaseGlobal()
	defer task.session.release()
	defer func() {
		if recovered := recover(); recovered != nil {
			log.Printf("message handler panic: %v\n%s", recovered, debug.Stack())
			_ = task.ctx.Error(PublicErrorMessage(ErrRecovered))
		}
	}()
	inv := NewMessageInvocation(context.Background(), task.ctx)
	defer inv.Release()
	if err := task.handler(inv); err != nil {
		log.Printf("message handler error: %v", err)
		_ = task.ctx.Error(PublicErrorMessage(err))
	}
}

func (r *Router) sessionFor(session Session) (*sessionExecutor, error) {
	executor, _, err := r.sessionForContext(context.Background(), session)
	return executor, err
}

func (r *Router) sessionForContext(ctx context.Context, session Session) (*sessionExecutor, bool, error) {
	done := SessionDone(session)
	key := sessionExecutorKey{id: session.ID(), done: done}
	waited := false

	for {
		if channelClosed(done) {
			return nil, waited, ErrClosed
		}
		if value, ok := r.sessions.Load(key); ok {
			existing := value.(*sessionExecutor)
			acquireWaited, err := existing.acquireContext(ctx, r.config.Backpressure)
			waited = waited || acquireWaited
			if err == nil {
				return existing, waited, nil
			} else if err != errSessionExecutorClosed {
				return nil, waited, err
			}
			r.sessions.CompareAndDelete(key, existing)
			existing.close()
			continue
		}

		r.sessionsCreateMu.Lock()
		if value, ok := r.sessions.Load(key); ok {
			existing := value.(*sessionExecutor)
			// Capacity waits execute caller-controlled cancellation paths and may
			// block. Never hold the global creation lock while waiting on one hot
			// session, otherwise unrelated sessions cannot be created.
			r.sessionsCreateMu.Unlock()
			acquireWaited, err := existing.acquireContext(ctx, r.config.Backpressure)
			waited = waited || acquireWaited
			if err == nil {
				return existing, waited, nil
			} else if err != errSessionExecutorClosed {
				return nil, waited, err
			}
			r.sessions.CompareAndDelete(key, existing)
			existing.close()
			continue
		}
		if r.closed() {
			r.sessionsCreateMu.Unlock()
			return nil, waited, ErrRouterClosed
		}
		if channelClosed(done) {
			r.sessionsCreateMu.Unlock()
			return nil, waited, ErrClosed
		}

		executor := newSessionExecutor(r, key)
		acquireWaited, err := executor.acquireContext(ctx, r.config.Backpressure)
		waited = waited || acquireWaited
		if err != nil {
			r.sessionsCreateMu.Unlock()
			return nil, waited, err
		}
		r.sessions.Store(key, executor)
		r.wg.Add(1)
		go executor.run()
		r.sessionsCreateMu.Unlock()
		return executor, waited, nil
	}
}

func (r *Router) removeSession(session *sessionExecutor) {
	r.sessions.CompareAndDelete(session.key, session)
}

type sessionExecutorKey struct {
	id   uint64
	done <-chan struct{}
}

type sessionExecutor struct {
	router        *Router
	key           sessionExecutorKey
	done          <-chan struct{}
	queue         chan dispatchTask
	closedCh      chan struct{}
	inflightSlots chan struct{}
	queueSlots    chan struct{}

	mu       sync.Mutex
	closed   atomic.Bool
	inflight int
}

var errSessionExecutorClosed = errors.New("session executor closed")

func newSessionExecutor(router *Router, key sessionExecutorKey) *sessionExecutor {
	return &sessionExecutor{
		router:        router,
		key:           key,
		done:          key.done,
		queue:         make(chan dispatchTask, router.config.SyncQueueSize),
		closedCh:      make(chan struct{}),
		inflightSlots: newCapacity(router.config.MaxInflightPerSession),
		queueSlots:    newCapacity(router.config.SyncQueueSize),
	}
}

func (s *sessionExecutor) enqueueReserved(ctx context.Context, task dispatchTask, policy BackpressurePolicy) (bool, error) {
	waited, err := acquireCapacity(
		ctx,
		s.queueSlots,
		policy,
		ErrDispatchQueueFull,
		s.router.done,
		s.done,
		s.closedCh,
		errSessionExecutorClosed,
	)
	if err != nil {
		return waited, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.router.closed() {
		releaseCapacity(s.queueSlots)
		return waited, ErrRouterClosed
	}
	if s.closed.Load() {
		releaseCapacity(s.queueSlots)
		return waited, errSessionExecutorClosed
	}
	if channelClosed(s.done) {
		releaseCapacity(s.queueSlots)
		return waited, ErrClosed
	}
	s.router.syncQueued.Add(1)
	select {
	case s.queue <- task:
		return waited, nil
	default:
		s.router.syncQueued.Add(-1)
		releaseCapacity(s.queueSlots)
		return waited, ErrDispatchQueueFull
	}
}

func (s *sessionExecutor) acquire() error {
	_, err := s.acquireContext(context.Background(), s.router.config.Backpressure)
	return err
}

func (s *sessionExecutor) acquireContext(ctx context.Context, policy BackpressurePolicy) (bool, error) {
	waited, err := acquireCapacity(
		ctx,
		s.inflightSlots,
		policy,
		ErrTooManyInflight,
		s.router.done,
		s.done,
		s.closedCh,
		errSessionExecutorClosed,
	)
	if err != nil {
		return waited, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.router.closed() {
		releaseCapacity(s.inflightSlots)
		return waited, ErrRouterClosed
	}
	if s.closed.Load() {
		releaseCapacity(s.inflightSlots)
		return waited, errSessionExecutorClosed
	}
	if channelClosed(s.done) {
		releaseCapacity(s.inflightSlots)
		return waited, ErrClosed
	}
	s.inflight++
	return waited, nil
}

func (s *sessionExecutor) release() {
	released := false
	s.mu.Lock()
	if s.inflight > 0 {
		s.inflight--
		released = true
	}
	s.mu.Unlock()
	if released {
		releaseCapacity(s.inflightSlots)
	}
}

func (s *sessionExecutor) isClosed() bool {
	return s.closed.Load()
}

func (s *sessionExecutor) run() {
	defer s.router.wg.Done()
	timer := time.NewTimer(s.router.config.SessionIdleTimeout)
	defer timer.Stop()

	for {
		select {
		case task := <-s.queue:
			s.router.syncQueued.Add(-1)
			releaseCapacity(s.queueSlots)
			resetTimer(timer, s.router.config.SessionIdleTimeout)
			s.router.execute(task)
		case <-s.done:
			s.close()
			return
		case <-s.router.done:
			s.close()
			return
		case <-timer.C:
			if s.closeIfIdle() {
				return
			}
			timer.Reset(s.router.config.SessionIdleTimeout)
		}
	}
}

func (s *sessionExecutor) closeIfIdle() bool {
	s.mu.Lock()
	if s.closed.Load() || len(s.queue) > 0 || s.inflight > 0 {
		s.mu.Unlock()
		return false
	}
	s.markClosedLocked()
	s.mu.Unlock()
	s.router.removeSession(s)
	return true
}

func (s *sessionExecutor) close() {
	s.mu.Lock()
	if s.closed.Load() {
		s.mu.Unlock()
		return
	}
	s.markClosedLocked()
	drained := make([]dispatchTask, 0, len(s.queue))
	for {
		select {
		case task := <-s.queue:
			if s.inflight > 0 {
				s.inflight--
				releaseCapacity(s.inflightSlots)
			}
			s.router.syncQueued.Add(-1)
			releaseCapacity(s.queueSlots)
			drained = append(drained, task)
		default:
			s.mu.Unlock()
			s.router.removeSession(s)
			for _, task := range drained {
				s.router.releaseGlobal()
				s.router.dropped.Add(1)
				_ = task.ctx.Error(PublicErrorMessage(ErrRouterClosed))
				releaseContext(task.ctx)
			}
			return
		}
	}
}

func (s *sessionExecutor) markClosedLocked() {
	if s.closed.CompareAndSwap(false, true) {
		close(s.closedCh)
	}
}

func (r *Router) closed() bool {
	select {
	case <-r.done:
		return true
	default:
		return false
	}
}

func (r *Router) acquireGlobal(ctx context.Context, sessionDone <-chan struct{}) (bool, error) {
	waited, err := acquireCapacity(
		ctx,
		r.globalSlots,
		r.config.Backpressure,
		ErrGlobalInflightLimit,
		r.done,
		sessionDone,
		nil,
		nil,
	)
	if err != nil {
		return waited, err
	}
	if r.closed() {
		releaseCapacity(r.globalSlots)
		return waited, ErrRouterClosed
	}
	if channelClosed(sessionDone) {
		releaseCapacity(r.globalSlots)
		return waited, ErrClosed
	}
	r.globalInflight.Add(1)
	return waited, nil
}

func (r *Router) releaseGlobal() {
	r.globalInflight.Add(-1)
	releaseCapacity(r.globalSlots)
}

func newCapacity(size int) chan struct{} {
	return make(chan struct{}, size)
}

func acquireCapacity(
	ctx context.Context,
	slots chan struct{},
	policy BackpressurePolicy,
	fullErr error,
	routerDone <-chan struct{},
	sessionDone <-chan struct{},
	ownerDone <-chan struct{},
	ownerErr error,
) (bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if channelClosed(routerDone) {
		return false, ErrRouterClosed
	}
	if channelClosed(sessionDone) {
		return false, ErrClosed
	}
	if channelClosed(ownerDone) {
		return false, ownerErr
	}

	select {
	case slots <- struct{}{}:
		if err := ctx.Err(); err != nil {
			releaseCapacity(slots)
			return false, err
		}
		return false, nil
	default:
	}
	if policy != BackpressureWait {
		return false, fullErr
	}

	select {
	case slots <- struct{}{}:
		if err := ctx.Err(); err != nil {
			releaseCapacity(slots)
			return true, err
		}
		return true, nil
	case <-ctx.Done():
		return true, ctx.Err()
	case <-routerDone:
		return true, ErrRouterClosed
	case <-sessionDone:
		return true, ErrClosed
	case <-ownerDone:
		return true, ownerErr
	}
}

func releaseCapacity(slots <-chan struct{}) {
	select {
	case <-slots:
	default:
		panic("core: capacity released without matching acquisition")
	}
}

func channelClosed(done <-chan struct{}) bool {
	if done == nil {
		return false
	}
	select {
	case <-done:
		return true
	default:
		return false
	}
}

func resetTimer(timer *time.Timer, d time.Duration) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(d)
}

func firstRouterConfig(config []RouterConfig) RouterConfig {
	if len(config) == 0 {
		return RouterConfig{}
	}
	if len(config) > 1 {
		panic("core.NewRouter accepts at most one RouterConfig")
	}
	return config[0]
}

func normalizeRouterConfig(config RouterConfig) RouterConfig {
	if config.SyncQueueSize <= 0 {
		config.SyncQueueSize = DefaultSyncQueueSize
	}
	if config.AsyncWorkers <= 0 {
		config.AsyncWorkers = runtime.GOMAXPROCS(0) * 4
		if config.AsyncWorkers < 4 {
			config.AsyncWorkers = 4
		}
	}
	if config.AsyncQueueSize <= 0 {
		config.AsyncQueueSize = DefaultAsyncQueueSize
	}
	if config.MaxInflightPerSession <= 0 {
		config.MaxInflightPerSession = DefaultMaxInflightPerSession
	}
	if config.MaxInflight <= 0 {
		config.MaxInflight = DefaultMaxInflight
	}
	if config.Backpressure != BackpressureWait {
		config.Backpressure = BackpressureReject
	}
	if config.SessionIdleTimeout <= 0 {
		config.SessionIdleTimeout = DefaultSessionIdleTimeout
	}
	if config.ShutdownTimeout <= 0 {
		config.ShutdownTimeout = DefaultRouterShutdownTimeout
	}
	return config
}
