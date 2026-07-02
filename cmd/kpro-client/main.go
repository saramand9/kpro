package main

import (
	"encoding/binary"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	legacy "github.com/saramand9/bignet/kpro/legacy"
)

type counters struct {
	connected int64
	sent      int64
	received  int64
	errors    int64

	dialErrors  int64
	readErrors  int64
	writeErrors int64

	errMu   sync.Mutex
	errText map[string]int
}

func main() {
	addr := flag.String("addr", "127.0.0.1:8088", "server address")
	conns := flag.Int("conns", 1000, "number of client connections")
	rate := flag.Int("rate", 1, "messages per second per connection")
	duration := flag.Duration("duration", 30*time.Second, "test duration")
	drain := flag.Duration("drain", 2*time.Second, "time to wait for in-flight replies after stopping sends")
	ramp := flag.Duration("ramp", 10*time.Second, "time window used to spread connection establishment")
	payloadSize := flag.Int("payload", 64, "payload bytes")
	flag.Parse()

	payload := make([]byte, *payloadSize)
	for i := range payload {
		payload[i] = byte('a' + i%26)
	}

	var wg sync.WaitGroup
	cs := counters{errText: make(map[string]int)}
	stop := make(chan struct{})
	start := time.Now()

	go printStats(&cs, start, stop)
	var connectDelay time.Duration
	if *conns > 0 && *ramp > 0 {
		connectDelay = *ramp / time.Duration(*conns)
	}
	for i := 0; i < *conns; i++ {
		wg.Add(1)
		go runConn(&wg, &cs, *addr, *rate, payload, *drain, stop)
		if connectDelay > 0 {
			time.Sleep(connectDelay)
		}
	}

	time.Sleep(*duration)
	close(stop)
	wg.Wait()
	fmt.Printf("done: connected=%d sent=%d received=%d errors=%d\n",
		atomic.LoadInt64(&cs.connected),
		atomic.LoadInt64(&cs.sent),
		atomic.LoadInt64(&cs.received),
		atomic.LoadInt64(&cs.errors),
	)
	fmt.Printf("error_breakdown: dial=%d read=%d write=%d\n",
		atomic.LoadInt64(&cs.dialErrors),
		atomic.LoadInt64(&cs.readErrors),
		atomic.LoadInt64(&cs.writeErrors),
	)
	cs.errMu.Lock()
	for text, count := range cs.errText {
		fmt.Printf("error_sample[%d]: %s\n", count, text)
	}
	cs.errMu.Unlock()
}

func runConn(wg *sync.WaitGroup, cs *counters, addr string, rate int, payload []byte, drain time.Duration, stop <-chan struct{}) {
	defer wg.Done()
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		recordError(cs, "dial", err)
		return
	}
	defer conn.Close()
	atomic.AddInt64(&cs.connected, 1)

	done := make(chan struct{})
	go readLoop(conn, cs, done)

	interval := time.Second
	if rate > 0 {
		interval = time.Second / time.Duration(rate)
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	var seq uint64
	for {
		select {
		case <-stop:
			time.Sleep(drain)
			close(done)
			return
		case <-ticker.C:
			seq++
			if err := writeFrame(conn, legacy.MsgEcho, seq, payload); err != nil {
				recordError(cs, "write", err)
				close(done)
				return
			}
			atomic.AddInt64(&cs.sent, 1)
		}
	}
}

func readLoop(conn net.Conn, cs *counters, done <-chan struct{}) {
	for {
		select {
		case <-done:
			return
		default:
		}
		var header [legacy.FrameHeaderSize]byte
		if _, err := io.ReadFull(conn, header[:]); err != nil {
			select {
			case <-done:
				return
			default:
			}
			recordError(cs, "read", err)
			return
		}
		length := binary.BigEndian.Uint32(header[16:20])
		if length > 0 {
			body := make([]byte, int(length))
			if _, err := io.ReadFull(conn, body); err != nil {
				select {
				case <-done:
					return
				default:
				}
				recordError(cs, "read", err)
				return
			}
		}
		atomic.AddInt64(&cs.received, 1)
	}
}

func writeFrame(conn net.Conn, msgID uint32, seq uint64, data []byte) error {
	buf := make([]byte, legacy.FrameHeaderSize+len(data))
	binary.BigEndian.PutUint16(buf[0:2], legacy.FrameMagic)
	buf[2] = legacy.FrameVersion
	binary.BigEndian.PutUint32(buf[4:8], msgID)
	binary.BigEndian.PutUint64(buf[8:16], seq)
	binary.BigEndian.PutUint32(buf[16:20], uint32(len(data)))
	copy(buf[legacy.FrameHeaderSize:], data)
	for len(buf) > 0 {
		n, err := conn.Write(buf)
		if err != nil {
			return err
		}
		buf = buf[n:]
	}
	return nil
}

func recordError(cs *counters, kind string, err error) {
	atomic.AddInt64(&cs.errors, 1)
	switch kind {
	case "dial":
		atomic.AddInt64(&cs.dialErrors, 1)
	case "read":
		atomic.AddInt64(&cs.readErrors, 1)
	case "write":
		atomic.AddInt64(&cs.writeErrors, 1)
	}
	text := kind + ": " + normalizeErr(err)
	cs.errMu.Lock()
	if len(cs.errText) < 12 || cs.errText[text] > 0 {
		cs.errText[text]++
	}
	cs.errMu.Unlock()
}

func normalizeErr(err error) string {
	if err == nil {
		return ""
	}
	text := err.Error()
	if strings.Contains(text, "connectex") {
		return text
	}
	if strings.Contains(text, "i/o timeout") {
		return "i/o timeout"
	}
	if strings.Contains(text, "connection reset") {
		return "connection reset"
	}
	if strings.Contains(text, "use of closed network connection") {
		return "use of closed network connection"
	}
	if len(text) > 160 {
		return text[:160]
	}
	return text
}

func printStats(cs *counters, start time.Time, stop <-chan struct{}) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	var lastSent, lastRecv int64
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			sent := atomic.LoadInt64(&cs.sent)
			recv := atomic.LoadInt64(&cs.received)
			log.Printf("elapsed=%s connected=%d sent/s=%d recv/s=%d total_sent=%d total_recv=%d errors=%d",
				time.Since(start).Truncate(time.Second),
				atomic.LoadInt64(&cs.connected),
				sent-lastSent,
				recv-lastRecv,
				sent,
				recv,
				atomic.LoadInt64(&cs.errors),
			)
			lastSent, lastRecv = sent, recv
		}
	}
}
