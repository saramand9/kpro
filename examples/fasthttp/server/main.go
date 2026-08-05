// File: examples/fasthttp/server/main.go
// Purpose: Provides a runnable HTTP/1.1-native fasthttpx example.

package main

import (
	"flag"
	"log"
	"time"

	"github.com/saramand9/kpro/server"
	"github.com/saramand9/kpro/transport/fasthttpx"
)

func main() {
	addr := flag.String("addr", ":8082", "listen address")
	flag.Parse()

	app, err := server.NewChecked(server.Config{
		Name:            "fasthttp-example",
		ShutdownTimeout: 5 * time.Second,
	})
	if err != nil {
		log.Fatal(err)
	}
	endpoint, err := server.MountNativeChecked(app, fasthttpx.New(fasthttpx.Config{
		Addr:            *addr,
		ReadTimeout:     10 * time.Second,
		WriteTimeout:    10 * time.Second,
		IdleTimeout:     30 * time.Second,
		ShutdownTimeout: 5 * time.Second,
		MaxBodyBytes:    1 << 20,
	}))
	if err != nil {
		log.Fatal(err)
	}
	endpoint.
		Get("/health", func(ctx *fasthttpx.Context) error {
			return ctx.Text(200, "ok\n")
		}).
		Post("/echo", func(ctx *fasthttpx.Context) error {
			return ctx.CopyBody(200, "application/octet-stream")
		})

	log.Printf("fasthttp example server listening on http://127.0.0.1%s", *addr)
	log.Fatal(app.ListenAndServe())
}
