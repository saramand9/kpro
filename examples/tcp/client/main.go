// File: examples/tcp/client/main.go
// Purpose: Provides runnable KPro example code.

package main

import (
	"flag"
	"fmt"
	"github.com/saramand9/kpro/examples/internal/msgid"
	"log"
	"net"
	"time"

	"github.com/saramand9/kpro/core"
	"github.com/saramand9/kpro/examples/internal/wire"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8088", "server address")
	text := flag.String("text", "hello tcp", "echo payload")
	flag.Parse()

	conn, err := net.DialTimeout("tcp", *addr, 5*time.Second)
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()

	if err := wire.WriteMessage(conn, core.Message{ID: msgid.Echo, Seq: 1, Data: []byte(*text)}); err != nil {
		log.Fatal(err)
	}
	msg, err := wire.ReadMessage(conn)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("tcp response: id=%d seq=%d data=%q\n", msg.ID, msg.Seq, string(msg.Data))
}
