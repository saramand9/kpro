package main

import (
	"flag"
	"log"

	routeapp "github.com/saramand9/bignet/kpro/examples/websocket-modular/server/internal/app"
	"github.com/saramand9/bignet/kpro/server"
)

func main() {
	addr := flag.String("addr", ":8091", "listen address")
	path := flag.String("path", "/ws", "websocket path")
	flag.Parse()

	app := server.New(server.Config{Name: "websocket-modular-example"})
	routeapp.RegisterRoutes(app, routeapp.Config{Addr: *addr, Path: *path})

	log.Printf("websocket modular example listening on ws://127.0.0.1%s%s", *addr, *path)
	log.Fatal(app.ListenAndServe())
}
