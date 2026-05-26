package echo

import "github.com/saramand9/bignet/kpro/transport/httpx"

func echo(ctx *httpx.Context) error {
	body, err := ctx.Body()
	if err != nil {
		return err
	}
	return ctx.JSON(200, map[string]string{"echo": string(body)})
}
