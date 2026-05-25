package middleware

import (
	"fmt"

	"github.com/saramand9/bignet/kpro/core"
)

func Recover() core.Middleware {
	return func(next core.Handler) core.Handler {
		return func(ctx core.Context) (err error) {
			defer func() {
				if r := recover(); r != nil {
					err = fmt.Errorf("handler panic: %v", r)
					_ = ctx.Error("handler panic")
				}
			}()
			return next(ctx)
		}
	}
}
