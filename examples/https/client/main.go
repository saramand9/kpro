// File: examples/https/client/main.go
// Purpose: Provides runnable KPro example code.

package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"

	"github.com/saramand9/kpro/examples/internal/examplecert"
)

func main() {
	base := flag.String("base", "https://127.0.0.1:8443", "server base url")
	text := flag.String("text", "hello https", "echo payload")
	caFile := flag.String("ca", "", "PEM CA file (default: system trust)")
	insecure := flag.Bool("insecure", false, "skip TLS verification for local self-signed demos only")
	flag.Parse()

	tlsConfig, err := examplecert.Client(*caFile, *insecure)
	if err != nil {
		log.Fatal(err)
	}

	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: tlsConfig,
		},
	}
	health, err := client.Get(*base + "/health")
	if err != nil {
		log.Fatal(err)
	}
	healthBody, _ := io.ReadAll(health.Body)
	_ = health.Body.Close()
	fmt.Printf("https health: %s", string(healthBody))

	resp, err := client.Post(*base+"/echo", "text/plain", bytes.NewBufferString(*text))
	if err != nil {
		log.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("https echo: %s", string(body))
}
