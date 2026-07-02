// File: examples/sse/server/main.go
// Purpose: Provides runnable KPro example code.

package main

import (
	"flag"
	"log"
	"time"

	"github.com/saramand9/bignet/kpro/server"
	"github.com/saramand9/bignet/kpro/transport/sse"
)

func main() {
	addr := flag.String("addr", ":8081", "listen address")
	path := flag.String("path", "/events", "sse path")
	flag.Parse()

	app := server.New(server.Config{Name: "sse-example"})
	app.SSE(*addr, *path).
		Handle(func(ctx *sse.Context) error {
			ticker := time.NewTicker(time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return nil
				case t := <-ticker.C:
					if err := ctx.Send("time", []byte(t.Format(time.RFC3339))); err != nil {
						return err
					}
				}
			}
		})

	log.Printf("sse example server listening on http://127.0.0.1%s%s", *addr, *path)
	log.Fatal(app.ListenAndServe())
}
