// File: example/udp/server/main.go
// Purpose: Provides runnable KPro example code.

package main

import (
	"flag"
	"log"

	"github.com/saramand9/bignet/kpro/core"
	"github.com/saramand9/bignet/kpro/example/internal/exampleutil"
	"github.com/saramand9/bignet/kpro/middleware"
	"github.com/saramand9/bignet/kpro/protocol"
	"github.com/saramand9/bignet/kpro/server"
	"github.com/saramand9/bignet/kpro/transport/udp"
)

func main() {
	addr := flag.String("addr", ":8089", "udp listen address")
	flag.Parse()

	app := server.New(server.Config{Name: "udp-example"})
	app.UDP(*addr, udp.Config{
		Protocol:      protocol.KProV1(),
		MaxPacketSize: 2048,
	}).
		Use(middleware.Recover(), middleware.MaxPayload(1500)).
		Handle(core.MsgEcho, exampleutil.Echo)

	log.Printf("udp example server listening on %s", *addr)
	if err := exampleutil.Run(app); err != nil {
		log.Fatal(err)
	}
}
