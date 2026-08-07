package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"runtime"
	"strconv"
	"time"

	"github.com/saramand9/kpro/transport/sse"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:18501", "listen address")
	yieldEvery := flag.Uint64("yield-every", 0, "yield the producer goroutine after this many events; zero disables yielding")
	flag.Parse()

	endpoint := sse.New(sse.Config{
		Addr:       *addr,
		Path:       "/events",
		MaxStreams: 4096,
	})
	endpoint.Handle(func(ctx *sse.Context) error {
		payloadSize, err := queryInt(ctx.Request, "payload", 16, 64*1024)
		if err != nil {
			return err
		}
		streamID, err := queryInt(ctx.Request, "stream", 0, 1<<30)
		if err != nil {
			return err
		}
		startNanos, err := strconv.ParseInt(ctx.Request.URL.Query().Get("start_ns"), 10, 64)
		if err != nil {
			return err
		}
		if wait := time.Until(time.Unix(0, startNanos)); wait > 0 {
			timer := time.NewTimer(wait)
			defer timer.Stop()
			select {
			case <-timer.C:
			case <-ctx.Done():
				return nil
			}
		}

		payload := makePayload(payloadSize, streamID)
		var sequence uint64
		for {
			select {
			case <-ctx.Done():
				return nil
			default:
			}
			sequence++
			writeHexSequence(payload[:16], sequence)
			if err := ctx.Send("message", payload); err != nil {
				return nil
			}
			if *yieldEvery > 0 && sequence%*yieldEvery == 0 {
				runtime.Gosched()
			}
		}
	})

	log.Printf("kpro SSE benchmark listening on http://%s/events", *addr)
	if err := endpoint.ListenAndServe(context.Background()); err != nil {
		log.Fatal(err)
	}
}

func queryInt(request *http.Request, name string, minimum, maximum int) (int, error) {
	value, err := strconv.Atoi(request.URL.Query().Get(name))
	if err != nil {
		return 0, err
	}
	if value < minimum || value > maximum {
		return 0, strconv.ErrRange
	}
	return value, nil
}

func makePayload(size, streamID int) []byte {
	payload := make([]byte, size)
	for index := 16; index < size; index++ {
		payload[index] = byte('a' + (index+streamID)%26)
	}
	return payload
}

func writeHexSequence(dst []byte, sequence uint64) {
	const digits = "0123456789abcdef"
	for index := 15; index >= 0; index-- {
		dst[index] = digits[sequence&0x0f]
		sequence >>= 4
	}
}
