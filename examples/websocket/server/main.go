// File: examples/websocket/server/main.go
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
	"github.com/saramand9/bignet/kpro/transport/websocket"
)

func main() {
	addr := flag.String("addr", ":8091", "listen address")
	path := flag.String("path", "/ws", "websocket path")
	flag.Parse()

	app := server.New(server.Config{Name: "websocket-example"})
	app.WebSocket(*addr, *path, websocket.Config{Codec: codec.KProV1()}).
		Use(middleware.Recover()).
		Handle(msgid.Echo, echo)

	log.Printf("websocket example server listening on ws://127.0.0.1%s%s", *addr, *path)
	log.Fatal(app.ListenAndServe())
}

func echo(ctx core.Context) error {
	msg := ctx.Message()
	return ctx.Reply(msg.ID, msg.Data)
}
