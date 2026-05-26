package main

import (
	"bufio"
	"flag"
	"fmt"
	"log"
	"net/http"
	"strings"
)

func main() {
	url := flag.String("url", "http://127.0.0.1:8081/events", "sse url")
	events := flag.Int("events", 3, "number of data events to read")
	flag.Parse()

	resp, err := http.Get(*url)
	if err != nil {
		log.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		log.Fatalf("unexpected status: %s", resp.Status)
	}
	scanner := bufio.NewScanner(resp.Body)
	seen := 0
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data: ") {
			seen++
			fmt.Printf("sse data: %s\n", strings.TrimPrefix(line, "data: "))
			if seen >= *events {
				return
			}
		}
	}
	if err := scanner.Err(); err != nil {
		log.Fatal(err)
	}
}
