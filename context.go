package kpro

type Handler func(*Context) error

type Middleware func(Handler) Handler

type Context struct {
	Server *Server
	Conn   *Conn
	Msg    Message
}

func (c *Context) Reply(msgID uint32, data []byte) error {
	return c.Conn.Send(Message{ID: msgID, Seq: c.Msg.Seq, Data: data})
}

func (c *Context) Error(text string) error {
	return c.Conn.Send(Message{ID: MsgError, Seq: c.Msg.Seq, Data: []byte(text)})
}
