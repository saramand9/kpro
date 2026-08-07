// File: core/outbound_completion.go
// Purpose: Defines optional acknowledgement and drain capabilities for session writes.

package core

import (
	"context"
	"sync"
)

// WriteReceipt reports completion of one transport write accepted by
// SendWithReceipt. A nil result means the transport completed its full write
// call successfully. It does not acknowledge peer receipt, application
// handling, or durable delivery.
//
// The zero value is ready for use. Complete is exported for third-party
// transport implementations; only the first call records a result.
type WriteReceipt struct {
	initOnce     sync.Once
	completeOnce sync.Once
	mu           sync.Mutex
	done         chan struct{}
	err          error
}

// NewWriteReceipt creates an incomplete write receipt.
func NewWriteReceipt() *WriteReceipt {
	receipt := &WriteReceipt{}
	receipt.initialize()
	return receipt
}

// Done is closed when the transport write completes or is discarded.
// Done returns nil for a nil receipt.
func (r *WriteReceipt) Done() <-chan struct{} {
	if r == nil {
		return nil
	}
	r.initialize()
	return r.done
}

// Wait waits for transport write completion. A nil context means
// context.Background. When completion races context cancellation, an already
// recorded write result takes precedence.
func (r *WriteReceipt) Wait(ctx context.Context) error {
	if r == nil {
		return ErrInvalidWriteReceipt
	}
	if ctx == nil {
		ctx = context.Background()
	}
	done := r.Done()
	select {
	case <-done:
		err, _ := r.Result()
		return err
	default:
	}
	select {
	case <-done:
		err, _ := r.Result()
		return err
	case <-ctx.Done():
		select {
		case <-done:
			err, _ := r.Result()
			return err
		default:
			return ctx.Err()
		}
	}
}

// Result returns the recorded transport result and whether the write has
// completed. It is safe to call before completion.
func (r *WriteReceipt) Result() (err error, completed bool) {
	if r == nil {
		return nil, false
	}
	r.initialize()
	select {
	case <-r.done:
		r.mu.Lock()
		err = r.err
		r.mu.Unlock()
		return err, true
	default:
		return nil, false
	}
}

// Complete records the transport result and releases all waiters. It returns
// true only for the call that completed the receipt.
func (r *WriteReceipt) Complete(err error) bool {
	if r == nil {
		return false
	}
	r.initialize()
	completed := false
	r.completeOnce.Do(func() {
		r.mu.Lock()
		r.err = err
		r.mu.Unlock()
		close(r.done)
		completed = true
	})
	return completed
}

func (r *WriteReceipt) initialize() {
	r.initOnce.Do(func() {
		r.done = make(chan struct{})
	})
}

// WriteReceiptSender is the optional capability implemented by sessions that
// can report the result of the transport write after queue admission. An
// immediate send failure must return a nil receipt with that error.
type WriteReceiptSender interface {
	SendWithReceipt(Message) (*WriteReceipt, error)
}

// SessionOutboundDrainer is the optional capability implemented by sessions
// that can reject new sends and wait for all accepted transport writes.
type SessionOutboundDrainer interface {
	DrainOutbound(context.Context) error
}

// SessionSendWithReceipt sends through the optional write-receipt capability.
// Unsupported sessions are left untouched and return supported=false.
func SessionSendWithReceipt(session Session, message Message) (receipt *WriteReceipt, supported bool, err error) {
	if session == nil {
		return nil, false, nil
	}
	sender, ok := session.(WriteReceiptSender)
	if !ok {
		return nil, false, nil
	}
	receipt, err = sender.SendWithReceipt(message)
	return receipt, true, err
}

// SessionDrainOutbound drains through the optional session capability.
// Unsupported sessions are left untouched and return supported=false.
func SessionDrainOutbound(ctx context.Context, session Session) (supported bool, err error) {
	if session == nil {
		return false, nil
	}
	drainer, ok := session.(SessionOutboundDrainer)
	if !ok {
		return false, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return true, drainer.DrainOutbound(ctx)
}
