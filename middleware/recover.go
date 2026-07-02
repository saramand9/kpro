// File: middleware/recover.go
// Purpose: Defines or tests reusable KPro invocation middleware.

package middleware

import (
	"fmt"

	"github.com/saramand9/bignet/kpro/core"
)

// Recover converts handler panics into returned errors.
func Recover() core.Middleware {
	return func(next core.InvocationHandler) core.InvocationHandler {
		return func(ctx core.Invocation) (err error) {
			defer func() {
				if r := recover(); r != nil {
					err = fmt.Errorf("handler panic: %v", r)
				}
			}()
			return next(ctx)
		}
	}
}
