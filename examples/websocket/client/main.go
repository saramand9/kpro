package main

import (
	"flag"
	"fmt"
	"log"

	"github.com/saramand9/bignet/kpro/core"
	"github.com/saramand9/bignet/kpro/examples/internal/wire"
	"github.com/saramand9/bignet/kpro/protocol"
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

	packet, err := protocol.KProV1().EncodePacket(core.Message{ID: core.MsgEcho, Seq: 1, Data: []byte(*text)})
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
	msg, err := protocol.KProV1().DecodePacket(replyPacket)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("websocket response: id=%d seq=%d data=%q\n", msg.ID, msg.Seq, string(msg.Data))
}
