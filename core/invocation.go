// File: core/invocation.go
// Purpose: Defines shared KPro primitives used by transports, routers, and middleware.

package core

import (
	"context"
	"strconv"
)

const messageOperationPrefix = "message/"

// PacketLimiterKey stores an optional packet-level limiter on native contexts.
const PacketLimiterKey = "kpro.packet_limiter"

// PacketLimiter rejects protocol packets or stream events before they are processed.
type PacketLimiter func() error

// InvocationHandler processes one protocol-level invocation.
type InvocationHandler func(Invocation) error

// Middleware wraps an InvocationHandler for cross-cutting behavior.
type Middleware func(InvocationHandler) InvocationHandler

// Invocation is the protocol-neutral view passed through middleware.
//
// Field examples:
//   - TCP message 100: Transport=TransportTCP, Operation="message/100",
//     Payload=[]byte, Native=core.Context.
//   - HTTP POST /users: Transport=TransportHTTP, Operation="POST /users",
//     Payload=*httpx.BodyPayload, Native=*httpx.Context.
//   - gRPC /pkg.Service/Call: Transport=TransportGRPC, Operation is the full
//     method name, Payload is the protobuf request for unary calls, and Native
//     is kprogrpc.UnaryInvocationNative or StreamInvocationNative.
type Invocation interface {
	// Context returns the cancellation and deadline context for the invocation.
	Context() context.Context
	// Transport identifies the protocol family that produced the invocation.
	Transport() Transport
	// Operation is the stable route key: message id, HTTP method/path, or gRPC method.
	Operation() string
	// ConnID identifies the transport connection, peer, or virtual session.
	ConnID() uint64
	// RemoteAddr is the peer address string when the transport exposes one.
	RemoteAddr() string
	// Headers contains protocol headers copied from the incoming call.
	Headers() map[string]string
	// Meta carries protocol-specific attributes such as path, method, or stream flags.
	Meta() map[string]any
	// Payload is the protocol-specific request body or message payload view.
	Payload() any
	// Native exposes the underlying protocol context for advanced integrations.
	Native() any
	// Set stores a value for downstream middleware or handlers.
	Set(key string, value any)
	// Get retrieves a value stored earlier in the invocation chain.
	Get(key string) (any, bool)
}

// InvocationOptions configures a BaseInvocation.
type InvocationOptions struct {
	// Context is the base context; nil is normalized to context.Background().
	Context context.Context
	// Transport identifies the source protocol family.
	Transport Transport
	// Operation is the stable middleware operation key.
	Operation string
	// ConnID is the connection, peer, or virtual session identifier.
	ConnID uint64
	// Remote is the peer address string stored behind RemoteAddr().
	Remote string
	// Headers are copied before being stored on the invocation.
	Headers map[string]string
	// Meta is copied before being stored on the invocation.
	Meta map[string]any
	// Payload is stored as-is because its type is protocol-specific.
	Payload any
	// Native is the underlying protocol context or request object.
	Native any
	// Done optionally carries the transport/session cancellation signal.
	Done <-chan struct{}
	// ConnDone optionally carries the underlying connection cancellation signal.
	ConnDone <-chan struct{}
}

// BaseInvocation is the default Invocation implementation.
type BaseInvocation struct {
	ctx       context.Context
	transport Transport
	operation string
	connID    uint64
	remote    string
	headers   map[string]string
	meta      map[string]any
	payload   any
	native    any
	done      <-chan struct{}
	connDone  <-chan struct{}
	values    map[string]any
}

// NewInvocation builds an invocation and defensively copies metadata maps.
func NewInvocation(opts InvocationOptions) *BaseInvocation {
	ctx := opts.Context
	if ctx == nil {
		ctx = context.Background()
	}
	return &BaseInvocation{
		ctx:       ctx,
		transport: opts.Transport,
		operation: opts.Operation,
		connID:    opts.ConnID,
		remote:    opts.Remote,
		headers:   cloneStringMap(opts.Headers),
		meta:      cloneAnyMap(opts.Meta),
		payload:   opts.Payload,
		native:    opts.Native,
		done:      opts.Done,
		connDone:  opts.ConnDone,
	}
}

// NewMessageInvocation adapts a message Context into the middleware invocation model.
func NewMessageInvocation(ctx context.Context, msgCtx Context) *BaseInvocation {
	msg := msgCtx.Message()
	var done <-chan struct{}
	if doneSession, ok := msgCtx.Session().(interface{ Done() <-chan struct{} }); ok {
		done = doneSession.Done()
	}
	return NewInvocation(InvocationOptions{
		Context:   ctx,
		Transport: msgCtx.Transport(),
		Operation: MessageOperation(msg.ID),
		ConnID:    msgCtx.ConnID(),
		Remote:    msgCtx.RemoteAddr(),
		Headers:   msg.Headers,
		Meta:      msg.Meta,
		Payload:   msg.Data,
		Native:    msgCtx,
		Done:      done,
	})
}

// MessageOperation returns the stable operation name for a message id.
func MessageOperation(msgID uint32) string {
	return messageOperationPrefix + strconv.FormatUint(uint64(msgID), 10)
}

// Chain composes middleware in registration order.
func Chain(middlewares ...Middleware) Middleware {
	return func(next InvocationHandler) InvocationHandler {
		for i := len(middlewares) - 1; i >= 0; i-- {
			next = middlewares[i](next)
		}
		return next
	}
}

// Context returns the request or stream context associated with the invocation.
func (c *BaseInvocation) Context() context.Context {
	return c.ctx
}

// Transport returns the protocol family that produced the invocation.
func (c *BaseInvocation) Transport() Transport {
	return c.transport
}

// Operation returns the route, RPC method, stream, or message operation name.
func (c *BaseInvocation) Operation() string {
	return c.operation
}

// ConnID returns the transport-level connection or peer identifier.
func (c *BaseInvocation) ConnID() uint64 {
	return c.connID
}

// RemoteAddr returns the peer address string when one is available.
func (c *BaseInvocation) RemoteAddr() string {
	return c.remote
}

// Headers returns protocol headers copied onto the invocation.
func (c *BaseInvocation) Headers() map[string]string {
	return c.headers
}

// Meta returns protocol-specific metadata copied onto the invocation.
func (c *BaseInvocation) Meta() map[string]any {
	return c.meta
}

// Payload returns the protocol-specific request payload view.
func (c *BaseInvocation) Payload() any {
	return c.payload
}

// Native returns the original protocol context or native request object.
func (c *BaseInvocation) Native() any {
	return c.native
}

// Done returns the cancellation channel associated with the invocation.
func (c *BaseInvocation) Done() <-chan struct{} {
	return c.done
}

// ConnDone returns the cancellation channel associated with the underlying connection.
func (c *BaseInvocation) ConnDone() <-chan struct{} {
	return c.connDone
}

// Set stores a middleware-local value, delegating to Native when supported.
func (c *BaseInvocation) Set(key string, value any) {
	if native, ok := c.native.(interface{ Set(string, any) }); ok {
		native.Set(key, value)
		return
	}
	if c.values == nil {
		c.values = make(map[string]any)
	}
	c.values[key] = value
}

// Get retrieves a middleware-local value, delegating to Native when supported.
func (c *BaseInvocation) Get(key string) (any, bool) {
	if native, ok := c.native.(interface{ Get(string) (any, bool) }); ok {
		return native.Get(key)
	}
	value, ok := c.values[key]
	return value, ok
}

func cloneStringMap(src map[string]string) map[string]string {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[string]string, len(src))
	for key, value := range src {
		dst[key] = value
	}
	return dst
}

func cloneAnyMap(src map[string]any) map[string]any {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[string]any, len(src))
	for key, value := range src {
		dst[key] = value
	}
	return dst
}
