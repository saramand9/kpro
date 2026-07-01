package httpx

import (
	"context"
	"hash/fnv"
	"net"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/saramand9/bignet/kpro/core"
)

const (
	MessageHeaderMethod = ":method"
	MessageHeaderPath   = ":path"
	MessageHeaderQuery  = ":query"
	MessageHeaderStatus = ":status"

	MessageMetaMethod = "method"
	MessageMetaPath   = "path"
	MessageMetaQuery  = "query"
	MessageMetaHost   = "host"
)

type MessageEndpoint struct {
	endpoint *Endpoint
	router   *core.Router
	seq      uint64
}

func NewMessage(config Config, routerConfig ...core.RouterConfig) *MessageEndpoint {
	return &MessageEndpoint{
		endpoint: New(config),
		router:   core.NewRouter(routerConfig...),
	}
}

func (e *MessageEndpoint) Name() string {
	return e.endpoint.Name()
}

func (e *MessageEndpoint) Endpoint() *Endpoint {
	return e.endpoint
}

func (e *MessageEndpoint) Use(middlewares ...core.Middleware) *MessageEndpoint {
	e.router.Use(middlewares...)
	return e
}

func (e *MessageEndpoint) Handle(msgID uint32, h core.Handler) *MessageEndpoint {
	e.router.Handle(msgID, h)
	return e
}

func (e *MessageEndpoint) HandleSync(msgID uint32, h core.Handler) *MessageEndpoint {
	e.router.HandleSync(msgID, h)
	return e
}

func (e *MessageEndpoint) HandleAsync(msgID uint32, h core.Handler) *MessageEndpoint {
	e.router.HandleAsync(msgID, h)
	return e
}

func (e *MessageEndpoint) Route(method, path string, msgID uint32) *MessageEndpoint {
	e.endpoint.Handle(method, path, e.messageHandler(msgID))
	return e
}

func (e *MessageEndpoint) Get(path string, msgID uint32) *MessageEndpoint {
	return e.Route(http.MethodGet, path, msgID)
}

func (e *MessageEndpoint) Post(path string, msgID uint32) *MessageEndpoint {
	return e.Route(http.MethodPost, path, msgID)
}

func (e *MessageEndpoint) Put(path string, msgID uint32) *MessageEndpoint {
	return e.Route(http.MethodPut, path, msgID)
}

func (e *MessageEndpoint) Delete(path string, msgID uint32) *MessageEndpoint {
	return e.Route(http.MethodDelete, path, msgID)
}

func (e *MessageEndpoint) ListenAndServe(ctx context.Context) error {
	return e.endpoint.ListenAndServe(ctx)
}

func (e *MessageEndpoint) Shutdown(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	transportErr := e.endpoint.Shutdown(ctx)
	routerErr := e.router.Shutdown(ctx)
	if transportErr != nil {
		return transportErr
	}
	return routerErr
}

func (e *MessageEndpoint) messageHandler(msgID uint32) Handler {
	return func(ctx *Context) error {
		body, err := ctx.Body()
		if err != nil {
			return err
		}
		reqSession := newMessageSession(ctx.w, ctx.r, e.endpoint.config.TLS)
		msg := core.Message{
			ID:      msgID,
			Seq:     atomic.AddUint64(&e.seq, 1),
			Data:    body,
			Headers: messageHeaders(ctx.r),
			Meta:    messageMeta(ctx.r),
		}
		if err := e.router.Dispatch(core.AcquireContext(reqSession, msg)); err != nil {
			_ = reqSession.Send(core.Message{
				ID:   core.MsgError,
				Seq:  msg.Seq,
				Data: []byte(err.Error()),
			})
		}
		select {
		case <-reqSession.responded:
		case <-ctx.r.Context().Done():
			_ = reqSession.Close()
		}
		return nil
	}
}

func messageHeaders(r *http.Request) map[string]string {
	headers := make(map[string]string, len(r.Header)+4)
	headers[MessageHeaderMethod] = r.Method
	if r.URL != nil {
		headers[MessageHeaderPath] = r.URL.Path
		headers[MessageHeaderQuery] = r.URL.RawQuery
	}
	if r.Host != "" {
		headers["Host"] = r.Host
	}
	for key, values := range r.Header {
		if len(values) > 0 {
			headers[key] = values[0]
		}
	}
	return headers
}

func messageMeta(r *http.Request) map[string]interface{} {
	meta := map[string]interface{}{
		MessageMetaMethod: r.Method,
		MessageMetaHost:   r.Host,
	}
	if r.URL != nil {
		meta[MessageMetaPath] = r.URL.Path
		meta[MessageMetaQuery] = r.URL.RawQuery
	}
	return meta
}

type httpMessageSession struct {
	id        uint64
	transport core.Transport
	remote    net.Addr
	w         http.ResponseWriter
	r         *http.Request

	mu        sync.Mutex
	closed    bool
	responded chan struct{}
	once      sync.Once
}

func newMessageSession(w http.ResponseWriter, r *http.Request, tls bool) *httpMessageSession {
	transport := core.TransportHTTP
	if tls {
		transport = core.TransportHTTPS
	}
	return &httpMessageSession{
		id:        sessionIDFromRemote(r.RemoteAddr),
		transport: transport,
		remote:    parseRemoteAddr(r.RemoteAddr),
		w:         w,
		r:         r,
		responded: make(chan struct{}),
	}
}

func (s *httpMessageSession) ID() uint64 {
	return s.id
}

func (s *httpMessageSession) Transport() core.Transport {
	return s.transport
}

func (s *httpMessageSession) RemoteAddr() net.Addr {
	return s.remote
}

func (s *httpMessageSession) Send(msg core.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return core.ErrClosed
	}
	status := responseStatus(msg)
	for key, value := range msg.Headers {
		if key == MessageHeaderStatus || len(key) == 0 || key[0] == ':' {
			continue
		}
		s.w.Header().Set(key, value)
	}
	s.w.WriteHeader(status)
	if len(msg.Data) > 0 {
		if _, err := s.w.Write(msg.Data); err != nil {
			s.closeLocked()
			return err
		}
	}
	s.closeLocked()
	return nil
}

func (s *httpMessageSession) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closeLocked()
	return nil
}

func (s *httpMessageSession) closeLocked() {
	if s.closed {
		return
	}
	s.closed = true
	s.once.Do(func() {
		close(s.responded)
	})
}

func responseStatus(msg core.Message) int {
	status := http.StatusOK
	if msg.ID == core.MsgError {
		status = http.StatusInternalServerError
	}
	if raw := msg.Headers[MessageHeaderStatus]; raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil {
			status = parsed
		}
	}
	if raw, ok := msg.Meta["status"]; ok {
		switch value := raw.(type) {
		case int:
			status = value
		case int32:
			status = int(value)
		case int64:
			status = int(value)
		case uint:
			status = int(value)
		case uint32:
			status = int(value)
		case uint64:
			status = int(value)
		}
	}
	return status
}

func sessionIDFromRemote(remote string) uint64 {
	if remote == "" {
		return 1
	}
	hash := fnv.New64a()
	_, _ = hash.Write([]byte(remote))
	id := hash.Sum64()
	if id == 0 {
		return 1
	}
	return id
}

func parseRemoteAddr(remote string) net.Addr {
	if remote == "" {
		return stringAddr("")
	}
	if addr, err := net.ResolveTCPAddr("tcp", remote); err == nil {
		return addr
	}
	return stringAddr(remote)
}

type stringAddr string

func (a stringAddr) Network() string {
	return "tcp"
}

func (a stringAddr) String() string {
	return string(a)
}
