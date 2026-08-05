package fasthttpx

import (
	"context"

	"github.com/saramand9/kpro/core"
)

// BodyPayload is a borrowed, lazy body view exposed to KPro middleware.
type BodyPayload struct {
	ctx *Context
}

func NewBodyPayload(ctx *Context) *BodyPayload { return &BodyPayload{ctx: ctx} }

func (p *BodyPayload) Bytes() ([]byte, error) {
	if p == nil || p.ctx == nil {
		return nil, nil
	}
	return p.ctx.Body()
}

func (p *BodyPayload) PayloadSize() (int, bool) {
	if p == nil || p.ctx == nil || p.ctx.native == nil {
		return 0, true
	}
	return len(p.ctx.native.PostBody()), true
}

func (p *BodyPayload) LimitPayload(size int) error {
	if p == nil || p.ctx == nil || size < 0 {
		return nil
	}
	payloadSize, _ := p.PayloadSize()
	if payloadSize > size {
		return &BodyTooLargeError{Size: int64(payloadSize), Limit: int64(size)}
	}
	return nil
}

func newHTTPInvocation(ctx *Context, transport core.Transport, operation string) core.Invocation {
	return &httpInvocation{
		ctx:       ctx,
		transport: transport,
		operation: operation,
		connID:    ctx.native.ConnID(),
	}
}

type httpInvocation struct {
	ctx       *Context
	transport core.Transport
	operation string
	connID    uint64

	remoteReady   bool
	remote        string
	headersReady  bool
	headers       map[string]string
	metaReady     bool
	meta          map[string]any
	payloadReady  bool
	payload       BodyPayload
	connDoneReady bool
	connDone      <-chan struct{}
}

var _ core.Invocation = (*httpInvocation)(nil)

func (i *httpInvocation) Context() context.Context  { return i.ctx.Context() }
func (i *httpInvocation) Transport() core.Transport { return i.transport }
func (i *httpInvocation) Operation() string         { return i.operation }
func (i *httpInvocation) ConnID() uint64            { return i.connID }

func (i *httpInvocation) RemoteAddr() string {
	if !i.remoteReady {
		if address := i.ctx.native.RemoteAddr(); address != nil {
			i.remote = address.String()
		}
		i.remoteReady = true
	}
	return i.remote
}

func (i *httpInvocation) Headers() map[string]string {
	if i.headersReady {
		return i.headers
	}
	host := i.ctx.native.Host()
	count := i.ctx.native.Request.Header.Len()
	if len(host) > 0 || count > 0 {
		i.headers = make(map[string]string, count+1)
	}
	if len(host) > 0 {
		i.headers["Host"] = string(host)
	}
	i.ctx.native.Request.Header.VisitAll(func(key, value []byte) {
		name := string(key)
		if _, exists := i.headers[name]; !exists {
			i.headers[name] = string(value)
		}
	})
	i.headersReady = true
	return i.headers
}

func (i *httpInvocation) Meta() map[string]any {
	if !i.metaReady {
		i.meta = map[string]any{
			"method": string(i.ctx.native.Method()),
			"host":   string(i.ctx.native.Host()),
			"path":   string(i.ctx.native.Path()),
			"query":  string(i.ctx.native.URI().QueryString()),
		}
		i.metaReady = true
	}
	return i.meta
}

func (i *httpInvocation) Payload() any {
	if !i.payloadReady {
		i.payload.ctx = i.ctx
		i.payloadReady = true
	}
	return &i.payload
}

func (i *httpInvocation) Native() any           { return i.ctx }
func (i *httpInvocation) Done() <-chan struct{} { return i.ctx.Context().Done() }

func (i *httpInvocation) ConnDone() <-chan struct{} {
	if !i.connDoneReady {
		i.connDone = i.ctx.endpoint.connectionDone(i.ctx.native.Conn())
		i.connDoneReady = true
	}
	return i.connDone
}

func (i *httpInvocation) Set(key string, value any)  { i.ctx.Set(key, value) }
func (i *httpInvocation) Get(key string) (any, bool) { return i.ctx.Get(key) }
