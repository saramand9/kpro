// File: examples/http/server/main.go
// Purpose: Provides runnable KPro example code.

package main

import (
	"flag"
	"log"

	"github.com/saramand9/kpro/server"
	"github.com/saramand9/kpro/transport/httpx"
)

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	flag.Parse()

	app := server.New(server.Config{Name: "http-example"})
	httpEndpoint := httpx.New(httpx.Config{Addr: *addr})
	app.MountRunner(httpEndpoint)
	httpEndpoint.
		Get("/health", func(ctx *httpx.Context) error {
			return ctx.Text(200, "ok\n")
		}).
		Post("/echo", func(ctx *httpx.Context) error {
			body, err := ctx.Body()
			if err != nil {
				return err
			}
			return ctx.JSON(200, map[string]interface{}{"echo": string(body)})
		})

	log.Printf("http example server listening on http://127.0.0.1%s", *addr)
	log.Fatal(app.ListenAndServe())
}
