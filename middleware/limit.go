package middleware

import (
	"fmt"

	"github.com/saramand9/bignet/kpro/core"
)

func MaxPayload(size int) core.Middleware {
	return func(next core.InvocationHandler) core.InvocationHandler {
		return func(ctx core.Invocation) error {
			if payloadSize, ok := payloadSize(ctx.Payload()); size >= 0 && ok && payloadSize > size {
				return fmt.Errorf("payload too large: %d > %d", payloadSize, size)
			}
			return next(ctx)
		}
	}
}

func payloadSize(payload any) (int, bool) {
	switch value := payload.(type) {
	case nil:
		return 0, true
	case []byte:
		return len(value), true
	case string:
		return len(value), true
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
