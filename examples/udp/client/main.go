// File: examples/udp/client/main.go
// Purpose: Provides runnable KPro example code.

package main

import (
	"flag"
	"fmt"
	"github.com/saramand9/kpro/examples/internal/msgid"
	"log"
	"net"
	"time"

	"github.com/saramand9/kpro/codec"
	"github.com/saramand9/kpro/core"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8089", "server address")
	text := flag.String("text", "hello udp", "echo payload")
	flag.Parse()

	remote, err := net.ResolveUDPAddr("udp", *addr)
	if err != nil {
		log.Fatal(err)
	}
	conn, err := net.DialUDP("udp", nil, remote)
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()

	packet, err := codec.KProV1().EncodePacket(core.Message{ID: msgid.Echo, Seq: 1, Data: []byte(*text)})
	if err != nil {
		log.Fatal(err)
	}
	if _, err := conn.Write(packet); err != nil {
		log.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 2048)
	n, err := conn.Read(buf)
	if err != nil {
		log.Fatal(err)
	}
	msg, err := codec.KProV1().DecodePacket(buf[:n])
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("udp response: id=%d seq=%d data=%q\n", msg.ID, msg.Seq, string(msg.Data))
}
