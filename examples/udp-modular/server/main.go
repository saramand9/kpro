// File: examples/udp-modular/server/main.go
// Purpose: Provides runnable KPro example code.

package main

import (
	"flag"
	"log"

	routeapp "github.com/saramand9/kpro/examples/udp-modular/server/internal/app"
	"github.com/saramand9/kpro/server"
)

func main() {
	addr := flag.String("addr", ":8089", "listen address")
	flag.Parse()

	app := server.New(server.Config{Name: "udp-modular-example"})
	routeapp.RegisterRoutes(app, routeapp.Config{Addr: *addr})

	log.Printf("udp modular example listening on %s", *addr)
	log.Fatal(app.ListenAndServe())
}
