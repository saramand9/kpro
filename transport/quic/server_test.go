package quic

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	quicgo "github.com/quic-go/quic-go"
	"github.com/saramand9/kpro/codec"
	"github.com/saramand9/kpro/core"
	transportapi "github.com/saramand9/kpro/transport"
)

func TestQUICEchoDispatch(t *testing.T) {
	server, addr, stop := startQUICTestServer(t, Config{
		Addr:         freeQUICTestAddr(t),
		Codec:        codec.KProV1(),
		ReadTimeout:  time.Second,
		WriteTimeout: time.Second,
	}, func(ctx core.Context) error {
		if ctx.Transport() != core.TransportQUIC {
			t.Fatalf("transport = %s, want %s", ctx.Transport(), core.TransportQUIC)
		}
		msg := ctx.Message()
		return ctx.Reply(msg.ID, msg.Data)
	})
	defer stop()

	conn := dialQUICTest(t, addr)
	defer conn.CloseWithError(0, "")
	stream := openQUICTestStream(t, conn)
	defer stream.Close()

	want := core.Message{ID: testMsgEcho, Seq: 7, Data: []byte("hello quic")}
	writeQUICTestMessage(t, stream, want)
	got := readQUICTestMessage(t, stream)
	if got.ID != want.ID || got.Seq != want.Seq || string(got.Data) != string(want.Data) {
		t.Fatalf("response = id=%d seq=%d data=%q", got.ID, got.Seq, string(got.Data))
	}
	waitForQUICActiveCount(t, server, 1)
}

func TestQUICHeartbeatBypassesRouter(t *testing.T) {
	var handled atomic.Int32
	_, addr, stop := startQUICTestServer(t, Config{
		Addr:         freeQUICTestAddr(t),
		Codec:        codec.KProV1(),
		ReadTimeout:  time.Second,
		WriteTimeout: time.Second,
	}, func(core.Context) error {
		handled.Add(1)
		return nil
	})
	defer stop()

	conn := dialQUICTest(t, addr)
	defer conn.CloseWithError(0, "")
	stream := openQUICTestStream(t, conn)
	defer stream.Close()

	writeQUICTestMessage(t, stream, core.Message{ID: core.MsgHeartbeat, Seq: 9})
	got := readQUICTestMessage(t, stream)
	if got.ID != core.MsgPong || got.Seq != 9 {
		t.Fatalf("heartbeat response = id=%d seq=%d", got.ID, got.Seq)
	}
	if handled.Load() != 0 {
		t.Fatalf("handler calls = %d, want 0", handled.Load())
	}
}

func TestQUICMultipleStreamsOnOneConnection(t *testing.T) {
	_, addr, stop := startQUICTestServer(t, Config{
		Addr:         freeQUICTestAddr(t),
		Codec:        codec.KProV1(),
		ReadTimeout:  time.Second,
		WriteTimeout: time.Second,
	}, func(ctx core.Context) error {
		msg := ctx.Message()
		return ctx.Reply(msg.ID, msg.Data)
	})
	defer stop()

	conn := dialQUICTest(t, addr)
	defer conn.CloseWithError(0, "")

	const streams = 4
	var wg sync.WaitGroup
	errCh := make(chan error, streams)
	for i := 0; i < streams; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			stream, err := openQUICTestStreamResult(conn)
			if err != nil {
				errCh <- fmt.Errorf("open quic stream: %w", err)
				return
			}
			defer stream.Close()
			want := core.Message{ID: testMsgEcho, Seq: uint64(i + 1), Data: []byte{byte('a' + i)}}
			if err := writeQUICTestMessageResult(stream, want); err != nil {
				errCh <- fmt.Errorf("write quic message: %w", err)
				return
			}
			got, err := readQUICTestMessageResult(stream)
			if err != nil {
				errCh <- fmt.Errorf("read quic message: %w", err)
				return
			}
			if got.ID != want.ID || got.Seq != want.Seq || string(got.Data) != string(want.Data) {
				errCh <- fmt.Errorf("stream response = id=%d seq=%d data=%q", got.ID, got.Seq, string(got.Data))
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestQUICOversizedFrameClosesWithoutDispatch(t *testing.T) {
	var handled atomic.Int32
	server, addr, stop := startQUICTestServer(t, Config{
		Addr:         freeQUICTestAddr(t),
		Codec:        codec.KProV1(),
		MaxFrameSize: 1,
		ReadTimeout:  time.Second,
		WriteTimeout: 50 * time.Millisecond,
	}, func(core.Context) error {
		handled.Add(1)
		return nil
	})
	defer stop()

	conn := dialQUICTest(t, addr)
	defer conn.CloseWithError(0, "")
	stream := openQUICTestStream(t, conn)
	defer stream.Close()

	writeQUICTestMessage(t, stream, core.Message{ID: testMsgEcho, Seq: 1, Data: []byte("xx")})
	assertQUICStreamClosed(t, stream)
	waitForQUICActiveCount(t, server, 0)
	if handled.Load() != 0 {
		t.Fatalf("handler calls = %d, want 0", handled.Load())
	}
}

func TestQUICHandlerErrorIsObservedAndClosesStreamByDefault(t *testing.T) {
	wantErr := errors.New("quic handler failed")
	observed := make(chan transportapi.HandlerErrorEvent, 1)
	server, addr, stop := startQUICTestServer(t, Config{
		Addr:           freeQUICTestAddr(t),
		ReadTimeout:    time.Second,
		WriteTimeout:   time.Second,
		OnHandlerError: func(event transportapi.HandlerErrorEvent) { observed <- event },
	}, func(core.Context) error { return wantErr })
	defer stop()

	conn := dialQUICTest(t, addr)
	defer conn.CloseWithError(0, "")
	stream := openQUICTestStream(t, conn)
	defer stream.Close()
	writeQUICTestMessage(t, stream, core.Message{ID: testMsgEcho, Seq: 19})
	select {
	case event := <-observed:
		if !errors.Is(event.Err, wantErr) || event.Transport != core.TransportQUIC || event.MessageID != testMsgEcho || event.Sequence != 19 {
			t.Fatalf("handler error event = %+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("handler error was not observed")
	}
	assertQUICStreamClosed(t, stream)
	waitForQUICActiveCount(t, server, 0)
}

func TestQUICSendQueueFullReturnsOverloadBeforeClosedState(t *testing.T) {
	server := New(Config{Codec: codec.KProV1()})
	session := &session{server: server, sendQueue: make(chan []byte), done: make(chan struct{})}
	// Prevent this focused unit from dereferencing a real stream while retaining
	// the Send path's close-on-overload behavior.
	session.closeOnce.Do(func() {})
	err := session.Send(core.Message{ID: testMsgEcho})
	if !errors.Is(err, core.ErrSendQueueFull) {
		t.Fatalf("Send() error = %v, want %v", err, core.ErrSendQueueFull)
	}
	if errors.Is(err, core.ErrClosed) {
		t.Fatalf("Send() error = %v, must report overload rather than prior closed state", err)
	}
}

func TestQUICRejectsNilHandlerWithoutBinding(t *testing.T) {
	certFile, keyFile := quicTestSelfSignedCert(t)
	server := New(Config{Addr: freeQUICTestAddr(t), CertFile: certFile, KeyFile: keyFile})
	err := server.ListenAndServe(context.Background(), nil)
	if !errors.Is(err, core.ErrNoHandler) {
		t.Fatalf("ListenAndServe() error = %v, want %v", err, core.ErrNoHandler)
	}
	select {
	case <-server.Ready():
		t.Fatal("Ready() closed after nil handler startup failure")
	default:
	}
	server.lifecycleMu.Lock()
	listener := server.ln
	server.lifecycleMu.Unlock()
	if listener != nil {
		t.Fatal("nil handler startup published a QUIC listener")
	}
}

func TestContextCancellationClosesListenerAndAcceptedConnection(t *testing.T) {
	certFile, keyFile := quicTestSelfSignedCert(t)
	addr := freeQUICTestAddr(t)
	server := New(Config{Addr: addr, CertFile: certFile, KeyFile: keyFile})
	t.Cleanup(func() { _ = server.Shutdown(context.Background()) })
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- server.ListenAndServe(ctx, func(core.Context) error { return nil }) }()
	time.Sleep(50 * time.Millisecond)

	conn := dialQUICTest(t, addr)
	defer conn.CloseWithError(0, "")
	stream := openQUICTestStream(t, conn)
	defer stream.Close()
	deadline := time.Now().Add(time.Second)
	var acceptedConnection *quicgo.Conn
	for {
		server.connections.Range(func(key, _ interface{}) bool {
			acceptedConnection, _ = key.(*quicgo.Conn)
			return false
		})
		if acceptedConnection != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("accepted QUIC connection was not tracked")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("ListenAndServe() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("QUIC server did not stop after context cancellation")
	}
	select {
	case <-acceptedConnection.Context().Done():
	case <-time.After(2 * time.Second):
		t.Fatal("server-side QUIC connection remained open after cancellation")
	}

	releaseDeadline := time.Now().Add(2 * time.Second)
	for {
		listener, err := net.ListenPacket("udp", addr)
		if err == nil {
			_ = listener.Close()
			break
		}
		if time.Now().After(releaseDeadline) {
			t.Fatalf("QUIC listener address remained bound: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestNormalizeConfigKeepsStreamAndConnectionLimitsIndependent(t *testing.T) {
	config := normalizeConfig(Config{MaxStreams: 7, MaxConnections: 3})
	if config.MaxStreams != 7 {
		t.Fatalf("max streams = %d, want 7", config.MaxStreams)
	}
	if config.MaxConnections != 3 {
		t.Fatalf("max connections = %d, want 3", config.MaxConnections)
	}
}

func TestNormalizeConfigDoesNotTreatConnectionsAsStreams(t *testing.T) {
	config := normalizeConfig(Config{MaxConnections: 5})
	if config.MaxStreams != DefaultMaxStreams {
		t.Fatalf("max streams = %d, want default %d", config.MaxStreams, DefaultMaxStreams)
	}
	if config.MaxConnections != 5 {
		t.Fatalf("max connections = %d, want 5", config.MaxConnections)
	}
}

func TestTryAcquireStreamSlotIsAtomic(t *testing.T) {
	server := New(Config{MaxStreams: 1})
	const contenders = 128
	start := make(chan struct{})
	results := make(chan bool, contenders)
	var wg sync.WaitGroup
	for index := 0; index < contenders; index++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			results <- server.tryAcquireStreamSlot()
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	acquired := 0
	for result := range results {
		if result {
			acquired++
		}
	}
	if acquired != 1 {
		t.Fatalf("acquired slots = %d, want 1", acquired)
	}
	if active := atomic.LoadInt64(&server.active); active != 1 {
		t.Fatalf("active streams = %d, want 1", active)
	}
	server.releaseStreamSlot()
	if active := atomic.LoadInt64(&server.active); active != 0 {
		t.Fatalf("active streams after release = %d, want 0", active)
	}
}

func TestTryAcquireConnectionSlotIsAtomic(t *testing.T) {
	server := New(Config{MaxConnections: 1})
	const contenders = 128
	start := make(chan struct{})
	results := make(chan bool, contenders)
	var wg sync.WaitGroup
	for index := 0; index < contenders; index++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			results <- server.tryAcquireConnectionSlot()
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	acquired := 0
	for result := range results {
		if result {
			acquired++
		}
	}
	if acquired != 1 {
		t.Fatalf("acquired connection slots = %d, want 1", acquired)
	}
	if active := atomic.LoadInt64(&server.activeConns); active != 1 {
		t.Fatalf("active connections = %d, want 1", active)
	}
	server.releaseConnectionSlot()
	if active := atomic.LoadInt64(&server.activeConns); active != 0 {
		t.Fatalf("active connections after release = %d, want 0", active)
	}
}

func TestQUICPhysicalConnectionLimitRejectsAndReleases(t *testing.T) {
	server, addr, stop := startQUICTestServer(t, Config{
		Addr:           freeQUICTestAddr(t),
		MaxConnections: 1,
		MaxStreams:     4,
		ReadTimeout:    time.Second,
		WriteTimeout:   time.Second,
	}, func(ctx core.Context) error {
		msg := ctx.Message()
		return ctx.Reply(msg.ID, msg.Data)
	})
	defer stop()

	first := dialQUICTest(t, addr)
	firstStream := openQUICTestStream(t, first)
	waitForQUICConnectionCount(t, server, 1)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	second, err := quicgo.DialAddr(ctx, addr, &tls.Config{
		InsecureSkipVerify: true,
		NextProtos:         []string{DefaultALPN},
	}, &quicgo.Config{HandshakeIdleTimeout: time.Second})
	cancel()
	if err == nil {
		defer second.CloseWithError(0, "")
		streamCtx, streamCancel := context.WithTimeout(context.Background(), time.Second)
		stream, streamErr := second.OpenStreamSync(streamCtx)
		streamCancel()
		if streamErr == nil {
			_ = stream.SetWriteDeadline(time.Now().Add(time.Second))
			_, writeErr := stream.Write([]byte{0x4b, 0x50})
			_ = stream.SetReadDeadline(time.Now().Add(time.Second))
			var one [1]byte
			_, readErr := stream.Read(one[:])
			if writeErr == nil && readErr == nil {
				t.Fatal("second physical QUIC connection remained usable above MaxConnections")
			}
		}
	}
	if active := atomic.LoadInt64(&server.activeConns); active != 1 {
		t.Fatalf("active physical connections = %d, want 1", active)
	}

	_ = firstStream.Close()
	_ = first.CloseWithError(0, "")
	waitForQUICConnectionCount(t, server, 0)

	third := dialQUICTest(t, addr)
	defer third.CloseWithError(0, "")
	thirdStream := openQUICTestStream(t, third)
	defer thirdStream.Close()
	writeQUICTestMessage(t, thirdStream, core.Message{ID: testMsgEcho, Seq: 23, Data: []byte("released")})
	if got := readQUICTestMessage(t, thirdStream); got.Seq != 23 || string(got.Data) != "released" {
		t.Fatalf("response after slot release = %+v", got)
	}
}

func TestReadyStaysOpenOnQUICStartupFailureAndShutdownBeforeStart(t *testing.T) {
	invalid := New(Config{})
	if err := invalid.ListenAndServe(context.Background(), func(core.Context) error { return nil }); err == nil {
		t.Fatal("ListenAndServe() error = nil, want TLS validation error")
	}
	select {
	case <-invalid.Ready():
		t.Fatal("Ready() closed after startup failure")
	default:
	}

	closed := New(Config{})
	if err := closed.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	select {
	case <-closed.Ready():
		t.Fatal("Ready() closed after shutdown-before-start")
	default:
	}
}

func startQUICTestServer(t *testing.T, config Config, handler core.Handler) (*Server, string, func()) {
	t.Helper()

	certFile, keyFile := quicTestSelfSignedCert(t)
	config.CertFile = certFile
	config.KeyFile = keyFile
	server := New(config)
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- server.ListenAndServe(ctx, handler)
	}()
	select {
	case <-server.Ready():
	case <-time.After(time.Second):
		t.Fatal("QUIC Ready() did not close")
	}
	server.lifecycleMu.Lock()
	listener := server.ln
	server.lifecycleMu.Unlock()
	if listener == nil {
		t.Fatal("QUIC Ready() closed without a published listener")
	}

	stop := func() {
		cancel()
		_ = server.Shutdown(context.Background())
		select {
		case err := <-errCh:
			if err != nil {
				t.Fatalf("quic server stopped with error: %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("quic server did not stop")
		}
	}
	return server, config.Addr, stop
}

func freeQUICTestAddr(t *testing.T) string {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatalf("listen free udp port: %v", err)
	}
	defer conn.Close()
	return conn.LocalAddr().String()
}

func dialQUICTest(t *testing.T, addr string) *quicgo.Conn {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	conn, err := quicgo.DialAddr(ctx, addr, &tls.Config{
		InsecureSkipVerify: true,
		NextProtos:         []string{DefaultALPN},
	}, &quicgo.Config{
		HandshakeIdleTimeout: time.Second,
		MaxIdleTimeout:       10 * time.Second,
	})
	if err != nil {
		t.Fatalf("dial quic: %v", err)
	}
	return conn
}

func openQUICTestStream(t *testing.T, conn *quicgo.Conn) *quicgo.Stream {
	t.Helper()

	stream, err := openQUICTestStreamResult(conn)
	if err != nil {
		t.Fatalf("open quic stream: %v", err)
	}
	return stream
}

func openQUICTestStreamResult(conn *quicgo.Conn) (*quicgo.Stream, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	return conn.OpenStreamSync(ctx)
}

func writeQUICTestMessage(t *testing.T, stream *quicgo.Stream, msg core.Message) {
	t.Helper()
	if err := writeQUICTestMessageResult(stream, msg); err != nil {
		t.Fatalf("write quic message: %v", err)
	}
}

func writeQUICTestMessageResult(stream *quicgo.Stream, msg core.Message) error {
	frame, err := codec.KProV1().Encode(msg)
	if err != nil {
		return fmt.Errorf("encode message: %w", err)
	}
	_ = stream.SetWriteDeadline(time.Now().Add(time.Second))
	if _, err := stream.Write(frame); err != nil {
		return err
	}
	return nil
}

func readQUICTestMessage(t *testing.T, stream *quicgo.Stream) core.Message {
	t.Helper()
	msg, err := readQUICTestMessageResult(stream)
	if err != nil {
		t.Fatalf("read quic message: %v", err)
	}
	return msg
}

func readQUICTestMessageResult(stream *quicgo.Stream) (core.Message, error) {
	_ = stream.SetReadDeadline(time.Now().Add(time.Second))
	result, err := codec.KProV1().Read(stream, 64*1024)
	if err != nil {
		return core.Message{}, err
	}
	return result.Message, nil
}

func assertQUICStreamClosed(t *testing.T, stream *quicgo.Stream) {
	t.Helper()
	_ = stream.SetReadDeadline(time.Now().Add(time.Second))
	var buf [1]byte
	if n, err := stream.Read(buf[:]); err == nil {
		t.Fatalf("read %d bytes from quic stream, want close/error", n)
	}
}

func waitForQUICActiveCount(t *testing.T, server *Server, want int64) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if got := atomic.LoadInt64(&server.active); got == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("active quic sessions = %d, want %d", atomic.LoadInt64(&server.active), want)
}

func waitForQUICConnectionCount(t *testing.T, server *Server, want int64) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if got := atomic.LoadInt64(&server.activeConns); got == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("active quic connections = %d, want %d", atomic.LoadInt64(&server.activeConns), want)
}

func quicTestSelfSignedCert(t *testing.T) (string, string) {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatalf("generate serial: %v", err)
	}
	tpl := x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, &tpl, &tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}

	dir := t.TempDir()
	certFile := filepath.Join(dir, "cert.pem")
	keyFile := filepath.Join(dir, "key.pem")
	writeQUICTestPEM(t, certFile, 0644, &pem.Block{Type: "CERTIFICATE", Bytes: der})
	writeQUICTestPEM(t, keyFile, 0600, &pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	return certFile, keyFile
}

func writeQUICTestPEM(t *testing.T, path string, perm os.FileMode, block *pem.Block) {
	t.Helper()

	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		t.Fatalf("open pem file: %v", err)
	}
	defer file.Close()
	if err := pem.Encode(file, block); err != nil {
		t.Fatalf("write pem file: %v", err)
	}
}
