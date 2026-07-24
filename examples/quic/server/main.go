package main

import (
	"flag"
	"log"
	"time"

	"github.com/saramand9/kpro/core"
	"github.com/saramand9/kpro/examples/internal/examplecert"
	"github.com/saramand9/kpro/examples/internal/msgid"
	"github.com/saramand9/kpro/middleware"
	"github.com/saramand9/kpro/server"
	quictransport "github.com/saramand9/kpro/transport/quic"
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
	endpoint := app.Mount(quictransport.New(quictransport.Config{
		Addr:                 *addr,
		TLSConfig:            tlsConfig,
		MaxStreams:           1024,
		SendQueueSize:        64,
		ReadTimeout:          30 * time.Second,
		WriteTimeout:         5 * time.Second,
		HandshakeIdleTimeout: 5 * time.Second,
		IdleTimeout:          30 * time.Second,
	}))
	endpoint.Use(middleware.Recover())
	endpoint.HandleSync(msgid.Echo, echo)

	log.Printf("quic example server listening on %s", *addr)
	log.Fatal(app.ListenAndServe())
}

func echo(ctx core.Context) error {
	message := ctx.Message()
	return ctx.Reply(message.ID, message.Data)
}
