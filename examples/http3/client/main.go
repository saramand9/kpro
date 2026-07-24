package main

import (
	"bytes"
	"crypto/tls"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"

	qh3 "github.com/quic-go/quic-go/http3"
)

func main() {
	base := flag.String("base", "https://127.0.0.1:8443", "server base url")
	text := flag.String("text", "hello http3", "echo payload")
	flag.Parse()

	roundTripper := &qh3.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, // example-only local certificate
	}
	defer roundTripper.Close()
	client := &http.Client{Transport: roundTripper}

	health, err := client.Get(*base + "/health")
	if err != nil {
		log.Fatal(err)
	}
	healthBody, _ := io.ReadAll(health.Body)
	_ = health.Body.Close()
	if health.Proto != "HTTP/3.0" {
		log.Fatalf("server negotiated %s, want HTTP/3.0", health.Proto)
	}
	fmt.Printf("http3 health (%s): %s", health.Proto, string(healthBody))

	resp, err := client.Post(*base+"/echo", "text/plain", bytes.NewBufferString(*text))
	if err != nil {
		log.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Fatal(err)
	}
	if resp.Proto != "HTTP/3.0" {
		log.Fatalf("server negotiated %s, want HTTP/3.0", resp.Proto)
	}
	fmt.Printf("http3 echo (%s): %s", resp.Proto, string(body))
}
