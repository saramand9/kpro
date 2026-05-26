package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"time"

	"github.com/saramand9/bignet/kpro/core"
	"github.com/saramand9/bignet/kpro/protocol"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8089", "udp server address")
	text := flag.String("text", "hello udp", "echo payload")
	flag.Parse()

	conn, err := net.DialTimeout("udp", *addr, 5*time.Second)
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()

	p := protocol.KProV1()
	req, err := p.EncodePacket(core.Message{ID: core.MsgEcho, Seq: 1, Data: []byte(*text)})
	if err != nil {
		log.Fatal(err)
	}
	if _, err := conn.Write(req); err != nil {
		log.Fatal(err)
	}

	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		log.Fatal(err)
	}
	buf := make([]byte, 2048)
	n, err := conn.Read(buf)
	if err != nil {
		log.Fatal(err)
	}
	resp, err := p.DecodePacket(buf[:n])
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("id=%d seq=%d data=%q\n", resp.ID, resp.Seq, string(resp.Data))
}
