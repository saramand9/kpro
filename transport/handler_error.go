package transport

import (
	"errors"
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

// InvokeHandler invokes a direct message-transport handler while containing
// panics at the handler boundary. Recovered panics retain core.ErrRecovered and
// an error panic's original errors.Is/As chain so ContinueAfterHandlerError can
// apply the configured policy and notify the observer.
func InvokeHandler(handler core.Handler, session core.Session, message core.Message) (err error) {
	if handler == nil {
		return core.ErrNilHandler
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			if recoveredErr, ok := recovered.(error); ok {
				err = fmt.Errorf("%w: %w", core.ErrRecovered, recoveredErr)
				return
			}
			err = fmt.Errorf("%w: %v", core.ErrRecovered, recovered)
		}
	}()
	ctx := core.AcquireTransportContext(session, message)
	defer core.ReleaseTransportContext(ctx)
	return handler(ctx)
}

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
// result. It returns true for success, a Router dispatch rejection that already
// produced a safe response, or an explicit continue policy. Recovered panics
// always require the explicit continue policy.
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
	if errors.Is(err, core.ErrRecovered) {
		return policy == HandlerErrorContinue
	}
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
