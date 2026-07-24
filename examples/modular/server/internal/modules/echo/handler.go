// File: examples/modular/server/internal/modules/echo/handler.go
// Purpose: Provides runnable KPro example code.

package echo

import (
	"github.com/saramand9/kpro/core"
	"github.com/saramand9/kpro/transport/httpx"
)

func httpEcho(ctx *httpx.Context) error {
	body, err := ctx.Body()
	if err != nil {
		return err
	}
	return ctx.JSON(200, map[string]string{"echo": string(body)})
}

func messageEcho(ctx core.Context) error {
	msg := ctx.Message()
	return ctx.Reply(msg.ID, msg.Data)
}
