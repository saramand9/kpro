// File: middleware/recover.go
// Purpose: Defines or tests reusable KPro invocation middleware.

package middleware

import (
	"fmt"
	"log"
	"runtime/debug"

	"github.com/saramand9/kpro/core"
)

// Recover converts handler panics into returned errors.
var logPanicStack = func(recovered interface{}, stack []byte) {
	log.Printf("handler panic: %v\n%s", recovered, stack)
}

func Recover() core.Middleware {
	return func(next core.InvocationHandler) core.InvocationHandler {
		return func(ctx core.Invocation) (err error) {
			defer func() {
				if r := recover(); r != nil {
					logPanicStack(r, debug.Stack())
					err = core.WithErrorKind(core.ErrorKindRecovered, fmt.Errorf("handler panic: %v", r))
				}
			}()
			return next(ctx)
		}
	}
}
