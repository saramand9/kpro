package core

// OwnedPayloadSender is an optional Session capability used by
// ReplyCurrentPayload. The supplied lease owns one transport-managed Context
// reference. Implementations must call ReleaseTransportContext exactly once,
// either before returning an error or after the queued write no longer uses
// Message.Data.
type OwnedPayloadSender interface {
	SendOwnedPayload(Message, *BaseContext) error
}

// ReplyCurrentPayload replies with the current message payload and sequence.
// When the Context and Session support ownership transfer, the transport may
// queue the inbound payload without copying it. The caller must return from the
// handler without reading or mutating Message.Data after this call. Unsupported
// contexts preserve ordinary Reply copy semantics.
func ReplyCurrentPayload(ctx Context, msgID uint32) error {
	if base, ok := ctx.(*BaseContext); ok {
		if sent, err := base.replyCurrentPayload(msgID); sent {
			return err
		}
	}
	message := ctx.Message()
	return ctx.Reply(msgID, message.Data)
}

func (c *BaseContext) replyCurrentPayload(msgID uint32) (bool, error) {
	sender, ok := c.session.(OwnedPayloadSender)
	if !ok || !c.transportManaged || c.bufferOwner == nil || c.refs.Load() <= 0 {
		return false, nil
	}
	payload := c.message.Data
	buffer := c.transportBuffer
	if len(payload) == 0 || len(payload) != len(buffer) || &payload[0] != &buffer[0] {
		return false, nil
	}

	// This reference is consumed by SendOwnedPayload on every return path. It
	// keeps both the Context and its transport buffer alive until the writer is
	// finished with the borrowed payload.
	c.refs.Add(1)
	return true, sender.SendOwnedPayload(Message{
		ID:   msgID,
		Seq:  c.message.Seq,
		Data: payload,
	}, c)
}
