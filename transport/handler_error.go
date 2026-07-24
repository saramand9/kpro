package transport

import (
	"fmt"

	"github.com/saramand9/kpro/core"
)

// HandlerErrorPolicy controls whether a message session remains open after its
// handler returns an error. The zero value closes the affected session.
type HandlerErrorPolicy uint8

const (
	// HandlerErrorCloseSession closes only the session whose handler failed.
	HandlerErrorCloseSession HandlerErrorPolicy = iota
	// HandlerErrorContinue keeps the session open after notifying the observer.
	HandlerErrorContinue
)

// HandlerErrorEvent is a stable snapshot delivered when a message handler
// fails. Err is the original handler error and retains its errors.Is/As chain.
type HandlerErrorEvent struct {
	Transport  core.Transport
	ConnID     uint64
	RemoteAddr string
	MessageID  uint32
	Sequence   uint64
	Err        error
}

// HandlerErrorObserver observes message handler failures. Observer panics are
// isolated from transport read loops. Implementations must not rely on a panic
// to change the configured session policy.
type HandlerErrorObserver func(HandlerErrorEvent)

// Validate rejects unknown policy values before a listener is opened.
func (p HandlerErrorPolicy) Validate() error {
	switch p {
	case HandlerErrorCloseSession, HandlerErrorContinue:
		return nil
	default:
		return fmt.Errorf("unknown handler error policy %d", p)
	}
}

// ContinueAfterHandlerError applies the observer and policy to one handler
// result. It returns true for a successful handler or when the policy explicitly
// keeps the session open.
func ContinueAfterHandlerError(
	policy HandlerErrorPolicy,
	observer HandlerErrorObserver,
	session core.Session,
	message core.Message,
	err error,
) bool {
	if err == nil {
		return true
	}
	event := HandlerErrorEvent{
		MessageID: message.ID,
		Sequence:  message.Seq,
		Err:       err,
	}
	if session != nil {
		event.Transport = session.Transport()
		event.ConnID = session.ID()
		if remote := session.RemoteAddr(); remote != nil {
			event.RemoteAddr = remote.String()
		}
	}
	notifyHandlerError(observer, event)
	if core.IsDispatchRejection(err) {
		return true
	}
	return policy == HandlerErrorContinue
}

func notifyHandlerError(observer HandlerErrorObserver, event HandlerErrorEvent) {
	if observer == nil {
		return
	}
	defer func() {
		_ = recover()
	}()
	observer(event)
}
