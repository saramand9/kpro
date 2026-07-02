// File: example/sse/client/main.go
// Purpose: Provides runnable KPro example code.

package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
)

func main() {
	url := flag.String("url", "http://127.0.0.1:8081/events", "sse URL")
	events := flag.Int("events", 3, "number of events to read before exiting")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, *url, nil)
	if err != nil {
		log.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		log.Fatalf("unexpected status: %s", resp.Status)
	}

	scanner := bufio.NewScanner(resp.Body)
	count := 0
	for scanner.Scan() {
		line := scanner.Text()
		fmt.Println(line)
		if strings.TrimSpace(line) == "" {
			count++
			if *events > 0 && count >= *events {
				return
			}
		}
	}
	if err := scanner.Err(); err != nil {
		log.Fatal(err)
	}
}
