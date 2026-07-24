// File: examples/https-modular/server/main.go
// Purpose: Provides runnable KPro example code.

package main

import (
	"flag"
	"log"

	routeapp "github.com/saramand9/kpro/examples/https-modular/server/internal/app"
	"github.com/saramand9/kpro/server"
)

func main() {
	addr := flag.String("addr", ":8443", "listen address")
	flag.Parse()

	certFile, keyFile, cleanup, err := routeapp.SelfSignedCert()
	if err != nil {
		log.Fatal(err)
	}
	defer cleanup()

	app := server.New(server.Config{Name: "https-modular-example"})
	routeapp.RegisterRoutes(app, routeapp.Config{
		Addr:     *addr,
		CertFile: certFile,
		KeyFile:  keyFile,
	})

	log.Printf("https modular example listening on https://127.0.0.1%s", *addr)
	log.Fatal(app.ListenAndServe())
}
