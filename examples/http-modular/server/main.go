// File: examples/http-modular/server/main.go
// Purpose: Provides runnable KPro example code.

package main

import (
	"flag"
	"log"

	routeapp "github.com/saramand9/kpro/examples/http-modular/server/internal/app"
	"github.com/saramand9/kpro/server"
)

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	flag.Parse()

	app := server.New(server.Config{Name: "http-modular-example"})
	routeapp.RegisterRoutes(app, routeapp.Config{HTTPAddr: *addr})

	log.Printf("http modular example server listening on http://127.0.0.1%s", *addr)
	log.Fatal(app.ListenAndServe())
}
