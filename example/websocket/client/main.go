package main

import (
	"flag"
	"fmt"
	"log"
	"time"

	"github.com/saramand9/bignet/kpro/core"
	"github.com/saramand9/bignet/kpro/example/internal/wsclient"
	"github.com/saramand9/bignet/kpro/protocol"
)

func main() {
	addr := flag.String("url", "ws://127.0.0.1:8091/ws", "websocket server URL")
	text := flag.String("text", "hello websocket", "echo payload")
	flag.Parse()

	conn, err := wsclient.Dial(*addr, 5*time.Second)
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()

	p := protocol.KProV1()
	packet, err := p.EncodePacket(core.Message{ID: core.MsgEcho, Seq: 1, Data: []byte(*text)})
	if err != nil {
		log.Fatal(err)
	}
	if err := conn.WriteFrame(wsclient.OpBinary, packet); err != nil {
		log.Fatal(err)
	}

	opcode, payload, err := conn.ReadFrame(64 * 1024)
	if err != nil {
		log.Fatal(err)
	}
	if opcode != wsclient.OpBinary {
		log.Fatalf("unexpected websocket opcode %d", opcode)
	}
	resp, err := p.DecodePacket(payload)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("id=%d seq=%d data=%q\n", resp.ID, resp.Seq, string(resp.Data))
}
