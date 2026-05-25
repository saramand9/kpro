package core

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
}

func NewContext(session Session, message Message) *BaseContext {
	return &BaseContext{
		session: session,
		message: message,
		values:  make(map[string]interface{}),
	}
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
	c.values[key] = value
}

func (c *BaseContext) Get(key string) (interface{}, bool) {
	value, ok := c.values[key]
	return value, ok
}
