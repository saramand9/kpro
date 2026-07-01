package main

import (
	"flag"
	"log"

	routeapp "github.com/saramand9/bignet/kpro/examples/grpc-modular/server/internal/app"
	"github.com/saramand9/bignet/kpro/kprogrpc"
)

func main() {
	addr := flag.String("addr", ":9090", "listen address")
	flag.Parse()

	srv := kprogrpc.NewServer(*addr)
	routeapp.RegisterServices(srv)

	log.Printf("grpc modular example listening on %s", *addr)
	if err := srv.Serve(); err != nil {
		log.Fatal(err)
	}
}
