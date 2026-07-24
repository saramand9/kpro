// File: examples/tcp/server/main.go
// Purpose: Provides runnable KPro example code.

package main

import (
	"flag"
	"github.com/saramand9/kpro/examples/internal/msgid"
	"log"

	"github.com/saramand9/kpro/codec"
	"github.com/saramand9/kpro/core"
	"github.com/saramand9/kpro/middleware"
	"github.com/saramand9/kpro/server"
	"github.com/saramand9/kpro/transport/tcp"
	"golang.org/x/time/rate"
)

func main() {
	addr := flag.String("addr", ":8088", "listen address")
	connRate := flag.Float64("conn-rate", 100, "per-connection message rate per second, <=0 disables")
	connBurst := flag.Int("conn-burst", 200, "per-connection message burst")
	flag.Parse()

	app := server.New(server.Config{Name: "tcp-example"})
	endpoint := app.Mount(tcp.New(tcp.Config{Addr: *addr, Codec: codec.KProV1()}))
	endpoint.Use(middleware.Recover())
	if *connRate > 0 {
		endpoint.Use(middleware.ConnRateLimit(middleware.RateLimitConfig{
			Rate:  rate.Limit(*connRate),
			Burst: *connBurst,
		}))
	}
	endpoint.Handle(msgid.Echo, echo)

	log.Printf("tcp example server listening on %s", *addr)
	log.Fatal(app.ListenAndServe())
}

func echo(ctx core.Context) error {
	msg := ctx.Message()
	return ctx.Reply(msg.ID, msg.Data)
}
