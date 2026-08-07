package main

import (
	"context"
	"flag"
	"log"

	"github.com/saramand9/kpro/core"
	"github.com/saramand9/kpro/transport/websocket"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:18401", "listen address")
	flag.Parse()

	server := websocket.New(websocket.Config{
		Addr:                *addr,
		Path:                "/ws",
		MaxMessage:          1 << 20,
		MaxConnections:      4096,
		SendQueueSize:       256,
		MaxPendingBytes:     16 << 20,
		MaxWriteBatchFrames: 16,
		MaxWriteBatchBytes:  1 << 20,
	})
	log.Printf("kpro websocket echo listening on %s/ws", *addr)
	if err := server.ListenAndServe(context.Background(), func(ctx core.Context) error {
		message := ctx.Message()
		return ctx.Reply(message.ID, message.Data)
	}); err != nil {
		log.Fatal(err)
	}
}
