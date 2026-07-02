// File: examples/tcp-modular/server/main.go
// Purpose: Provides runnable KPro example code.

package main

import (
	"flag"
	"log"

	routeapp "github.com/saramand9/bignet/kpro/examples/tcp-modular/server/internal/app"
	"github.com/saramand9/bignet/kpro/server"
)

func main() {
	addr := flag.String("addr", ":8088", "listen address")
	flag.Parse()

	app := server.New(server.Config{Name: "tcp-modular-example"})
	routeapp.RegisterRoutes(app, routeapp.Config{Addr: *addr})

	log.Printf("tcp modular example listening on %s", *addr)
	log.Fatal(app.ListenAndServe())
}
