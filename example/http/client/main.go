package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"
)

func main() {
	url := flag.String("url", "http://127.0.0.1:8080/echo", "http echo URL")
	text := flag.String("text", "hello http", "echo payload")
	flag.Parse()

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Post(*url, "text/plain", bytes.NewBufferString(*text))
	if err != nil {
		log.Fatal(err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("status=%s body=%s", resp.Status, body)
}
