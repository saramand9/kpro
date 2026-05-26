package main

import (
	"flag"
	"log"

	"github.com/saramand9/bignet/kpro/core"
	"github.com/saramand9/bignet/kpro/example/internal/exampleutil"
	"github.com/saramand9/bignet/kpro/middleware"
	"github.com/saramand9/bignet/kpro/protocol"
	"github.com/saramand9/bignet/kpro/server"
	"github.com/saramand9/bignet/kpro/transport/websocket"
)

func main() {
	addr := flag.String("addr", ":8091", "websocket listen address")
	path := flag.String("path", "/ws", "websocket path")
	flag.Parse()

	app := server.New(server.Config{Name: "websocket-example"})
	app.WebSocket(*addr, *path, websocket.Config{
		Protocol:   protocol.KProV1(),
		MaxMessage: 64 * 1024,
	}).
		Use(middleware.Recover(), middleware.MaxPayload(64*1024)).
		Handle(core.MsgEcho, exampleutil.Echo)

	log.Printf("websocket example server listening on %s%s", *addr, *path)
	if err := exampleutil.Run(app); err != nil {
		log.Fatal(err)
	}
}
