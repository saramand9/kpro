// File: legacy/server.go
// Purpose: Implements or tests the legacy TCP validation server.

package legacy

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	pprof "net/http/pprof"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/saramand9/bignet/kpro/core"
)

type requestTask struct {
	conn *Conn
	msg  Message
}

// Server is the legacy TCP validation server retained for load testing.
type Server struct {
	config  Config
	ln      net.Listener
	conns   *ConnManager
	metrics *Metrics

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	nextID uint64
	tasks  chan requestTask

	routerMu    sync.RWMutex
	handlers    map[uint32]Handler
	middlewares []core.Middleware
}

// NewServer creates a legacy TCP validation server.
func NewServer(c Config) *Server {
	c = normalizeConfig(c)
	if c.WorkerCount <= 0 {
		c.WorkerCount = runtime.GOMAXPROCS(0) * 2
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Server{
		config:   c,
		conns:    NewConnManager(),
		metrics:  NewMetrics(),
		ctx:      ctx,
		cancel:   cancel,
		tasks:    make(chan requestTask, c.WorkerQueueSize),
		handlers: make(map[uint32]Handler),
	}
}

// Handle registers a handler for a legacy message id.
func (s *Server) Handle(msgID uint32, h Handler) {
	s.routerMu.Lock()
	s.handlers[msgID] = h
	s.routerMu.Unlock()
}

// Use installs middleware on legacy message dispatch.
func (s *Server) Use(middlewares ...core.Middleware) {
	s.routerMu.Lock()
	s.middlewares = append(s.middlewares, middlewares...)
	s.routerMu.Unlock()
}

// Metrics returns the live metrics collector.
func (s *Server) Metrics() *Metrics {
	return s.metrics
}

// ListenAndServe starts the legacy TCP listener and optional metrics server.
func (s *Server) ListenAndServe() error {
	addr := fmt.Sprintf("%s:%d", s.config.Host, s.config.Port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	s.ln = ln

	for i := 0; i < s.config.WorkerCount; i++ {
		s.wg.Add(1)
		go s.worker(i)
	}
	s.wg.Add(1)
	go s.sweeper()

	if s.config.EnableMetrics {
		s.wg.Add(1)
		go s.serveMetrics()
	}

	log.Printf("[%s] listening on %s, workers=%d, max_conn=%d", s.config.Name, addr, s.config.WorkerCount, s.config.MaxConnections)
	return s.acceptLoop()
}

// Shutdown stops the listener, closes active connections, and waits for workers.
func (s *Server) Shutdown() {
	s.cancel()
	if s.ln != nil {
		_ = s.ln.Close()
	}
	s.conns.CloseAll()
	s.wg.Wait()
}

func (s *Server) acceptLoop() error {
	for {
		raw, err := s.ln.Accept()
		if err != nil {
			select {
			case <-s.ctx.Done():
				return nil
			default:
				return err
			}
		}
		if atomic.LoadInt64(&s.metrics.activeConnections) >= int64(s.config.MaxConnections) {
			atomic.AddInt64(&s.metrics.rejectedConnections, 1)
			atomic.AddInt64(&s.metrics.rejectedMaxConns, 1)
			_ = raw.Close()
			continue
		}
		ip := remoteIP(raw.RemoteAddr())
		if s.config.MaxConnectionsPerIP > 0 && s.conns.CountByIP(ip) >= s.config.MaxConnectionsPerIP {
			atomic.AddInt64(&s.metrics.rejectedConnections, 1)
			atomic.AddInt64(&s.metrics.rejectedPerIP, 1)
			_ = raw.Close()
			continue
		}
		id := atomic.AddUint64(&s.nextID, 1)
		conn := &Conn{
			id:        id,
			server:    s,
			raw:       raw,
			remoteIP:  ip,
			sendQueue: make(chan []byte, s.config.SendQueueSize),
			done:      make(chan struct{}),
		}
		s.conns.Add(conn)
		s.metrics.incActive()
		conn.start()
	}
}

func (s *Server) worker(id int) {
	defer s.wg.Done()
	for {
		select {
		case <-s.ctx.Done():
			return
		case task := <-s.tasks:
			s.dispatch(task)
		}
	}
}

func (s *Server) dispatch(task requestTask) {
	defer atomic.AddInt32(&task.conn.inflight, -1)
	defer func() {
		if r := recover(); r != nil {
			atomic.AddInt64(&s.metrics.panicRecovered, 1)
			_ = task.conn.Send(Message{ID: MsgError, Seq: task.msg.Seq, Data: []byte("handler panic")})
		}
	}()

	s.routerMu.RLock()
	h := s.handlers[task.msg.ID]
	middlewares := append([]core.Middleware(nil), s.middlewares...)
	s.routerMu.RUnlock()
	if h == nil {
		atomic.AddInt64(&s.metrics.handlerErrors, 1)
		_ = task.conn.Send(Message{ID: MsgError, Seq: task.msg.Seq, Data: []byte(ErrNoHandler.Error())})
		return
	}
	ctx := core.AcquireContext(task.conn, task.msg)
	inv := core.NewMessageInvocation(context.Background(), ctx)
	handler := core.Chain(middlewares...)(func(inv core.Invocation) error {
		msgCtx, ok := inv.Native().(core.Context)
		if !ok {
			msgCtx = ctx
		}
		return h(msgCtx)
	})
	if err := handler(inv); err != nil {
		atomic.AddInt64(&s.metrics.handlerErrors, 1)
		_ = task.conn.Send(Message{ID: MsgError, Seq: task.msg.Seq, Data: []byte(err.Error())})
	}
}

func (s *Server) sweeper() {
	defer s.wg.Done()
	ticker := time.NewTicker(s.config.SweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			deadline := time.Now().Add(-s.config.IdleTimeout)
			for _, c := range s.conns.Snapshot() {
				if c.LastSeen().Before(deadline) {
					c.Close()
				}
			}
		}
	}
}

func (s *Server) serveMetrics() {
	defer s.wg.Done()
	mux := http.NewServeMux()
	mux.Handle("/metrics", s.metrics)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	mux.Handle("/debug/pprof/goroutine", pprof.Handler("goroutine"))
	mux.Handle("/debug/pprof/heap", pprof.Handler("heap"))
	mux.Handle("/debug/pprof/threadcreate", pprof.Handler("threadcreate"))
	mux.Handle("/debug/pprof/block", pprof.Handler("block"))
	addr := fmt.Sprintf("%s:%d", s.config.MetricsHost, s.config.MetricsPort)
	server := &http.Server{Addr: addr, Handler: mux}
	go func() {
		<-s.ctx.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	}()
	log.Printf("[%s] metrics listening on http://%s/metrics", s.config.Name, addr)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Printf("[%s] metrics server error: %v", s.config.Name, err)
	}
}
