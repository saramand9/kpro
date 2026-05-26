package core

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"time"
)

const (
	DefaultSyncQueueSize         = 64
	DefaultAsyncWorkers          = 0
	DefaultAsyncQueueSize        = 1024
	DefaultMaxInflightPerSession = 128
	DefaultSessionIdleTimeout    = time.Minute
	DefaultRouterShutdownTimeout = 5 * time.Second
)

type DispatchMode uint8

const (
	DispatchSync DispatchMode = iota
	DispatchAsync
)

type RouterConfig struct {
	SyncQueueSize         int
	AsyncWorkers          int
	AsyncQueueSize        int
	MaxInflightPerSession int
	SessionIdleTimeout    time.Duration
	ShutdownTimeout       time.Duration
}

type route struct {
	handler Handler
	mode    DispatchMode
}

type dispatchTask struct {
	ctx     Context
	handler Handler
	session *sessionExecutor
}

type Router struct {
	mu          sync.RWMutex
	routes      map[uint32]route
	middlewares []Middleware
	config      RouterConfig

	done      chan struct{}
	closeOnce sync.Once
	wg        sync.WaitGroup

	asyncQueue     chan dispatchTask
	asyncStartOnce sync.Once

	sessionsMu sync.Mutex
	sessions   map[uint64]*sessionExecutor
}

func NewRouter(config ...RouterConfig) *Router {
	cfg := normalizeRouterConfig(firstRouterConfig(config))
	r := &Router{
		routes:     make(map[uint32]route),
		config:     cfg,
		done:       make(chan struct{}),
		asyncQueue: make(chan dispatchTask, cfg.AsyncQueueSize),
		sessions:   make(map[uint64]*sessionExecutor),
	}
	return r
}

func (r *Router) Use(middlewares ...Middleware) {
	r.mu.Lock()
	r.middlewares = append(r.middlewares, middlewares...)
	r.mu.Unlock()
}

func (r *Router) Handle(msgID uint32, h Handler) {
	r.HandleSync(msgID, h)
}

func (r *Router) HandleSync(msgID uint32, h Handler) {
	r.handle(msgID, h, DispatchSync)
}

func (r *Router) HandleAsync(msgID uint32, h Handler) {
	r.handle(msgID, h, DispatchAsync)
}

func (r *Router) Dispatch(ctx Context) error {
	msg := ctx.Message()

	r.mu.RLock()
	rt := r.routes[msg.ID]
	middlewares := append([]Middleware(nil), r.middlewares...)
	r.mu.RUnlock()

	if rt.handler == nil {
		_ = ctx.Error(ErrNoHandler.Error())
		return ErrNoHandler
	}

	h := rt.handler
	for i := len(middlewares) - 1; i >= 0; i-- {
		h = middlewares[i](h)
	}

	task := dispatchTask{
		ctx:     ctx,
		handler: h,
		session: r.sessionFor(ctx.Session()),
	}
	switch rt.mode {
	case DispatchAsync:
		return r.submitAsync(task)
	default:
		return task.session.enqueue(task)
	}
}

func (r *Router) Close() {
	r.closeOnce.Do(func() {
		close(r.done)

		r.sessionsMu.Lock()
		sessions := make([]*sessionExecutor, 0, len(r.sessions))
		for _, session := range r.sessions {
			sessions = append(sessions, session)
		}
		r.sessionsMu.Unlock()

		for _, session := range sessions {
			session.close()
		}
	})
}

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

	done := make(chan struct{})
	go func() {
		r.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *Router) handle(msgID uint32, h Handler, mode DispatchMode) {
	if mode == DispatchAsync {
		r.startAsyncWorkers()
	}
	r.mu.Lock()
	r.routes[msgID] = route{handler: h, mode: mode}
	r.mu.Unlock()
}

func (r *Router) submitAsync(task dispatchTask) error {
	r.startAsyncWorkers()
	if err := task.session.acquire(); err != nil {
		_ = task.ctx.Error(err.Error())
		return err
	}
	select {
	case r.asyncQueue <- task:
		return nil
	case <-r.done:
		task.session.release()
		_ = task.ctx.Error(ErrRouterClosed.Error())
		return ErrRouterClosed
	default:
		task.session.release()
		_ = task.ctx.Error(ErrDispatchQueueFull.Error())
		return ErrDispatchQueueFull
	}
}

func (r *Router) startAsyncWorkers() {
	r.asyncStartOnce.Do(func() {
		for i := 0; i < r.config.AsyncWorkers; i++ {
			r.wg.Add(1)
			go r.asyncWorker()
		}
	})
}

func (r *Router) asyncWorker() {
	defer r.wg.Done()
	for {
		select {
		case task := <-r.asyncQueue:
			r.execute(task)
		case <-r.done:
			return
		}
	}
}

func (r *Router) execute(task dispatchTask) {
	defer task.session.release()
	defer func() {
		if recovered := recover(); recovered != nil {
			_ = task.ctx.Error(fmt.Sprintf("handler panic: %v", recovered))
		}
	}()
	if err := task.handler(task.ctx); err != nil {
		_ = task.ctx.Error(err.Error())
	}
}

func (r *Router) sessionFor(session Session) *sessionExecutor {
	id := session.ID()
	done := sessionDone(session)

	for {
		r.sessionsMu.Lock()
		if existing := r.sessions[id]; existing != nil && !existing.isClosed() {
			r.sessionsMu.Unlock()
			return existing
		}

		executor := newSessionExecutor(r, id, done)
		r.sessions[id] = executor
		r.wg.Add(1)
		go executor.run()
		r.sessionsMu.Unlock()
		return executor
	}
}

func (r *Router) removeSession(session *sessionExecutor) {
	r.sessionsMu.Lock()
	if r.sessions[session.id] == session {
		delete(r.sessions, session.id)
	}
	r.sessionsMu.Unlock()
}

type doneSession interface {
	Done() <-chan struct{}
}

func sessionDone(session Session) <-chan struct{} {
	if session, ok := session.(doneSession); ok {
		return session.Done()
	}
	return nil
}

type sessionExecutor struct {
	router *Router
	id     uint64
	done   <-chan struct{}
	queue  chan dispatchTask

	mu       sync.Mutex
	closed   bool
	inflight int
}

func newSessionExecutor(router *Router, id uint64, done <-chan struct{}) *sessionExecutor {
	return &sessionExecutor{
		router: router,
		id:     id,
		done:   done,
		queue:  make(chan dispatchTask, router.config.SyncQueueSize),
	}
}

func (s *sessionExecutor) enqueue(task dispatchTask) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.router.closed() {
		_ = task.ctx.Error(ErrRouterClosed.Error())
		return ErrRouterClosed
	}
	if s.router.config.MaxInflightPerSession > 0 && s.inflight >= s.router.config.MaxInflightPerSession {
		_ = task.ctx.Error(ErrTooManyInflight.Error())
		return ErrTooManyInflight
	}
	select {
	case s.queue <- task:
		s.inflight++
		return nil
	default:
		_ = task.ctx.Error(ErrDispatchQueueFull.Error())
		return ErrDispatchQueueFull
	}
}

func (s *sessionExecutor) acquire() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.router.closed() {
		return ErrRouterClosed
	}
	if s.router.config.MaxInflightPerSession > 0 && s.inflight >= s.router.config.MaxInflightPerSession {
		return ErrTooManyInflight
	}
	s.inflight++
	return nil
}

func (s *sessionExecutor) release() {
	s.mu.Lock()
	if s.inflight > 0 {
		s.inflight--
	}
	s.mu.Unlock()
}

func (s *sessionExecutor) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

func (s *sessionExecutor) run() {
	defer s.router.wg.Done()
	timer := time.NewTimer(s.router.config.SessionIdleTimeout)
	defer timer.Stop()

	for {
		select {
		case task := <-s.queue:
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
	if s.closed || len(s.queue) > 0 || s.inflight > 0 {
		s.mu.Unlock()
		return false
	}
	s.closed = true
	s.mu.Unlock()
	s.router.removeSession(s)
	return true
}

func (s *sessionExecutor) close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	for {
		select {
		case task := <-s.queue:
			if s.inflight > 0 {
				s.inflight--
			}
			_ = task.ctx.Error(ErrRouterClosed.Error())
		default:
			s.mu.Unlock()
			s.router.removeSession(s)
			return
		}
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
	if config.SessionIdleTimeout <= 0 {
		config.SessionIdleTimeout = DefaultSessionIdleTimeout
	}
	if config.ShutdownTimeout <= 0 {
		config.ShutdownTimeout = DefaultRouterShutdownTimeout
	}
	return config
}
