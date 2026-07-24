// File: examples/grpc-modular/server/main.go
// Purpose: Provides runnable KPro example code.

package main

import (
	"flag"
	"log"

	routeapp "github.com/saramand9/kpro/examples/grpc-modular/server/internal/app"
	"github.com/saramand9/kpro/server"
	"github.com/saramand9/kpro/transport/grpcx"
)

func main() {
	addr := flag.String("addr", ":9090", "listen address")
	flag.Parse()

	app := server.New()
	grpcEndpoint := grpcx.NewServer(*addr)
	app.MountRunner(grpcEndpoint)
	routeapp.RegisterServices(grpcEndpoint)

	log.Printf("grpc modular example listening on %s", *addr)
	if err := app.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}
