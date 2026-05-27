package core

import "sync"

type Handler func(Context) error

type Middleware func(Handler) Handler

type Context interface {
	Session() Session
	Message() Message
	Transport() Transport
	ConnID() uint64
	RemoteAddr() string
	Reply(msgID uint32, data []byte) error
	Error(text string) error
	Set(key string, value interface{})
	Get(key string) (interface{}, bool)
}

type BaseContext struct {
	session Session
	message Message
	values  map[string]interface{}
	pooled  bool
}

var baseContextPool = sync.Pool{
	New: func() interface{} {
		return &BaseContext{}
	},
}

func NewContext(session Session, message Message) *BaseContext {
	return &BaseContext{
		session: session,
		message: message,
	}
}

func AcquireContext(session Session, message Message) *BaseContext {
	ctx := baseContextPool.Get().(*BaseContext)
	ctx.session = session
	ctx.message = message
	ctx.pooled = true
	return ctx
}

func releaseContext(ctx Context) {
	base, ok := ctx.(*BaseContext)
	if !ok || !base.pooled {
		return
	}
	base.reset()
	baseContextPool.Put(base)
}

func (c *BaseContext) reset() {
	c.session = nil
	c.message = Message{}
	if c.values != nil {
		for key := range c.values {
			delete(c.values, key)
		}
	}
	c.pooled = false
}

func (c *BaseContext) Session() Session {
	return c.session
}

func (c *BaseContext) Message() Message {
	return c.message
}

func (c *BaseContext) Transport() Transport {
	return c.session.Transport()
}

func (c *BaseContext) ConnID() uint64 {
	return c.session.ID()
}

func (c *BaseContext) RemoteAddr() string {
	if c.session.RemoteAddr() == nil {
		return ""
	}
	return c.session.RemoteAddr().String()
}

func (c *BaseContext) Reply(msgID uint32, data []byte) error {
	return c.session.Send(Message{
		ID:   msgID,
		Seq:  c.message.Seq,
		Data: data,
	})
}

func (c *BaseContext) Error(text string) error {
	return c.session.Send(Message{
		ID:   MsgError,
		Seq:  c.message.Seq,
		Data: []byte(text),
	})
}

func (c *BaseContext) Set(key string, value interface{}) {
	if c.values == nil {
		c.values = make(map[string]interface{})
	}
	c.values[key] = value
}

func (c *BaseContext) Get(key string) (interface{}, bool) {
	value, ok := c.values[key]
	return value, ok
}
