// File: examples/websocket/client/main.go
// Purpose: Provides runnable KPro example code.

package main

import (
	"flag"
	"fmt"
	"github.com/saramand9/bignet/kpro/examples/internal/msgid"
	"log"

	"github.com/saramand9/bignet/kpro/codec"
	"github.com/saramand9/bignet/kpro/core"
	"github.com/saramand9/bignet/kpro/examples/internal/wire"
)

func main() {
	url := flag.String("url", "ws://127.0.0.1:8091/ws", "websocket url")
	text := flag.String("text", "hello websocket", "echo payload")
	flag.Parse()

	conn, reader, err := wire.DialWebSocket(*url)
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()

	packet, err := codec.KProV1().EncodePacket(core.Message{ID: msgid.Echo, Seq: 1, Data: []byte(*text)})
	if err != nil {
		log.Fatal(err)
	}
	if err := wire.WriteWebSocketBinary(conn, packet); err != nil {
		log.Fatal(err)
	}
	replyPacket, err := wire.ReadWebSocketBinary(reader, wire.MaxFrameSize)
	if err != nil {
		log.Fatal(err)
	}
	msg, err := codec.KProV1().DecodePacket(replyPacket)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("websocket response: id=%d seq=%d data=%q\n", msg.ID, msg.Seq, string(msg.Data))
}
