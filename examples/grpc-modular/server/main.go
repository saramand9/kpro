// File: examples/grpc-modular/server/main.go
// Purpose: Provides runnable KPro example code.

package main

import (
	"flag"
	"log"

	routeapp "github.com/saramand9/bignet/kpro/examples/grpc-modular/server/internal/app"
	"github.com/saramand9/bignet/kpro/server"
)

func main() {
	addr := flag.String("addr", ":9090", "listen address")
	flag.Parse()

	app := server.New()
	routeapp.RegisterServices(app.GRPC(*addr))

	log.Printf("grpc modular example listening on %s", *addr)
	if err := app.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}
