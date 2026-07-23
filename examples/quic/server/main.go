package main

import (
	"flag"
	"log"
	"time"

	"github.com/saramand9/bignet/kpro/core"
	"github.com/saramand9/bignet/kpro/examples/internal/examplecert"
	"github.com/saramand9/bignet/kpro/examples/internal/msgid"
	"github.com/saramand9/bignet/kpro/middleware"
	"github.com/saramand9/bignet/kpro/server"
	quictransport "github.com/saramand9/bignet/kpro/transport/quic"
)

func main() {
	addr := flag.String("addr", ":8444", "listen address")
	flag.Parse()

	tlsConfig, err := examplecert.SelfSignedServer()
	if err != nil {
		log.Fatal(err)
	}
	app, err := server.NewChecked(server.Config{Name: "quic-example"})
	if err != nil {
		log.Fatal(err)
	}
	endpoint := app.QUIC(*addr, quictransport.Config{
		TLSConfig:            tlsConfig,
		MaxStreams:           1024,
		SendQueueSize:        64,
		ReadTimeout:          30 * time.Second,
		WriteTimeout:         5 * time.Second,
		HandshakeIdleTimeout: 5 * time.Second,
		IdleTimeout:          30 * time.Second,
	})
	endpoint.Use(middleware.Recover())
	endpoint.HandleSync(msgid.Echo, echo)

	log.Printf("quic example server listening on %s", *addr)
	log.Fatal(app.ListenAndServe())
}

func echo(ctx core.Context) error {
	message := ctx.Message()
	return ctx.Reply(message.ID, message.Data)
}
