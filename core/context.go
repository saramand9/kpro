// File: core/context.go
// Purpose: Defines shared KPro primitives used by transports, routers, and middleware.

package core

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

// BaseContext is the default Context implementation used by message transports.
type BaseContext struct {
	session Session
	message Message
	values  map[string]interface{}
}

// NewContext creates a message context for the supplied session and message.
func NewContext(session Session, message Message) *BaseContext {
	return &BaseContext{
		session: session,
		message: message,
	}
}

// AcquireContext creates a context for transport dispatch.
// It currently does not pool or reuse context objects.
func AcquireContext(session Session, message Message) *BaseContext {
	return NewContext(session, message)
}

// CloneContext returns a context snapshot. Message data and map containers are
// copied; values inside Meta or context values are copied by assignment. Session
// is shared because it represents the live connection used for Reply and Error.
func CloneContext(ctx Context) *BaseContext {
	if base, ok := ctx.(*BaseContext); ok {
		return base.Clone()
	}
	return NewContext(ctx.Session(), cloneMessage(ctx.Message()))
}

// Clone returns a snapshot of the current context.
func (c *BaseContext) Clone() *BaseContext {
	cloned := NewContext(c.session, cloneMessage(c.message))
	if len(c.values) > 0 {
		cloned.values = make(map[string]interface{}, len(c.values))
		for key, value := range c.values {
			cloned.values[key] = value
		}
	}
	return cloned
}

func releaseContext(Context) {}

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
