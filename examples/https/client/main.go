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
	base := flag.String("base", "https://127.0.0.1:8443", "server base url")
	text := flag.String("text", "hello https", "echo payload")
	flag.Parse()

	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, // example-only local certificate
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
