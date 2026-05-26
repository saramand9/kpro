package main

import (
	"flag"
	"log"

	"github.com/saramand9/bignet/kpro/server"
	"github.com/saramand9/bignet/kpro/transport/httpx"
)

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	flag.Parse()

	app := server.New(server.Config{Name: "http-example"})
	app.HTTP(*addr).
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
