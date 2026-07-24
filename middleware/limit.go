// File: middleware/limit.go
// Purpose: Defines or tests reusable KPro invocation middleware.

package middleware

import (
	"fmt"

	"github.com/saramand9/kpro/core"
)

// MaxPayload rejects invocations whose payload size exceeds size.
func MaxPayload(size int) core.Middleware {
	return func(next core.InvocationHandler) core.InvocationHandler {
		return func(ctx core.Invocation) error {
			payload := ctx.Payload()
			if size >= 0 {
				if limiter, ok := payload.(payloadLimiter); ok {
					if err := limiter.LimitPayload(size); err != nil {
						return core.WithErrorKind(core.ErrorKindPayloadTooLarge, err)
					}
				}
				if payloadSize, ok := payloadSize(payload); ok && payloadSize > size {
					return fmt.Errorf("%w: %d > %d", core.ErrPayloadTooLarge, payloadSize, size)
				}
			}
			return next(ctx)
		}
	}
}

type payloadLimiter interface {
	LimitPayload(size int) error
}

type payloadSizer interface {
	PayloadSize() (int, bool)
}

func payloadSize(payload any) (int, bool) {
	switch value := payload.(type) {
	case nil:
		return 0, true
	case []byte:
		return len(value), true
	case string:
		return len(value), true
	case payloadSizer:
		return value.PayloadSize()
	case core.Message:
		return len(value.Data), true
	case *core.Message:
		if value == nil {
			return 0, true
		}
		return len(value.Data), true
	default:
		return 0, false
	}
}
