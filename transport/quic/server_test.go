package quic

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
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
	"github.com/saramand9/bignet/kpro/codec"
	"github.com/saramand9/bignet/kpro/core"
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
	var acceptedConnection quicgo.Connection
	for {
		server.connections.Range(func(key, _ interface{}) bool {
			acceptedConnection, _ = key.(quicgo.Connection)
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

	listener, err := net.ListenPacket("udp", addr)
	if err != nil {
		t.Fatalf("QUIC listener address remained bound: %v", err)
	}
	_ = listener.Close()
}

func TestNormalizeConfigUsesMaxStreams(t *testing.T) {
	config := normalizeConfig(Config{MaxStreams: 7, MaxConnections: 3})
	if config.MaxStreams != 7 {
		t.Fatalf("max streams = %d, want 7", config.MaxStreams)
	}
	if config.MaxConnections != 7 {
		t.Fatalf("compat max connections = %d, want mirrored max streams 7", config.MaxConnections)
	}
}

func TestNormalizeConfigKeepsMaxConnectionsCompatibility(t *testing.T) {
	config := normalizeConfig(Config{MaxConnections: 5})
	if config.MaxStreams != 5 {
		t.Fatalf("max streams = %d, want compat max connections 5", config.MaxStreams)
	}
	if config.MaxConnections != 5 {
		t.Fatalf("compat max connections = %d, want 5", config.MaxConnections)
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
	time.Sleep(50 * time.Millisecond)

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

func dialQUICTest(t *testing.T, addr string) quicgo.Connection {
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

func openQUICTestStream(t *testing.T, conn quicgo.Connection) quicgo.Stream {
	t.Helper()

	stream, err := openQUICTestStreamResult(conn)
	if err != nil {
		t.Fatalf("open quic stream: %v", err)
	}
	return stream
}

func openQUICTestStreamResult(conn quicgo.Connection) (quicgo.Stream, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	return conn.OpenStreamSync(ctx)
}

func writeQUICTestMessage(t *testing.T, stream quicgo.Stream, msg core.Message) {
	t.Helper()
	if err := writeQUICTestMessageResult(stream, msg); err != nil {
		t.Fatalf("write quic message: %v", err)
	}
}

func writeQUICTestMessageResult(stream quicgo.Stream, msg core.Message) error {
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

func readQUICTestMessage(t *testing.T, stream quicgo.Stream) core.Message {
	t.Helper()
	msg, err := readQUICTestMessageResult(stream)
	if err != nil {
		t.Fatalf("read quic message: %v", err)
	}
	return msg
}

func readQUICTestMessageResult(stream quicgo.Stream) (core.Message, error) {
	_ = stream.SetReadDeadline(time.Now().Add(time.Second))
	result, err := codec.KProV1().Read(stream, 64*1024)
	if err != nil {
		return core.Message{}, err
	}
	return result.Message, nil
}

func assertQUICStreamClosed(t *testing.T, stream quicgo.Stream) {
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
