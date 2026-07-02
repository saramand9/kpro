// File: examples/sse-modular/server/main.go
// Purpose: Provides runnable KPro example code.

package main

import (
	"flag"
	"log"

	routeapp "github.com/saramand9/bignet/kpro/examples/sse-modular/server/internal/app"
	"github.com/saramand9/bignet/kpro/server"
)

func main() {
	addr := flag.String("addr", ":8081", "listen address")
	path := flag.String("path", "/events", "sse path")
	flag.Parse()

	app := server.New(server.Config{Name: "sse-modular-example"})
	routeapp.RegisterRoutes(app, routeapp.Config{Addr: *addr, Path: *path})

	log.Printf("sse modular example listening on http://127.0.0.1%s%s", *addr, *path)
	log.Fatal(app.ListenAndServe())
}
