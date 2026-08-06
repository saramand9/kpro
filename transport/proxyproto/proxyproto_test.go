package proxyproto

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"
)

func TestConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		config  Config
		wantErr string
	}{
		{name: "disabled zero value", config: Config{}},
		{
			name: "disabled ignores inactive trust settings",
			config: Config{
				TrustedProxies: []string{"not-an-ip"},
				HeaderTimeout:  -time.Second,
			},
		},
		{
			name:    "enabled requires trust",
			config:  Config{Enabled: true},
			wantErr: "TrustedProxies",
		},
		{
			name: "exact and prefix",
			config: Config{
				Enabled:        true,
				TrustedProxies: []string{"127.0.0.1", "2001:db8::/32"},
			},
		},
		{
			name: "invalid trust entry",
			config: Config{
				Enabled:        true,
				TrustedProxies: []string{"load-balancer.internal"},
			},
			wantErr: "TrustedProxies[0]",
		},
		{
			name: "IPv6 zone is not a trust identity",
			config: Config{
				Enabled:        true,
				TrustedProxies: []string{"fe80::1%ethernet"},
			},
			wantErr: "IPv6 zone",
		},
		{
			name: "negative timeout",
			config: Config{
				Enabled:        true,
				TrustedProxies: []string{"127.0.0.1"},
				HeaderTimeout:  -time.Second,
			},
			wantErr: "HeaderTimeout",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.config.Validate()
			if test.wantErr == "" && err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
			if test.wantErr != "" && (err == nil || !strings.Contains(err.Error(), test.wantErr)) {
				t.Fatalf("Validate() error = %v, want containing %q", err, test.wantErr)
			}
		})
	}
}

func TestWrapListenerDisabledReturnsOriginalListener(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()
	wrapped, err := WrapListener(listener, Config{})
	if err != nil {
		t.Fatalf("WrapListener: %v", err)
	}
	if wrapped != listener {
		t.Fatal("disabled configuration replaced the listener")
	}
}

func TestParseV1PreservesBufferedPayload(t *testing.T) {
	input := "PROXY TCP4 203.0.113.10 198.51.100.20 45678 8443\r\npayload"
	reader := bufio.NewReaderSize(&oneByteReader{reader: strings.NewReader(input)}, maxV1HeaderBytes)
	header, err := parseHeader(reader)
	if err != nil {
		t.Fatalf("parseHeader: %v", err)
	}
	if header.Version != 1 || header.Command != CommandProxy || header.Protocol != "TCP4" {
		t.Fatalf("header identity = %+v", header)
	}
	if got, want := header.Source, netip.MustParseAddrPort("203.0.113.10:45678"); got != want {
		t.Fatalf("source = %v, want %v", got, want)
	}
	if got, want := header.Destination, netip.MustParseAddrPort("198.51.100.20:8443"); got != want {
		t.Fatalf("destination = %v, want %v", got, want)
	}
	payload, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read payload: %v", err)
	}
	if string(payload) != "payload" {
		t.Fatalf("payload = %q, want payload", payload)
	}
}

func TestParseV1TCP6AndUnknown(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		command  Command
		protocol string
		source   netip.AddrPort
	}{
		{
			name:     "tcp6",
			input:    "PROXY TCP6 2001:db8::10 2001:db8::20 1234 443\r\n",
			command:  CommandProxy,
			protocol: "TCP6",
			source:   netip.MustParseAddrPort("[2001:db8::10]:1234"),
		},
		{
			name:     "unknown",
			input:    "PROXY UNKNOWN ignored fields\r\n",
			command:  CommandUnknown,
			protocol: "UNKNOWN",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			header, err := parseHeader(bufio.NewReaderSize(strings.NewReader(test.input), maxV1HeaderBytes))
			if err != nil {
				t.Fatalf("parseHeader: %v", err)
			}
			if header.Command != test.command || header.Protocol != test.protocol {
				t.Fatalf("header = %+v", header)
			}
			if test.source.IsValid() && header.Source != test.source {
				t.Fatalf("source = %v, want %v", header.Source, test.source)
			}
		})
	}
}

func TestParseV1LineLengthBoundary(t *testing.T) {
	prefix := "PROXY UNKNOWN "
	for _, test := range []struct {
		name    string
		length  int
		wantErr bool
	}{
		{name: "107 bytes", length: 107},
		{name: "108 bytes", length: 108, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			line := prefix + strings.Repeat("x", test.length-len(prefix)-2) + "\r\n"
			_, err := parseHeader(bufio.NewReaderSize(strings.NewReader(line), maxV1HeaderBytes))
			if test.wantErr && !errors.Is(err, ErrMalformedHeader) {
				t.Fatalf("parseHeader() error = %v, want malformed header", err)
			}
			if !test.wantErr && err != nil {
				t.Fatalf("parseHeader() error = %v", err)
			}
		})
	}
}

func TestParseV2AddressesTLVsAndPayload(t *testing.T) {
	tests := []struct {
		name        string
		header      []byte
		protocol    string
		source      netip.AddrPort
		destination netip.AddrPort
		tlvBytes    int
	}{
		{
			name:        "tcp4",
			header:      v2TCP4("203.0.113.10", "198.51.100.20", 45678, 8443, []byte{0x01, 0x00, 0x01, 0x7f}),
			protocol:    "TCP4",
			source:      netip.MustParseAddrPort("203.0.113.10:45678"),
			destination: netip.MustParseAddrPort("198.51.100.20:8443"),
			tlvBytes:    4,
		},
		{
			name:        "tcp6",
			header:      v2TCP6("2001:db8::10", "2001:db8::20", 1234, 443),
			protocol:    "TCP6",
			source:      netip.MustParseAddrPort("[2001:db8::10]:1234"),
			destination: netip.MustParseAddrPort("[2001:db8::20]:443"),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := append(append([]byte(nil), test.header...), []byte("payload")...)
			reader := bufio.NewReaderSize(&oneByteReader{reader: bytes.NewReader(input)}, maxV1HeaderBytes)
			header, err := parseHeader(reader)
			if err != nil {
				t.Fatalf("parseHeader: %v", err)
			}
			if header.Version != 2 || header.Command != CommandProxy || header.Protocol != test.protocol {
				t.Fatalf("header identity = %+v", header)
			}
			if header.Source != test.source || header.Destination != test.destination {
				t.Fatalf("addresses = %v -> %v, want %v -> %v", header.Source, header.Destination, test.source, test.destination)
			}
			if header.TLVBytes != test.tlvBytes {
				t.Fatalf("TLVBytes = %d, want %d", header.TLVBytes, test.tlvBytes)
			}
			payload, err := io.ReadAll(reader)
			if err != nil || string(payload) != "payload" {
				t.Fatalf("payload = %q, err = %v", payload, err)
			}
		})
	}
}

func TestParseV2LocalAndUnspec(t *testing.T) {
	tests := []struct {
		name     string
		header   []byte
		command  Command
		protocol string
	}{
		{name: "local", header: v2Raw(0x20, 0x31, []byte("ignored")), command: CommandLocal, protocol: "LOCAL"},
		{name: "unspec", header: v2Raw(0x21, 0x00, []byte("ignored")), command: CommandProxy, protocol: "UNSPEC"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			header, err := parseHeader(bufio.NewReaderSize(bytes.NewReader(test.header), maxV1HeaderBytes))
			if err != nil {
				t.Fatalf("parseHeader: %v", err)
			}
			if header.Command != test.command || header.Protocol != test.protocol {
				t.Fatalf("header = %+v", header)
			}
		})
	}
}

func TestParseHeaderRejectsInvalidInput(t *testing.T) {
	tooLong := "PROXY UNKNOWN " + strings.Repeat("x", maxV1HeaderBytes) + "\r\n"
	tests := []struct {
		name string
		data []byte
		want error
	}{
		{name: "missing", data: []byte("GET / HTTP/1.1\r\n"), want: ErrHeaderRequired},
		{name: "v1 invalid separator", data: []byte("PROXY\tUNKNOWN\r\n"), want: ErrMalformedHeader},
		{name: "v1 no CRLF", data: []byte("PROXY UNKNOWN\n"), want: ErrMalformedHeader},
		{name: "v1 too long", data: []byte(tooLong), want: ErrMalformedHeader},
		{name: "v1 wrong family", data: []byte("PROXY TCP4 2001:db8::1 192.0.2.1 1 2\r\n"), want: ErrMalformedHeader},
		{name: "v1 IPv6 zone", data: []byte("PROXY TCP6 fe80::1%eth0 2001:db8::1 1 2\r\n"), want: ErrMalformedHeader},
		{name: "v1 invalid port", data: []byte("PROXY TCP4 192.0.2.1 192.0.2.2 65536 2\r\n"), want: ErrMalformedHeader},
		{name: "v1 leading zero port", data: []byte("PROXY TCP4 192.0.2.1 192.0.2.2 0123 2\r\n"), want: ErrMalformedHeader},
		{name: "v1 plus port", data: []byte("PROXY TCP4 192.0.2.1 192.0.2.2 +123 2\r\n"), want: ErrMalformedHeader},
		{name: "v2 invalid command", data: v2Raw(0x22, 0x11, nil), want: ErrMalformedHeader},
		{name: "v2 reserved protocol", data: v2Raw(0x21, 0x13, nil), want: ErrUnsupportedHeader},
		{name: "v2 reserved family", data: v2Raw(0x21, 0x41, nil), want: ErrUnsupportedHeader},
		{name: "v2 short tcp4", data: v2Raw(0x21, 0x11, make([]byte, 11)), want: ErrMalformedHeader},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseHeader(bufio.NewReaderSize(bytes.NewReader(test.data), maxV1HeaderBytes))
			if !errors.Is(err, test.want) {
				t.Fatalf("parseHeader() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestWriteBuffersBypassesProxyReadWrapper(t *testing.T) {
	underlying := &writeRecordingConn{}
	connection := &Conn{Conn: underlying}
	if got := writeConnection(connection); got != underlying {
		t.Fatalf("writeConnection() = %T, want underlying connection", got)
	}
	buffers := net.Buffers{[]byte("first"), []byte("second")}
	written, err := WriteBuffers(&buffers, connection)
	if err != nil {
		t.Fatalf("WriteBuffers: %v", err)
	}
	if got, want := written, int64(len("firstsecond")); got != want {
		t.Fatalf("written = %d, want %d", got, want)
	}
	if got, want := underlying.writes, 2; got != want {
		t.Fatalf("underlying writes = %d, want %d", got, want)
	}
	if got, want := underlying.payload.String(), "firstsecond"; got != want {
		t.Fatalf("payload = %q, want %q", got, want)
	}
}

func TestParseV2UnsupportedValidProtocolsFallBackToUnspec(t *testing.T) {
	tests := []struct {
		name           string
		familyProtocol byte
		addressBytes   int
	}{
		{name: "udp4", familyProtocol: 0x12, addressBytes: 12},
		{name: "udp6", familyProtocol: 0x22, addressBytes: 36},
		{name: "unix stream", familyProtocol: 0x31, addressBytes: 216},
		{name: "unix datagram", familyProtocol: 0x32, addressBytes: 216},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			header, err := parseHeader(bufio.NewReaderSize(
				bytes.NewReader(v2Raw(0x21, test.familyProtocol, make([]byte, test.addressBytes))),
				maxV1HeaderBytes,
			))
			if err != nil {
				t.Fatalf("parseHeader: %v", err)
			}
			if header.Version != 2 || header.Command != CommandProxy || header.Protocol != "UNSPEC" || header.Source.IsValid() {
				t.Fatalf("header = %+v", header)
			}
		})
	}
}

func TestWrappedListenerUsesAdvertisedAddressesAndPreservesPayload(t *testing.T) {
	base, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	listener, err := WrapListener(base, Config{
		Enabled:        true,
		TrustedProxies: []string{"127.0.0.1/32"},
	})
	if err != nil {
		t.Fatalf("WrapListener: %v", err)
	}
	defer listener.Close()

	accepted := make(chan net.Conn, 1)
	acceptErr := make(chan error, 1)
	go func() {
		connection, err := listener.Accept()
		if err != nil {
			acceptErr <- err
			return
		}
		accepted <- connection
	}()

	client, err := net.Dial("tcp", base.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()
	if _, err := client.Write([]byte("PROXY TCP4 203.0.113.10 198.51.100.20 45678 8443\r\npayload")); err != nil {
		t.Fatalf("write: %v", err)
	}

	var connection net.Conn
	select {
	case connection = <-accepted:
	case err := <-acceptErr:
		t.Fatalf("Accept: %v", err)
	case <-time.After(time.Second):
		t.Fatal("Accept did not return")
	}
	defer connection.Close()
	if got, want := connection.RemoteAddr().String(), "203.0.113.10:45678"; got != want {
		t.Fatalf("RemoteAddr = %q, want %q", got, want)
	}
	if got, want := connection.LocalAddr().String(), "198.51.100.20:8443"; got != want {
		t.Fatalf("LocalAddr = %q, want %q", got, want)
	}
	payload := make([]byte, len("payload"))
	if _, err := io.ReadFull(connection, payload); err != nil {
		t.Fatalf("read payload: %v", err)
	}
	if string(payload) != "payload" {
		t.Fatalf("payload = %q", payload)
	}

	info, ok := InfoFrom(connection.RemoteAddr())
	if !ok {
		t.Fatal("InfoFrom(RemoteAddr) did not find metadata")
	}
	if info.Header.Version != 1 || !strings.HasPrefix(info.DirectRemoteAddr, "127.0.0.1:") {
		t.Fatalf("info = %+v", info)
	}
	ctx := WithInfo(context.Background(), info)
	if fromContext, ok := FromContext(ctx); !ok || fromContext != info {
		t.Fatalf("FromContext = %+v, %v; want %+v, true", fromContext, ok, info)
	}
}

func TestWrappedListenerParsesHeaderBeforeHTTP2TLS(t *testing.T) {
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = io.WriteString(writer, request.Proto+" "+request.RemoteAddr)
	}))
	wrapped, err := WrapListener(server.Listener, Config{
		Enabled:        true,
		TrustedProxies: []string{"127.0.0.1"},
	})
	if err != nil {
		t.Fatalf("WrapListener: %v", err)
	}
	server.Listener = wrapped
	server.EnableHTTP2 = true
	server.StartTLS()
	defer server.Close()

	transport := server.Client().Transport.(*http.Transport).Clone()
	dialer := new(net.Dialer)
	transport.DialContext = func(ctx context.Context, network string, address string) (net.Conn, error) {
		connection, err := dialer.DialContext(ctx, network, address)
		if err != nil {
			return nil, err
		}
		if _, err := io.WriteString(connection, "PROXY TCP4 203.0.113.15 198.51.100.25 45683 443\r\n"); err != nil {
			_ = connection.Close()
			return nil, err
		}
		return connection, nil
	}
	transport.ForceAttemptHTTP2 = true
	client := &http.Client{Transport: transport, Timeout: time.Second}
	response, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("HTTP/2 request: %v", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if got, want := string(body), "HTTP/2.0 203.0.113.15:45683"; got != want {
		t.Fatalf("response = %q, want %q", got, want)
	}
}

func TestWrappedListenerRejectsUntrustedPeerWithoutStoppingListener(t *testing.T) {
	base, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	rejected := make(chan RejectEvent, 1)
	listener, err := WrapListener(base, Config{
		Enabled:        true,
		TrustedProxies: []string{"192.0.2.0/24"},
		OnReject: func(event RejectEvent) {
			rejected <- event
		},
	})
	if err != nil {
		t.Fatalf("WrapListener: %v", err)
	}
	acceptErr := make(chan error, 1)
	go func() {
		_, err := listener.Accept()
		acceptErr <- err
	}()

	client, err := net.Dial("tcp", base.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	_ = client.Close()
	select {
	case event := <-rejected:
		if !errors.Is(event.Err, ErrUntrustedProxy) {
			t.Fatalf("reject error = %v, want %v", event.Err, ErrUntrustedProxy)
		}
		if !strings.HasPrefix(event.DirectRemoteAddr, "127.0.0.1:") {
			t.Fatalf("direct remote = %q", event.DirectRemoteAddr)
		}
	case <-time.After(time.Second):
		t.Fatal("untrusted connection was not rejected")
	}
	if err := listener.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case err := <-acceptErr:
		if !errors.Is(err, net.ErrClosed) {
			t.Fatalf("Accept error = %v, want net.ErrClosed", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Accept remained blocked after Close")
	}
}

func TestWrappedListenerSkipsMalformedConnection(t *testing.T) {
	base, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	rejected := make(chan RejectEvent, 1)
	listener, err := WrapListener(base, Config{
		Enabled:        true,
		TrustedProxies: []string{"127.0.0.1"},
		OnReject: func(event RejectEvent) {
			rejected <- event
		},
	})
	if err != nil {
		t.Fatalf("WrapListener: %v", err)
	}
	defer listener.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		connection, _ := listener.Accept()
		accepted <- connection
	}()

	bad, err := net.Dial("tcp", base.Addr().String())
	if err != nil {
		t.Fatalf("dial malformed: %v", err)
	}
	_, _ = bad.Write([]byte("GET / HTTP/1.1\r\n"))
	_ = bad.Close()
	select {
	case event := <-rejected:
		if !errors.Is(event.Err, ErrHeaderRequired) {
			t.Fatalf("reject error = %v, want %v", event.Err, ErrHeaderRequired)
		}
	case <-time.After(time.Second):
		t.Fatal("malformed connection was not rejected")
	}

	good, err := net.Dial("tcp", base.Addr().String())
	if err != nil {
		t.Fatalf("dial valid: %v", err)
	}
	defer good.Close()
	_, _ = good.Write([]byte("PROXY UNKNOWN\r\n"))
	select {
	case connection := <-accepted:
		if connection == nil {
			t.Fatal("valid connection was not accepted")
		}
		_ = connection.Close()
	case <-time.After(time.Second):
		t.Fatal("listener stopped after malformed connection")
	}
}

func TestWrappedListenerClassifiesHeaderTimeout(t *testing.T) {
	base, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	rejected := make(chan RejectEvent, 1)
	listener, err := WrapListener(base, Config{
		Enabled:        true,
		TrustedProxies: []string{"127.0.0.1"},
		HeaderTimeout:  20 * time.Millisecond,
		OnReject: func(event RejectEvent) {
			rejected <- event
		},
	})
	if err != nil {
		t.Fatalf("WrapListener: %v", err)
	}
	defer listener.Close()
	acceptErr := make(chan error, 1)
	go func() {
		_, err := listener.Accept()
		acceptErr <- err
	}()
	client, err := net.Dial("tcp", base.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()
	if _, err := client.Write([]byte("PROXY")); err != nil {
		t.Fatalf("write partial header: %v", err)
	}
	select {
	case event := <-rejected:
		if !errors.Is(event.Err, ErrHeaderTimeout) {
			t.Fatalf("reject error = %v, want %v", event.Err, ErrHeaderTimeout)
		}
	case <-time.After(time.Second):
		t.Fatal("header timeout was not observed")
	}
	if err := listener.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case err := <-acceptErr:
		if !errors.Is(err, net.ErrClosed) {
			t.Fatalf("Accept error = %v, want net.ErrClosed", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Accept remained blocked after Close")
	}
}

func TestListenerCloseInterruptsHeaderRead(t *testing.T) {
	base, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	listener, err := WrapListener(base, Config{
		Enabled:        true,
		TrustedProxies: []string{"127.0.0.1"},
		HeaderTimeout:  time.Minute,
	})
	if err != nil {
		t.Fatalf("WrapListener: %v", err)
	}
	acceptErr := make(chan error, 1)
	go func() {
		_, err := listener.Accept()
		acceptErr <- err
	}()
	client, err := net.Dial("tcp", base.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()

	deadline := time.Now().Add(time.Second)
	for {
		wrapped := listener.(*Listener)
		wrapped.mu.Lock()
		parsing := len(wrapped.parsing)
		wrapped.mu.Unlock()
		if parsing > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("listener did not begin parsing")
		}
		time.Sleep(time.Millisecond)
	}
	if err := listener.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case err := <-acceptErr:
		if !errors.Is(err, net.ErrClosed) {
			t.Fatalf("Accept error = %v, want net.ErrClosed", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Accept remained blocked after Close")
	}
}

func FuzzParseHeader(f *testing.F) {
	f.Add([]byte("PROXY UNKNOWN\r\n"))
	f.Add([]byte("PROXY TCP4 192.0.2.1 192.0.2.2 1234 443\r\n"))
	f.Add(v2TCP4("192.0.2.1", "192.0.2.2", 1234, 443, nil))
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = parseHeader(bufio.NewReaderSize(bytes.NewReader(data), maxV1HeaderBytes))
	})
}

type oneByteReader struct {
	reader io.Reader
}

func (r *oneByteReader) Read(buffer []byte) (int, error) {
	if len(buffer) > 1 {
		buffer = buffer[:1]
	}
	return r.reader.Read(buffer)
}

type writeRecordingConn struct {
	writes  int
	payload bytes.Buffer
}

func (c *writeRecordingConn) Read([]byte) (int, error) { return 0, io.EOF }
func (c *writeRecordingConn) Write(payload []byte) (int, error) {
	c.writes++
	return c.payload.Write(payload)
}
func (c *writeRecordingConn) Close() error                     { return nil }
func (c *writeRecordingConn) LocalAddr() net.Addr              { return nil }
func (c *writeRecordingConn) RemoteAddr() net.Addr             { return nil }
func (c *writeRecordingConn) SetDeadline(time.Time) error      { return nil }
func (c *writeRecordingConn) SetReadDeadline(time.Time) error  { return nil }
func (c *writeRecordingConn) SetWriteDeadline(time.Time) error { return nil }

func v2TCP4(source string, destination string, sourcePort uint16, destinationPort uint16, tlvs []byte) []byte {
	sourceAddress := netip.MustParseAddr(source).As4()
	destinationAddress := netip.MustParseAddr(destination).As4()
	payload := make([]byte, 12, 12+len(tlvs))
	copy(payload[0:4], sourceAddress[:])
	copy(payload[4:8], destinationAddress[:])
	binary.BigEndian.PutUint16(payload[8:10], sourcePort)
	binary.BigEndian.PutUint16(payload[10:12], destinationPort)
	payload = append(payload, tlvs...)
	return v2Raw(0x21, 0x11, payload)
}

func v2TCP6(source string, destination string, sourcePort uint16, destinationPort uint16) []byte {
	sourceAddress := netip.MustParseAddr(source).As16()
	destinationAddress := netip.MustParseAddr(destination).As16()
	payload := make([]byte, 36)
	copy(payload[0:16], sourceAddress[:])
	copy(payload[16:32], destinationAddress[:])
	binary.BigEndian.PutUint16(payload[32:34], sourcePort)
	binary.BigEndian.PutUint16(payload[34:36], destinationPort)
	return v2Raw(0x21, 0x21, payload)
}

func v2Raw(versionCommand byte, familyProtocol byte, payload []byte) []byte {
	header := make([]byte, 16, 16+len(payload))
	copy(header[:12], v2Signature)
	header[12] = versionCommand
	header[13] = familyProtocol
	binary.BigEndian.PutUint16(header[14:16], uint16(len(payload)))
	return append(header, payload...)
}
