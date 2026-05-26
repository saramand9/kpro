package echo

import "github.com/saramand9/bignet/kpro/core"

func echo(ctx core.Context) error {
	msg := ctx.Message()
	return ctx.Reply(msg.ID, msg.Data)
}
