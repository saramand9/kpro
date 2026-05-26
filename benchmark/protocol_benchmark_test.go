package benchmark

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/binary"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/saramand9/bignet/kpro/core"
	"github.com/saramand9/bignet/kpro/protocol"
	"github.com/saramand9/bignet/kpro/transport/httpx"
	"github.com/saramand9/bignet/kpro/transport/sse"
	"github.com/saramand9/bignet/kpro/transport/tcp"
	"github.com/saramand9/bignet/kpro/transport/udp"
	wstransport "github.com/saramand9/bignet/kpro/transport/websocket"
)

var benchmarkPayloadSizes = []int{64, 1024}

func BenchmarkTCP_Echo(b *testing.B) {
	for _, size := range benchmarkPayloadSizes {
		b.Run(payloadName(size), func(b *testing.B) {
			benchmarkTCPEcho(b, size)
		})
	}
}

func BenchmarkUDP_Echo(b *testing.B) {
	for _, size := range benchmarkPayloadSizes {
		b.Run(payloadName(size), func(b *testing.B) {
			benchmarkUDPEcho(b, size)
		})
	}
}

func BenchmarkWebSocket_Echo(b *testing.B) {
	for _, size := range benchmarkPayloadSizes {
		b.Run(payloadName(size), func(b *testing.B) {
			benchmarkWebSocketEcho(b, size)
		})
	}
}

func BenchmarkHTTP_Echo(b *testing.B) {
	for _, size := range benchmarkPayloadSizes {
		b.Run(payloadName(size), func(b *testing.B) {
			benchmarkHTTPEcho(b, size, false)
		})
	}
}

func BenchmarkHTTPS_Echo(b *testing.B) {
	for _, size := range benchmarkPayloadSizes {
		b.Run(payloadName(size), func(b *testing.B) {
			benchmarkHTTPEcho(b, size, true)
		})
	}
}

func BenchmarkSSE_Events(b *testing.B) {
	for _, size := range benchmarkPayloadSizes {
		b.Run(payloadName(size), func(b *testing.B) {
			benchmarkSSEEvents(b, size)
		})
	}
}

func benchmarkTCPEcho(b *testing.B, payloadSize int) {
	addr, stop := startTCPBenchmarkServer(b)
	defer stop()

	conn := dialTCPBenchmark(b, addr)
	defer conn.Close()

	payload := bytes.Repeat([]byte("x"), payloadSize)
	msg := core.Message{ID: core.MsgEcho, Data: payload}

	b.SetBytes(int64(payloadSize))
	b.ReportAllocs()
	started := startBenchmarkTimer(b)
	for i := 0; i < b.N; i++ {
		msg.Seq = uint64(i + 1)
		writeBenchmarkMessage(b, conn, msg)
		got := readBenchmarkMessage(b, conn)
		if got.Seq != msg.Seq || got.ID != msg.ID || len(got.Data) != len(payload) {
			b.Fatalf("unexpected TCP response: id=%d seq=%d len=%d", got.ID, got.Seq, len(got.Data))
		}
	}
	reportBenchmarkMetrics(b, started, payloadSize)
}

func benchmarkUDPEcho(b *testing.B, payloadSize int) {
	addr, stop := startUDPBenchmarkServer(b)
	defer stop()

	remote, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		b.Fatalf("resolve UDP addr: %v", err)
	}
	conn, err := net.DialUDP("udp", nil, remote)
	if err != nil {
		b.Fatalf("dial UDP addr: %v", err)
	}
	defer conn.Close()

	payload := bytes.Repeat([]byte("x"), payloadSize)
	msg := core.Message{ID: core.MsgEcho, Data: payload}
	buf := make([]byte, 64*1024)

	b.SetBytes(int64(payloadSize))
	b.ReportAllocs()
	started := startBenchmarkTimer(b)
	for i := 0; i < b.N; i++ {
		msg.Seq = uint64(i + 1)
		packet, err := protocol.KProV1().EncodePacket(msg)
		if err != nil {
			b.Fatalf("encode UDP packet: %v", err)
		}
		if _, err := conn.Write(packet); err != nil {
			b.Fatalf("write UDP packet: %v", err)
		}
		_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		n, err := conn.Read(buf)
		if err != nil {
			b.Fatalf("read UDP packet: %v", err)
		}
		got, err := protocol.KProV1().DecodePacket(buf[:n])
		if err != nil {
			b.Fatalf("decode UDP packet: %v", err)
		}
		if got.Seq != msg.Seq || got.ID != msg.ID || len(got.Data) != len(payload) {
			b.Fatalf("unexpected UDP response: id=%d seq=%d len=%d", got.ID, got.Seq, len(got.Data))
		}
	}
	reportBenchmarkMetrics(b, started, payloadSize)
}

func benchmarkWebSocketEcho(b *testing.B, payloadSize int) {
	addr, stop := startWebSocketBenchmarkServer(b)
	defer stop()

	conn, reader := dialWebSocketBenchmark(b, "ws://"+addr+"/ws")
	defer conn.Close()

	payload := bytes.Repeat([]byte("x"), payloadSize)
	msg := core.Message{ID: core.MsgEcho, Data: payload}

	b.SetBytes(int64(payloadSize))
	b.ReportAllocs()
	started := startBenchmarkTimer(b)
	for i := 0; i < b.N; i++ {
		msg.Seq = uint64(i + 1)
		packet, err := protocol.KProV1().EncodePacket(msg)
		if err != nil {
			b.Fatalf("encode WebSocket packet: %v", err)
		}
		if err := writeBenchmarkWebSocketBinary(conn, packet); err != nil {
			b.Fatalf("write WebSocket frame: %v", err)
		}
		replyPacket, err := readBenchmarkWebSocketBinary(reader, 64*1024)
		if err != nil {
			b.Fatalf("read WebSocket frame: %v", err)
		}
		got, err := protocol.KProV1().DecodePacket(replyPacket)
		if err != nil {
			b.Fatalf("decode WebSocket packet: %v", err)
		}
		if got.Seq != msg.Seq || got.ID != msg.ID || len(got.Data) != len(payload) {
			b.Fatalf("unexpected WebSocket response: id=%d seq=%d len=%d", got.ID, got.Seq, len(got.Data))
		}
	}
	reportBenchmarkMetrics(b, started, payloadSize)
}

func benchmarkHTTPEcho(b *testing.B, payloadSize int, tlsEnabled bool) {
	addr, stop, client, scheme := startHTTPBenchmarkServer(b, tlsEnabled)
	defer stop()

	payload := bytes.Repeat([]byte("x"), payloadSize)
	url := scheme + "://" + addr + "/echo"

	b.SetBytes(int64(payloadSize))
	b.ReportAllocs()
	started := startBenchmarkTimer(b)
	for i := 0; i < b.N; i++ {
		req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(payload))
		if err != nil {
			b.Fatalf("create HTTP request: %v", err)
		}
		req.Header.Set("Content-Type", "text/plain")
		resp, err := client.Do(req)
		if err != nil {
			b.Fatalf("do HTTP request: %v", err)
		}
		body, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			b.Fatalf("read HTTP response: %v", err)
		}
		if resp.StatusCode != http.StatusOK || len(body) != len(payload) {
			b.Fatalf("unexpected HTTP response: status=%d len=%d", resp.StatusCode, len(body))
		}
	}
	reportBenchmarkMetrics(b, started, payloadSize)
}

func benchmarkSSEEvents(b *testing.B, payloadSize int) {
	addr, stop, start := startSSEBenchmarkServer(b, payloadSize)
	defer stop()

	resp, err := http.Get("http://" + addr + "/events")
	if err != nil {
		b.Fatalf("get SSE events: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b.Fatalf("unexpected SSE status: %s", resp.Status)
	}
	scanner := bufio.NewScanner(resp.Body)

	b.SetBytes(int64(payloadSize))
	b.ReportAllocs()
	close(start)
	started := startBenchmarkTimer(b)
	for seen := 0; seen < b.N; {
		if !scanner.Scan() {
			if err := scanner.Err(); err != nil {
				b.Fatalf("scan SSE event: %v", err)
			}
			b.Fatal("SSE stream ended")
		}
		if strings.HasPrefix(scanner.Text(), "data: ") {
			seen++
		}
	}
	reportBenchmarkMetrics(b, started, payloadSize)
}

func startBenchmarkTimer(b *testing.B) time.Time {
	b.Helper()
	b.ResetTimer()
	return time.Now()
}

func reportBenchmarkMetrics(b *testing.B, started time.Time, bytesPerOp int) {
	b.Helper()
	elapsed := time.Since(started)
	b.StopTimer()
	if b.N <= 0 {
		return
	}
	if elapsed <= 0 {
		elapsed = time.Nanosecond
	}

	opsPerSecond := float64(b.N) / elapsed.Seconds()
	b.ReportMetric(float64(elapsed.Nanoseconds())/float64(b.N), "ns/op")
	b.ReportMetric(float64(bytesPerOp)*opsPerSecond/1e6, "MB/s")
	b.ReportMetric(opsPerSecond, "qps")
}

func startTCPBenchmarkServer(b *testing.B) (string, func()) {
	b.Helper()

	addr := "127.0.0.1:" + freeTCPPort(b)
	router := benchmarkEchoRouter()
	transport := tcp.New(tcp.Config{
		Addr:          addr,
		Protocol:      protocol.KProV1(),
		SendQueueSize: 1024,
		ReadTimeout:   10 * time.Second,
		WriteTimeout:  10 * time.Second,
	})
	return startBenchmarkMessageTransport(b, addr, router, func(ctx context.Context, h core.Handler) error {
		return transport.ListenAndServe(ctx, h)
	}, func(ctx context.Context) error {
		return transport.Shutdown(ctx)
	})
}

func startUDPBenchmarkServer(b *testing.B) (string, func()) {
	b.Helper()

	addr := "127.0.0.1:" + freeUDPPort(b)
	router := benchmarkEchoRouter()
	transport := udp.New(udp.Config{
		Addr:          addr,
		Protocol:      protocol.KProV1(),
		MaxPacketSize: 64 * 1024,
	})
	return startBenchmarkMessageTransport(b, addr, router, func(ctx context.Context, h core.Handler) error {
		return transport.ListenAndServe(ctx, h)
	}, func(ctx context.Context) error {
		return transport.Shutdown(ctx)
	})
}

func startWebSocketBenchmarkServer(b *testing.B) (string, func()) {
	b.Helper()

	addr := "127.0.0.1:" + freeTCPPort(b)
	router := benchmarkEchoRouter()
	transport := wstransport.New(wstransport.Config{
		Addr:          addr,
		Path:          "/ws",
		Protocol:      protocol.KProV1(),
		MaxMessage:    64 * 1024,
		SendQueueSize: 1024,
	})
	return startBenchmarkMessageTransport(b, addr, router, func(ctx context.Context, h core.Handler) error {
		return transport.ListenAndServe(ctx, h)
	}, func(ctx context.Context) error {
		return transport.Shutdown(ctx)
	})
}

func benchmarkEchoRouter() *core.Router {
	router := core.NewRouter(core.RouterConfig{
		SyncQueueSize:         4096,
		AsyncWorkers:          1,
		AsyncQueueSize:        4096,
		MaxInflightPerSession: 4096,
		SessionIdleTimeout:    time.Minute,
	})
	router.HandleSync(core.MsgEcho, func(ctx core.Context) error {
		msg := ctx.Message()
		return ctx.Reply(msg.ID, msg.Data)
	})
	return router
}

func startBenchmarkMessageTransport(
	b *testing.B,
	addr string,
	router *core.Router,
	listen func(context.Context, core.Handler) error,
	shutdown func(context.Context) error,
) (string, func()) {
	b.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- listen(ctx, router.Dispatch)
	}()
	waitForTCPOrUDPServer(b, addr)

	stop := func() {
		cancel()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer shutdownCancel()
		_ = shutdown(shutdownCtx)
		_ = router.Shutdown(shutdownCtx)
		select {
		case <-errCh:
		case <-time.After(3 * time.Second):
			b.Fatal("benchmark server did not stop")
		}
	}
	return addr, stop
}

func startHTTPBenchmarkServer(b *testing.B, tlsEnabled bool) (string, func(), *http.Client, string) {
	b.Helper()

	addr := "127.0.0.1:" + freeTCPPort(b)
	config := httpx.Config{
		Addr:            addr,
		ReadTimeout:     10 * time.Second,
		WriteTimeout:    10 * time.Second,
		IdleTimeout:     30 * time.Second,
		ShutdownTimeout: 500 * time.Millisecond,
	}
	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:        100,
			MaxIdleConnsPerHost: 100,
		},
	}
	scheme := "http"
	cleanupCert := func() {}
	if tlsEnabled {
		certFile, keyFile, cleanup, err := benchmarkSelfSignedCert()
		if err != nil {
			b.Fatalf("generate benchmark cert: %v", err)
		}
		cleanupCert = cleanup
		config.TLS = true
		config.CertFile = certFile
		config.KeyFile = keyFile
		scheme = "https"
		client.Transport = &http.Transport{
			TLSClientConfig:     &tls.Config{InsecureSkipVerify: true},
			ForceAttemptHTTP2:   false,
			MaxIdleConns:        100,
			MaxIdleConnsPerHost: 100,
		}
	}

	endpoint := httpx.New(config)
	endpoint.Post("/echo", func(ctx *httpx.Context) error {
		return ctx.CopyBody(http.StatusOK, "text/plain; charset=utf-8")
	})

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- endpoint.ListenAndServe(ctx)
	}()
	if tlsEnabled {
		waitForTLSServer(b, addr)
	} else {
		waitForTCPServer(b, addr)
	}

	stop := func() {
		cancel()
		client.CloseIdleConnections()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer shutdownCancel()
		_ = endpoint.Shutdown(shutdownCtx)
		select {
		case <-errCh:
		case <-time.After(3 * time.Second):
			b.Fatal("HTTP benchmark server did not stop")
		}
		cleanupCert()
	}
	return addr, stop, client, scheme
}

func startSSEBenchmarkServer(b *testing.B, payloadSize int) (string, func(), chan struct{}) {
	b.Helper()

	addr := "127.0.0.1:" + freeTCPPort(b)
	start := make(chan struct{})
	payload := bytes.Repeat([]byte("x"), payloadSize)

	endpoint := sse.New(sse.Config{
		Addr:         addr,
		Path:         "/events",
		WriteTimeout: 10 * time.Second,
	})
	endpoint.Handle(func(ctx *sse.Context) error {
		select {
		case <-start:
		case <-ctx.Done():
			return nil
		}
		for {
			select {
			case <-ctx.Done():
				return nil
			default:
				if err := ctx.Send("message", payload); err != nil {
					return nil
				}
			}
		}
	})

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- endpoint.ListenAndServe(ctx)
	}()
	waitForTCPServer(b, addr)

	stop := func() {
		cancel()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer shutdownCancel()
		_ = endpoint.Shutdown(shutdownCtx)
		select {
		case <-errCh:
		case <-time.After(3 * time.Second):
			b.Fatal("SSE benchmark server did not stop")
		}
	}
	return addr, stop, start
}

func dialTCPBenchmark(b *testing.B, addr string) net.Conn {
	b.Helper()
	deadline := time.Now().Add(3 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			return conn
		}
		lastErr = err
		time.Sleep(20 * time.Millisecond)
	}
	b.Fatalf("dial TCP %s: %v", addr, lastErr)
	return nil
}

func writeBenchmarkMessage(b *testing.B, conn net.Conn, msg core.Message) {
	b.Helper()
	frame, err := protocol.KProV1().Encode(msg)
	if err != nil {
		b.Fatalf("encode message: %v", err)
	}
	if _, err := conn.Write(frame); err != nil {
		b.Fatalf("write message: %v", err)
	}
}

func readBenchmarkMessage(b *testing.B, conn net.Conn) core.Message {
	b.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	result, err := protocol.KProV1().Read(conn, 64*1024)
	if err != nil {
		b.Fatalf("read message: %v", err)
	}
	return result.Message
}

func dialWebSocketBenchmark(b *testing.B, rawURL string) (net.Conn, *bufio.Reader) {
	b.Helper()
	deadline := time.Now().Add(3 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		conn, reader, err := dialBenchmarkRawWebSocket(rawURL)
		if err == nil {
			return conn, reader
		}
		lastErr = err
		time.Sleep(20 * time.Millisecond)
	}
	b.Fatalf("dial websocket %s: %v", rawURL, lastErr)
	return nil, nil
}

func dialBenchmarkRawWebSocket(rawURL string) (net.Conn, *bufio.Reader, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, nil, err
	}
	host := u.Host
	if !strings.Contains(host, ":") {
		host += ":80"
	}
	conn, err := net.DialTimeout("tcp", host, time.Second)
	if err != nil {
		return nil, nil, err
	}
	key, err := benchmarkWebSocketKey()
	if err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	path := u.RequestURI()
	if path == "" {
		path = "/"
	}
	_, err = fmt.Fprintf(conn,
		"GET %s HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: %s\r\n\r\n",
		path,
		u.Host,
		key,
	)
	if err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, nil)
	if err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSwitchingProtocols {
		_ = conn.Close()
		return nil, nil, fmt.Errorf("websocket upgrade failed: %s", resp.Status)
	}
	if resp.Header.Get("Sec-WebSocket-Accept") != benchmarkWebSocketAccept(key) {
		_ = conn.Close()
		return nil, nil, fmt.Errorf("websocket accept key mismatch")
	}
	return conn, reader, nil
}

func writeBenchmarkWebSocketBinary(w io.Writer, payload []byte) error {
	header := []byte{0x82}
	length := len(payload)
	switch {
	case length < 126:
		header = append(header, 0x80|byte(length))
	case length <= 0xffff:
		header = append(header, 0x80|126, 0, 0)
		binary.BigEndian.PutUint16(header[len(header)-2:], uint16(length))
	default:
		header = append(header, 0x80|127, 0, 0, 0, 0, 0, 0, 0, 0)
		binary.BigEndian.PutUint64(header[len(header)-8:], uint64(length))
	}
	var mask [4]byte
	if _, err := rand.Read(mask[:]); err != nil {
		return err
	}
	header = append(header, mask[:]...)
	masked := make([]byte, len(payload))
	for i := range payload {
		masked[i] = payload[i] ^ mask[i%4]
	}
	if _, err := w.Write(header); err != nil {
		return err
	}
	_, err := w.Write(masked)
	return err
}

func readBenchmarkWebSocketBinary(r *bufio.Reader, maxSize int) ([]byte, error) {
	var header [2]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return nil, err
	}
	opcode := header[0] & 0x0f
	if opcode == 0x8 {
		return nil, io.EOF
	}
	masked := header[1]&0x80 != 0
	length := uint64(header[1] & 0x7f)
	switch length {
	case 126:
		var ext [2]byte
		if _, err := io.ReadFull(r, ext[:]); err != nil {
			return nil, err
		}
		length = uint64(binary.BigEndian.Uint16(ext[:]))
	case 127:
		var ext [8]byte
		if _, err := io.ReadFull(r, ext[:]); err != nil {
			return nil, err
		}
		length = binary.BigEndian.Uint64(ext[:])
	}
	if maxSize > 0 && length > uint64(maxSize) {
		return nil, fmt.Errorf("websocket payload too large")
	}
	var mask [4]byte
	if masked {
		if _, err := io.ReadFull(r, mask[:]); err != nil {
			return nil, err
		}
	}
	payload := make([]byte, int(length))
	if length > 0 {
		if _, err := io.ReadFull(r, payload); err != nil {
			return nil, err
		}
	}
	if masked {
		for i := range payload {
			payload[i] ^= mask[i%4]
		}
	}
	if opcode != 0x2 && opcode != 0x1 {
		return nil, fmt.Errorf("unexpected websocket opcode: %d", opcode)
	}
	return payload, nil
}

func benchmarkWebSocketKey() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(b[:]), nil
}

func benchmarkWebSocketAccept(key string) string {
	const magic = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"
	sum := sha1.Sum([]byte(key + magic))
	return base64.StdEncoding.EncodeToString(sum[:])
}

func freeTCPPort(tb testing.TB) string {
	tb.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		tb.Fatalf("listen free TCP port: %v", err)
	}
	defer ln.Close()
	return strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)
}

func freeUDPPort(tb testing.TB) string {
	tb.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		tb.Fatalf("listen free UDP port: %v", err)
	}
	defer conn.Close()
	return strconv.Itoa(conn.LocalAddr().(*net.UDPAddr).Port)
}

func waitForTCPOrUDPServer(tb testing.TB, addr string) {
	tb.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 50*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return
		}
		udpAddr, resolveErr := net.ResolveUDPAddr("udp", addr)
		if resolveErr == nil {
			udpConn, dialErr := net.DialUDP("udp", nil, udpAddr)
			if dialErr == nil {
				_ = udpConn.Close()
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	tb.Fatalf("server did not start on %s", addr)
}

func waitForTCPServer(tb testing.TB, addr string) {
	tb.Helper()
	deadline := time.Now().Add(3 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 50*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return
		}
		lastErr = err
		time.Sleep(20 * time.Millisecond)
	}
	tb.Fatalf("TCP server did not start on %s: %v", addr, lastErr)
}

func waitForTLSServer(tb testing.TB, addr string) {
	tb.Helper()
	deadline := time.Now().Add(3 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		conn, err := tls.DialWithDialer(
			&net.Dialer{Timeout: 50 * time.Millisecond},
			"tcp",
			addr,
			&tls.Config{InsecureSkipVerify: true},
		)
		if err == nil {
			_ = conn.Close()
			return
		}
		lastErr = err
		time.Sleep(20 * time.Millisecond)
	}
	tb.Fatalf("TLS server did not start on %s: %v", addr, lastErr)
}

func payloadName(size int) string {
	return fmt.Sprintf("%dB", size)
}

func benchmarkSelfSignedCert() (string, string, func(), error) {
	dir, err := os.MkdirTemp("", "kpro-benchmark-https-*")
	if err != nil {
		return "", "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		cleanup()
		return "", "", nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		cleanup()
		return "", "", nil, err
	}
	tpl := x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, &tpl, &tpl, &key.PublicKey, key)
	if err != nil {
		cleanup()
		return "", "", nil, err
	}

	certFile := filepath.Join(dir, "cert.pem")
	keyFile := filepath.Join(dir, "key.pem")
	if err := writeBenchmarkPEM(certFile, 0644, &pem.Block{Type: "CERTIFICATE", Bytes: der}); err != nil {
		cleanup()
		return "", "", nil, err
	}
	if err := writeBenchmarkPEM(keyFile, 0600, &pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}); err != nil {
		cleanup()
		return "", "", nil, err
	}
	return certFile, keyFile, cleanup, nil
}

func writeBenchmarkPEM(path string, perm os.FileMode, block *pem.Block) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	defer file.Close()
	return pem.Encode(file, block)
}
