// File: examples/udp/server/main.go
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
	"github.com/saramand9/kpro/transport/udp"
)

func main() {
	addr := flag.String("addr", ":8089", "listen address")
	flag.Parse()

	app := server.New(server.Config{Name: "udp-example"})
	app.Mount(udp.New(udp.Config{
		Addr:          *addr,
		Codec:         codec.KProV1(),
		MaxPacketSize: 2048,
	})).
		Use(middleware.Recover()).
		Handle(msgid.Echo, echo)

	log.Printf("udp example server listening on %s", *addr)
	log.Fatal(app.ListenAndServe())
}

func echo(ctx core.Context) error {
	msg := ctx.Message()
	return ctx.Reply(msg.ID, msg.Data)
}
