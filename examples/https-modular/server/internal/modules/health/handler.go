// File: examples/https-modular/server/internal/modules/health/handler.go
// Purpose: Provides runnable KPro example code.

package health

import "github.com/saramand9/bignet/kpro/transport/httpx"

func health(ctx *httpx.Context) error {
	return ctx.Text(200, "ok\n")
}
