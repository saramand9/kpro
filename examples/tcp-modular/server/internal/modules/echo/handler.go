// File: examples/tcp-modular/server/internal/modules/echo/handler.go
// Purpose: Provides runnable KPro example code.

package echo

import "github.com/saramand9/bignet/kpro/core"

func echo(ctx core.Context) error {
	msg := ctx.Message()
	return ctx.Reply(msg.ID, msg.Data)
}
