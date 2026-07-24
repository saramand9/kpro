// File: core/context.go
// Purpose: Defines shared KPro primitives used by transports, routers, and middleware.

package core

import (
	"context"
	"sync"
	"sync/atomic"
)

// Handler processes one decoded KPro message context.
type Handler func(Context) error

// Context carries the current session, message, and handler-local values.
// It is not safe for concurrent mutation. If a handler starts background work,
// call CloneContext first when the goroutine must retain an isolated copy of
// mutable message data or context values.
type Context interface {
	// Session returns the live transport session used for replies and errors.
	Session() Session
	// Message returns the decoded business message for the current dispatch.
	Message() Message
	// Transport returns the protocol family of the underlying session.
	Transport() Transport
	// ConnID returns the stable identifier of the underlying session.
	ConnID() uint64
	// RemoteAddr returns the peer address string, or an empty string if unavailable.
	RemoteAddr() string
	// Reply sends a response with the current message sequence number.
	Reply(msgID uint32, data []byte) error
	// Error sends a MsgError response with the current message sequence number.
	Error(text string) error
	// Set stores a handler-local value on the context.
	Set(key string, value interface{})
	// Get retrieves a handler-local value from the context.
	Get(key string) (interface{}, bool)
}

// ContextProvider is the optional request-context capability implemented by
// BaseContext and middleware-decorated message contexts. Keeping it separate
// preserves compatibility with third-party Context implementations.
type ContextProvider interface {
	Context() context.Context
}

// ContextOf returns the middleware-updated request context when available and
// falls back to context.Background for legacy Context implementations.
func ContextOf(messageCtx Context) context.Context {
	if provider, ok := messageCtx.(ContextProvider); ok {
		return normalizeContext(provider.Context())
	}
	return context.Background()
}

// BaseContext is the default Context implementation used by message transports.
type BaseContext struct {
	ctx              context.Context
	session          Session
	message          Message
	values           map[string]interface{}
	refs             atomic.Int32
	transportManaged bool
}

var baseContextPool = sync.Pool{
	New: func() any {
		return new(BaseContext)
	},
}

// NewContext creates a message context for the supplied session and message.
func NewContext(session Session, message Message) *BaseContext {
	return &BaseContext{
		ctx:     context.Background(),
		session: session,
		message: message,
	}
}

// AcquireContext creates a Router-owned pooled context. Existing custom
// transports may keep using it when their handler is always Router.Dispatch.
// New transports that also support direct handlers should use
// AcquireTransportContext and ReleaseTransportContext.
func AcquireContext(session Session, message Message) *BaseContext {
	return acquireContext(session, message, false)
}

// AcquireTransportContext creates a pooled context whose caller lifetime can
// overlap a Router task. Message transports must pair it with
// ReleaseTransportContext after the supplied handler returns.
func AcquireTransportContext(session Session, message Message) *BaseContext {
	return acquireContext(session, message, true)
}

func acquireContext(session Session, message Message, transportManaged bool) *BaseContext {
	ctx := baseContextPool.Get().(*BaseContext)
	ctx.ctx = context.Background()
	ctx.session = session
	ctx.message = message
	ctx.values = nil
	ctx.transportManaged = transportManaged
	ctx.refs.Store(1)
	return ctx
}

// ReleaseTransportContext releases the transport caller's ownership. If a
// Router accepted the context, pooling is delayed until that task completes.
func ReleaseTransportContext(ctx Context) {
	base, ok := ctx.(*BaseContext)
	if !ok || !base.transportManaged {
		return
	}
	releaseContext(base)
}

// CloneContext returns a context snapshot. Message data and map containers are
// copied; values inside Meta or context values are copied by assignment. Session
// is shared because it represents the live connection used for Reply and Error.
func CloneContext(ctx Context) *BaseContext {
	if base, ok := ctx.(*BaseContext); ok {
		return base.Clone()
	}
	cloned := NewContext(ctx.Session(), cloneMessage(ctx.Message()))
	cloned.ctx = ContextOf(ctx)
	return cloned
}

// Clone returns a snapshot of the current context.
func (c *BaseContext) Clone() *BaseContext {
	cloned := NewContext(c.session, cloneMessage(c.message))
	cloned.ctx = normalizeContext(c.ctx)
	if len(c.values) > 0 {
		cloned.values = make(map[string]interface{}, len(c.values))
		for key, value := range c.values {
			cloned.values[key] = value
		}
	}
	return cloned
}

func releaseContext(ctx Context) {
	base, ok := ctx.(*BaseContext)
	if !ok || base.refs.Load() <= 0 {
		return
	}
	if base.refs.Add(-1) != 0 {
		return
	}
	base.ctx = nil
	base.session = nil
	base.message = Message{}
	base.values = nil
	base.transportManaged = false
	baseContextPool.Put(base)
}

func retainRouterContext(ctx Context) {
	base, ok := ctx.(*BaseContext)
	if !ok || !base.transportManaged || base.refs.Load() <= 0 {
		return
	}
	base.refs.Add(1)
}

func normalizeContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

// Context returns the middleware-updated context for the current dispatch.
func (c *BaseContext) Context() context.Context {
	return normalizeContext(c.ctx)
}

func (c *BaseContext) setContext(ctx context.Context) {
	c.ctx = normalizeContext(ctx)
}

type messageContextDelegate interface {
	Context
}

type contextualMessageContext struct {
	messageContextDelegate
	ctx context.Context
}

func (c *contextualMessageContext) Context() context.Context {
	return c.ctx
}

func withMessageContext(messageCtx Context, ctx context.Context) Context {
	ctx = normalizeContext(ctx)
	if base, ok := messageCtx.(*BaseContext); ok {
		base.setContext(ctx)
		return base
	}
	return &contextualMessageContext{
		messageContextDelegate: messageCtx,
		ctx:                    ctx,
	}
}

func cloneMessage(msg Message) Message {
	cloned := msg
	if len(msg.Data) > 0 {
		cloned.Data = append([]byte(nil), msg.Data...)
	}
	if len(msg.Headers) > 0 {
		cloned.Headers = make(map[string]string, len(msg.Headers))
		for key, value := range msg.Headers {
			cloned.Headers[key] = value
		}
	}
	if len(msg.Meta) > 0 {
		cloned.Meta = make(map[string]interface{}, len(msg.Meta))
		for key, value := range msg.Meta {
			cloned.Meta[key] = value
		}
	}
	return cloned
}

// Session returns the transport session that delivered the message.
func (c *BaseContext) Session() Session {
	return c.session
}

// Message returns the decoded business message.
func (c *BaseContext) Message() Message {
	return c.message
}

// Transport returns the protocol family for the underlying session.
func (c *BaseContext) Transport() Transport {
	return c.session.Transport()
}

// ConnID returns the stable session identifier for this context.
func (c *BaseContext) ConnID() uint64 {
	return c.session.ID()
}

// RemoteAddr returns the peer address string when one is available.
func (c *BaseContext) RemoteAddr() string {
	if c.session.RemoteAddr() == nil {
		return ""
	}
	return c.session.RemoteAddr().String()
}

// Reply sends a response with the current message sequence number.
func (c *BaseContext) Reply(msgID uint32, data []byte) error {
	return c.session.Send(Message{
		ID:   msgID,
		Seq:  c.message.Seq,
		Data: data,
	})
}

// Error sends a MsgError response with the current message sequence number.
func (c *BaseContext) Error(text string) error {
	return c.session.Send(Message{
		ID:   MsgError,
		Seq:  c.message.Seq,
		Data: []byte(text),
	})
}

// Set stores a handler-local value on the context.
func (c *BaseContext) Set(key string, value interface{}) {
	if c.values == nil {
		c.values = make(map[string]interface{})
	}
	c.values[key] = value
}

// Get retrieves a handler-local value from the context.
func (c *BaseContext) Get(key string) (interface{}, bool) {
	value, ok := c.values[key]
	return value, ok
}
