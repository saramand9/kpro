package main

import (
	"flag"
	"log"
	"time"

	"github.com/saramand9/bignet/kpro/core"
	"github.com/saramand9/bignet/kpro/examples/internal/msgid"
	"github.com/saramand9/bignet/kpro/middleware"
	"github.com/saramand9/bignet/kpro/server"
	"github.com/saramand9/bignet/kpro/transport/kcp"
)

func main() {
	addr := flag.String("addr", ":8090", "listen address")
	flag.Parse()

	app, err := server.NewChecked(server.Config{Name: "kcp-example"})
	if err != nil {
		log.Fatal(err)
	}
	endpoint := app.KCP(*addr, kcp.Config{
		MaxConnections: 1024,
		SendQueueSize:  64,
		ReadTimeout:    30 * time.Second,
		WriteTimeout:   5 * time.Second,
	})
	endpoint.Use(middleware.Recover())
	endpoint.HandleSync(msgid.Echo, echo)

	log.Printf("kcp example server listening on %s", *addr)
	log.Fatal(app.ListenAndServe())
}

func echo(ctx core.Context) error {
	message := ctx.Message()
	return ctx.Reply(message.ID, message.Data)
}
