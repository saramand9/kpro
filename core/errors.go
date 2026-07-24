// File: core/errors.go
// Purpose: Defines shared KPro primitives used by transports, routers, and middleware.

package core

import (
	"context"
	"errors"
)

// ErrorKind is a stable, protocol-neutral classification for errors that need
// to be translated at transport boundaries. Error text remains diagnostic and
// must not be used as a wire-level contract.
type ErrorKind string

const (
	ErrorKindRateLimited     ErrorKind = "rate_limited"
	ErrorKindPayloadTooLarge ErrorKind = "payload_too_large"
	ErrorKindOverloaded      ErrorKind = "overloaded"
	ErrorKindInternal        ErrorKind = "internal"
	ErrorKindRecovered       ErrorKind = "recovered"
)

type classifiedError struct {
	kind    ErrorKind
	message string
	cause   error
}

func (e *classifiedError) Error() string { return e.message }

func (e *classifiedError) Unwrap() error { return e.cause }

func (e *classifiedError) ErrorKind() ErrorKind { return e.kind }

// ErrorKindOf returns the first protocol-neutral classification in err's
// unwrap chain. An empty kind means the error has not been classified.
func ErrorKindOf(err error) ErrorKind {
	var classified interface{ ErrorKind() ErrorKind }
	if errors.As(err, &classified) {
		return classified.ErrorKind()
	}
	return ""
}

// PublicErrorMessage returns a stable, non-sensitive message suitable for
// protocol responses. Unclassified errors are treated as internal failures.
func PublicErrorMessage(err error) string {
	switch ErrorKindOf(err) {
	case ErrorKindRateLimited:
		return ErrRateLimited.Error()
	case ErrorKindPayloadTooLarge:
		return ErrPayloadTooLarge.Error()
	case ErrorKindOverloaded:
		return ErrOverloaded.Error()
	case ErrorKindInternal, ErrorKindRecovered:
		return ErrInternal.Error()
	default:
		return ErrInternal.Error()
	}
}

// WithErrorKind classifies err while retaining it for errors.Is/errors.As and
// internal diagnostics. It returns nil when err is nil.
func WithErrorKind(kind ErrorKind, err error) error {
	if err == nil {
		return nil
	}
	sentinel := errorKindSentinel(kind)
	if ErrorKindOf(err) == kind && (sentinel == nil || errors.Is(err, sentinel)) {
		return err
	}
	cause := err
	if sentinel != nil && !errors.Is(err, sentinel) {
		cause = errors.Join(sentinel, err)
	}
	return &classifiedError{
		kind:    kind,
		message: err.Error(),
		cause:   cause,
	}
}

func errorKindSentinel(kind ErrorKind) error {
	switch kind {
	case ErrorKindRateLimited:
		return ErrRateLimited
	case ErrorKindPayloadTooLarge:
		return ErrPayloadTooLarge
	case ErrorKindOverloaded:
		return ErrOverloaded
	case ErrorKindInternal:
		return ErrInternal
	case ErrorKindRecovered:
		return ErrRecovered
	default:
		return nil
	}
}

// IsDispatchRejection reports Router admission and lookup failures that have
// already been translated to MsgError by Router.Dispatch. Message transports
// must keep the session open long enough to flush that response instead of
// applying direct-handler failure policy to the same error a second time.
func IsDispatchRejection(err error) bool {
	return errors.Is(err, ErrNoHandler) ||
		errors.Is(err, ErrRouterClosed) ||
		errors.Is(err, ErrDispatchQueueFull) ||
		errors.Is(err, ErrTooManyInflight) ||
		errors.Is(err, ErrGlobalInflightLimit) ||
		errors.Is(err, ErrClosed) ||
		errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded)
}

var (
	// ErrRateLimited identifies invocation rejection by a rate policy.
	ErrRateLimited = &classifiedError{kind: ErrorKindRateLimited, message: "rate limit exceeded"}
	// ErrPayloadTooLarge identifies payload rejection by a configured limit.
	ErrPayloadTooLarge = &classifiedError{kind: ErrorKindPayloadTooLarge, message: "payload too large"}
	// ErrOverloaded identifies bounded-capacity admission rejection.
	ErrOverloaded = &classifiedError{kind: ErrorKindOverloaded, message: "service overloaded"}
	// ErrInternal identifies an internal failure whose detail must not be sent to clients.
	ErrInternal = &classifiedError{kind: ErrorKindInternal, message: "internal error"}
	// ErrRecovered identifies a panic converted into an error at a recovery boundary.
	ErrRecovered = &classifiedError{kind: ErrorKindRecovered, message: "handler panic recovered", cause: ErrInternal}

	// ErrAlreadyStarted reports that a one-shot component was started more than once.
	ErrAlreadyStarted = errors.New("component already started")
	// ErrClosed reports that a session or transport is already closed.
	ErrClosed = errors.New("component closed")
	// ErrNoHandler reports that no route was registered for a message id.
	ErrNoHandler = errors.New("no handler registered")
	// ErrNilHandler reports an attempt to register a nil message handler.
	ErrNilHandler = errors.New("message handler is nil")
	// ErrHandlerAlreadyRegistered reports duplicate registration for a message id.
	ErrHandlerAlreadyRegistered = errors.New("message handler already registered")
	// ErrRouterClosed reports that dispatch was attempted after router shutdown.
	ErrRouterClosed = errors.New("router closed")
	// ErrDispatchQueueFull reports that a sync or async dispatch queue is full.
	ErrDispatchQueueFull = &classifiedError{kind: ErrorKindOverloaded, message: "dispatch queue full", cause: ErrOverloaded}
	// ErrSendQueueFull reports that a session send queue cannot accept more messages.
	ErrSendQueueFull = &classifiedError{kind: ErrorKindOverloaded, message: "send queue full", cause: ErrOverloaded}
	// ErrTooManyInflight reports that a session reached its in-flight limit.
	ErrTooManyInflight = &classifiedError{kind: ErrorKindOverloaded, message: "too many in-flight messages", cause: ErrOverloaded}
	// ErrGlobalInflightLimit reports that the Router-wide in-flight limit was reached.
	ErrGlobalInflightLimit = &classifiedError{kind: ErrorKindOverloaded, message: "global in-flight limit reached", cause: ErrOverloaded}
)
