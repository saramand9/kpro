package core

import (
	"context"
	"strconv"
)

const messageOperationPrefix = "message/"

type InvocationHandler func(Invocation) error

type Middleware func(InvocationHandler) InvocationHandler

type Invocation interface {
	Context() context.Context
	Transport() Transport
	Operation() string
	ConnID() uint64
	RemoteAddr() string
	Headers() map[string]string
	Meta() map[string]any
	Payload() any
	Native() any
	Set(key string, value any)
	Get(key string) (any, bool)
}

type InvocationOptions struct {
	Context   context.Context
	Transport Transport
	Operation string
	ConnID    uint64
	Remote    string
	Headers   map[string]string
	Meta      map[string]any
	Payload   any
	Native    any
	Done      <-chan struct{}
}

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
	values    map[string]any
}

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
	}
}

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

func MessageOperation(msgID uint32) string {
	return messageOperationPrefix + strconv.FormatUint(uint64(msgID), 10)
}

func Chain(middlewares ...Middleware) Middleware {
	return func(next InvocationHandler) InvocationHandler {
		for i := len(middlewares) - 1; i >= 0; i-- {
			next = middlewares[i](next)
		}
		return next
	}
}

func (c *BaseInvocation) Context() context.Context {
	return c.ctx
}

func (c *BaseInvocation) Transport() Transport {
	return c.transport
}

func (c *BaseInvocation) Operation() string {
	return c.operation
}

func (c *BaseInvocation) ConnID() uint64 {
	return c.connID
}

func (c *BaseInvocation) RemoteAddr() string {
	return c.remote
}

func (c *BaseInvocation) Headers() map[string]string {
	return c.headers
}

func (c *BaseInvocation) Meta() map[string]any {
	return c.meta
}

func (c *BaseInvocation) Payload() any {
	return c.payload
}

func (c *BaseInvocation) Native() any {
	return c.native
}

func (c *BaseInvocation) Done() <-chan struct{} {
	return c.done
}

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
