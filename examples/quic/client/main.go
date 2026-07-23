package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"log"
	"time"

	quicgo "github.com/quic-go/quic-go"
	"github.com/saramand9/bignet/kpro/core"
	"github.com/saramand9/bignet/kpro/examples/internal/msgid"
	"github.com/saramand9/bignet/kpro/examples/internal/wire"
	quictransport "github.com/saramand9/bignet/kpro/transport/quic"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8444", "server address")
	text := flag.String("text", "hello quic", "echo payload")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := quicgo.DialAddr(ctx, *addr, &tls.Config{
		InsecureSkipVerify: true, // example-only local certificate
		NextProtos:         []string{quictransport.DefaultALPN},
	}, nil)
	if err != nil {
		log.Fatal(err)
	}
	defer conn.CloseWithError(0, "example complete")
	stream, err := conn.OpenStreamSync(ctx)
	if err != nil {
		log.Fatal(err)
	}
	defer stream.Close()
	if err := stream.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		log.Fatal(err)
	}
	if err := wire.WriteMessage(stream, core.Message{ID: msgid.Echo, Seq: 1, Data: []byte(*text)}); err != nil {
		log.Fatal(err)
	}
	message, err := wire.ReadMessage(stream)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("quic response: id=%d seq=%d data=%q\n", message.ID, message.Seq, string(message.Data))
}
