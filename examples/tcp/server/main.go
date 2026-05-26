package main

import (
	"flag"
	"log"

	"github.com/saramand9/bignet/kpro/core"
	"github.com/saramand9/bignet/kpro/middleware"
	"github.com/saramand9/bignet/kpro/protocol"
	"github.com/saramand9/bignet/kpro/server"
	"github.com/saramand9/bignet/kpro/transport/tcp"
)

func main() {
	addr := flag.String("addr", ":8088", "listen address")
	flag.Parse()

	app := server.New(server.Config{Name: "tcp-example"})
	app.TCP(*addr, tcp.Config{Protocol: protocol.KProV1()}).
		Use(middleware.Recover()).
		Handle(core.MsgEcho, echo)

	log.Printf("tcp example server listening on %s", *addr)
	log.Fatal(app.ListenAndServe())
}

func echo(ctx core.Context) error {
	msg := ctx.Message()
	return ctx.Reply(msg.ID, msg.Data)
}
