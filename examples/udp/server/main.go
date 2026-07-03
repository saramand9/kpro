// File: examples/udp/server/main.go
// Purpose: Provides runnable KPro example code.

package main

import (
	"flag"
	"github.com/saramand9/bignet/kpro/examples/internal/msgid"
	"log"

	"github.com/saramand9/bignet/kpro/codec"
	"github.com/saramand9/bignet/kpro/core"
	"github.com/saramand9/bignet/kpro/middleware"
	"github.com/saramand9/bignet/kpro/server"
	"github.com/saramand9/bignet/kpro/transport/udp"
)

func main() {
	addr := flag.String("addr", ":8089", "listen address")
	flag.Parse()

	app := server.New(server.Config{Name: "udp-example"})
	app.UDP(*addr, udp.Config{
		Codec:         codec.KProV1(),
		MaxPacketSize: 2048,
	}).
		Use(middleware.Recover()).
		Handle(msgid.Echo, echo)

	log.Printf("udp example server listening on %s", *addr)
	log.Fatal(app.ListenAndServe())
}

func echo(ctx core.Context) error {
	msg := ctx.Message()
	return ctx.Reply(msg.ID, msg.Data)
}
