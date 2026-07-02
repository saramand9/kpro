// File: examples/http/client/main.go
// Purpose: Provides runnable KPro example code.

package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
)

func main() {
	base := flag.String("base", "http://127.0.0.1:8080", "server base url")
	text := flag.String("text", "hello http", "echo payload")
	flag.Parse()

	health, err := http.Get(*base + "/health")
	if err != nil {
		log.Fatal(err)
	}
	healthBody, _ := io.ReadAll(health.Body)
	_ = health.Body.Close()
	fmt.Printf("http health: %s", string(healthBody))

	resp, err := http.Post(*base+"/echo", "text/plain", bytes.NewBufferString(*text))
	if err != nil {
		log.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("http echo: %s", string(body))
}
