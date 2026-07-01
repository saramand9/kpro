package main

import (
	"bytes"
	"crypto/tls"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
)

func main() {
	base := flag.String("base", "https://127.0.0.1:8442", "server base url")
	text := flag.String("text", "hello http2", "echo payload")
	flag.Parse()

	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig:   &tls.Config{InsecureSkipVerify: true}, // example-only local certificate
			ForceAttemptHTTP2: true,
		},
	}
	health, err := client.Get(*base + "/health")
	if err != nil {
		log.Fatal(err)
	}
	healthBody, _ := io.ReadAll(health.Body)
	_ = health.Body.Close()
	if health.Proto != "HTTP/2.0" {
		log.Fatalf("server negotiated %s, want HTTP/2.0", health.Proto)
	}
	fmt.Printf("http2 health (%s): %s", health.Proto, string(healthBody))

	resp, err := client.Post(*base+"/echo", "text/plain", bytes.NewBufferString(*text))
	if err != nil {
		log.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Fatal(err)
	}
	if resp.Proto != "HTTP/2.0" {
		log.Fatalf("server negotiated %s, want HTTP/2.0", resp.Proto)
	}
	fmt.Printf("http2 echo (%s): %s", resp.Proto, string(body))
}
