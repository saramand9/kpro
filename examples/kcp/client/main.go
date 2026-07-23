package main

import (
	"flag"
	"fmt"
	"log"
	"time"

	"github.com/saramand9/bignet/kpro/core"
	"github.com/saramand9/bignet/kpro/examples/internal/msgid"
	"github.com/saramand9/bignet/kpro/examples/internal/wire"
	kcpgo "github.com/xtaci/kcp-go"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8090", "server address")
	text := flag.String("text", "hello kcp", "echo payload")
	flag.Parse()

	conn, err := kcpgo.DialWithOptions(*addr, nil, 0, 0)
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		log.Fatal(err)
	}
	if err := wire.WriteMessage(conn, core.Message{ID: msgid.Echo, Seq: 1, Data: []byte(*text)}); err != nil {
		log.Fatal(err)
	}
	message, err := wire.ReadMessage(conn)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("kcp response: id=%d seq=%d data=%q\n", message.ID, message.Seq, string(message.Data))
}
