package echo

import (
	"github.com/saramand9/bignet/kpro/core"
	"github.com/saramand9/bignet/kpro/transport/httpx"
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
