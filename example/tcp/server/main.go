package main

import (
	"flag"
	"log"

	"github.com/saramand9/bignet/kpro/core"
	"github.com/saramand9/bignet/kpro/example/internal/exampleutil"
	"github.com/saramand9/bignet/kpro/middleware"
	"github.com/saramand9/bignet/kpro/protocol"
	"github.com/saramand9/bignet/kpro/server"
	"github.com/saramand9/bignet/kpro/transport/tcp"
)

func main() {
	addr := flag.String("addr", ":8088", "tcp listen address")
	flag.Parse()

	app := server.New(server.Config{Name: "tcp-example"})
	app.TCP(*addr, tcp.Config{
		Protocol:     protocol.KProV1(),
		MaxFrameSize: 64 * 1024,
	}).
		Use(middleware.Recover(), middleware.MaxPayload(64*1024)).
		Handle(core.MsgEcho, exampleutil.Echo)

	log.Printf("tcp example server listening on %s", *addr)
	if err := exampleutil.Run(app); err != nil {
		log.Fatal(err)
	}
}
