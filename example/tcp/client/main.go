// File: example/tcp/client/main.go
// Purpose: Provides runnable KPro example code.

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
	addr := flag.String("addr", "127.0.0.1:8088", "tcp server address")
	text := flag.String("text", "hello tcp", "echo payload")
	flag.Parse()

	conn, err := net.DialTimeout("tcp", *addr, 5*time.Second)
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()

	p := protocol.KProV1()
	req := core.Message{ID: core.MsgEcho, Seq: 1, Data: []byte(*text)}
	frame, err := p.Encode(req)
	if err != nil {
		log.Fatal(err)
	}
	if _, err := conn.Write(frame); err != nil {
		log.Fatal(err)
	}

	resp, err := p.Read(conn, 64*1024)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("id=%d seq=%d data=%q\n", resp.Message.ID, resp.Message.Seq, string(resp.Message.Data))
}
