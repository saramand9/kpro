package middleware

import (
	"fmt"

	"github.com/saramand9/bignet/kpro/core"
)

func MaxPayload(size int) core.Middleware {
	return func(next core.Handler) core.Handler {
		return func(ctx core.Context) error {
			if size >= 0 && len(ctx.Message().Data) > size {
				return fmt.Errorf("payload too large: %d > %d", len(ctx.Message().Data), size)
			}
			return next(ctx)
		}
	}
}
