package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"log"
	"time"

	quicgo "github.com/quic-go/quic-go"
	"github.com/saramand9/kpro/core"
	"github.com/saramand9/kpro/examples/internal/examplecert"
	"github.com/saramand9/kpro/examples/internal/msgid"
	"github.com/saramand9/kpro/examples/internal/wire"
	quictransport "github.com/saramand9/kpro/transport/quic"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8444", "server address")
	text := flag.String("text", "hello quic", "echo payload")
	caFile := flag.String("ca", "", "PEM CA file (default: system trust)")
	insecure := flag.Bool("insecure", false, "skip TLS verification for local self-signed demos only")
	flag.Parse()

	tlsConfig, err := examplecert.Client(*caFile, *insecure)
	if err != nil {
		log.Fatal(err)
	}

	tlsConfig.MinVersion = tls.VersionTLS13
	tlsConfig.NextProtos = []string{quictransport.DefaultALPN}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := quicgo.DialAddr(ctx, *addr, tlsConfig, nil)
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
