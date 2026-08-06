// Package proxyproto implements the HAProxy PROXY protocol for TCP listeners.
// It is disabled unless a transport explicitly enables it and configures
// trusted proxy addresses.
package proxyproto

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// DefaultHeaderTimeout bounds the time a trusted proxy may spend sending a
	// complete PROXY protocol header.
	DefaultHeaderTimeout = 5 * time.Second
	maxV1HeaderBytes     = 107
)

var (
	// ErrHeaderRequired reports a connection that did not start with a PROXY
	// protocol v1 or v2 header while the feature was enabled.
	ErrHeaderRequired = errors.New("proxy protocol header required")
	// ErrHeaderTimeout reports a trusted peer that did not complete its header
	// within Config.HeaderTimeout.
	ErrHeaderTimeout = errors.New("proxy protocol header timeout")
	// ErrMalformedHeader reports a syntactically invalid PROXY protocol header.
	ErrMalformedHeader = errors.New("malformed proxy protocol header")
	// ErrUnsupportedHeader reports an unassigned v2 family or protocol
	// combination. Valid datagram and Unix combinations fall back to UNSPEC.
	ErrUnsupportedHeader = errors.New("unsupported proxy protocol header")
	// ErrUntrustedProxy reports a direct peer outside TrustedProxies.
	ErrUntrustedProxy = errors.New("untrusted proxy protocol peer")
)

var v2Signature = []byte("\r\n\r\n\x00\r\nQUIT\n")

// Command identifies how a header should affect the connection addresses.
type Command string

const (
	CommandProxy   Command = "PROXY"
	CommandLocal   Command = "LOCAL"
	CommandUnknown Command = "UNKNOWN"
)

// Header is the validated, allocation-bounded view of a PROXY protocol header.
// TLV values are deliberately not retained; TLVBytes records only their size.
type Header struct {
	Version     uint8
	Command     Command
	Protocol    string
	Source      netip.AddrPort
	Destination netip.AddrPort
	TLVBytes    int
}

// Info preserves both the advertised addresses and the actual TCP peer used
// to establish the connection.
type Info struct {
	Header           Header
	DirectRemoteAddr string
	DirectLocalAddr  string
}

// RejectEvent describes a connection rejected before it reached a transport.
// Header bytes and application payloads are never included.
type RejectEvent struct {
	DirectRemoteAddr string
	Err              error
}

// Config controls PROXY protocol parsing for one TCP listener.
type Config struct {
	// Enabled requires every accepted connection to start with a v1 or v2
	// header. The zero value leaves the listener unchanged.
	Enabled bool
	// TrustedProxies contains exact IP addresses or CIDR prefixes. Hostnames are
	// intentionally unsupported so validation remains deterministic.
	TrustedProxies []string
	// HeaderTimeout bounds header parsing. Zero uses DefaultHeaderTimeout.
	HeaderTimeout time.Duration
	// OnReject observes rejected connections synchronously. It must return
	// quickly and must not perform blocking telemetry work.
	OnReject func(RejectEvent)
}

// Validate checks trust and timeout settings without binding or resolving a
// network address.
func (c Config) Validate() error {
	if !c.Enabled {
		return nil
	}
	var validationErr error
	if len(c.TrustedProxies) == 0 {
		validationErr = errors.Join(validationErr, errors.New("proxy protocol TrustedProxies is required when enabled"))
	}
	if c.HeaderTimeout < 0 {
		validationErr = errors.Join(validationErr, errors.New("proxy protocol HeaderTimeout must not be negative"))
	}
	for index, value := range c.TrustedProxies {
		if _, err := parseTrustedPrefix(value); err != nil {
			validationErr = errors.Join(validationErr, fmt.Errorf(
				"proxy protocol TrustedProxies[%d]: %w",
				index,
				err,
			))
		}
	}
	return validationErr
}

type compiledConfig struct {
	headerTimeout time.Duration
	trusted       []netip.Prefix
	onReject      func(RejectEvent)
}

func compileConfig(config Config) (compiledConfig, error) {
	if err := config.Validate(); err != nil {
		return compiledConfig{}, err
	}
	compiled := compiledConfig{
		headerTimeout: config.HeaderTimeout,
		trusted:       make([]netip.Prefix, 0, len(config.TrustedProxies)),
		onReject:      config.OnReject,
	}
	if compiled.headerTimeout == 0 {
		compiled.headerTimeout = DefaultHeaderTimeout
	}
	for _, value := range config.TrustedProxies {
		prefix, _ := parseTrustedPrefix(value)
		compiled.trusted = append(compiled.trusted, prefix)
	}
	return compiled, nil
}

func parseTrustedPrefix(value string) (netip.Prefix, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return netip.Prefix{}, errors.New("IP address or CIDR prefix is empty")
	}
	if strings.Contains(value, "/") {
		prefix, err := netip.ParsePrefix(value)
		if err != nil {
			return netip.Prefix{}, errors.New("must be an IP address or CIDR prefix")
		}
		return prefix.Masked(), nil
	}
	address, err := netip.ParseAddr(value)
	if err != nil {
		return netip.Prefix{}, errors.New("must be an IP address or CIDR prefix")
	}
	if address.Zone() != "" {
		return netip.Prefix{}, errors.New("must not contain an IPv6 zone")
	}
	return netip.PrefixFrom(address, address.BitLen()), nil
}

// WrapListener applies mandatory PROXY protocol parsing when config.Enabled is
// true. Disabled configuration returns listener unchanged.
func WrapListener(listener net.Listener, config Config) (net.Listener, error) {
	if listener == nil {
		return nil, errors.New("proxy protocol listener is nil")
	}
	if !config.Enabled {
		return listener, nil
	}
	compiled, err := compileConfig(config)
	if err != nil {
		return nil, err
	}
	return &Listener{
		listener: listener,
		config:   compiled,
		parsing:  make(map[net.Conn]struct{}),
	}, nil
}

// Listener parses a mandatory header before returning each trusted connection.
// Invalid connections are closed and skipped without terminating the server.
type Listener struct {
	listener net.Listener
	config   compiledConfig

	mu        sync.Mutex
	parsing   map[net.Conn]struct{}
	closed    bool
	closeErr  error
	closeOnce sync.Once
}

// Accept returns the next trusted connection with its effective addresses
// replaced by the validated PROXY header values.
func (l *Listener) Accept() (net.Conn, error) {
	for {
		raw, err := l.listener.Accept()
		if err != nil {
			return nil, err
		}
		if !l.beginParse(raw) {
			_ = raw.Close()
			return nil, net.ErrClosed
		}

		directRemote := raw.RemoteAddr()
		connection, parseErr := l.parseConnection(raw)
		open := l.endParse(raw)
		if parseErr == nil && open {
			return connection, nil
		}
		_ = raw.Close()
		if !open {
			return nil, net.ErrClosed
		}
		l.reject(directRemote, parseErr)
	}
}

// Addr returns the underlying listener address.
func (l *Listener) Addr() net.Addr {
	if l == nil || l.listener == nil {
		return nil
	}
	return l.listener.Addr()
}

func (l *Listener) parseConnection(raw net.Conn) (net.Conn, error) {
	if !l.config.trusts(raw.RemoteAddr()) {
		return nil, ErrUntrustedProxy
	}
	if err := raw.SetReadDeadline(time.Now().Add(l.config.headerTimeout)); err != nil {
		return nil, fmt.Errorf("set proxy protocol header deadline: %w", err)
	}
	reader := bufio.NewReaderSize(raw, maxV1HeaderBytes)
	header, err := parseHeader(reader)
	if err != nil {
		var networkError net.Error
		if errors.As(err, &networkError) && networkError.Timeout() {
			return nil, errors.Join(ErrHeaderTimeout, err)
		}
		return nil, err
	}
	if err := raw.SetReadDeadline(time.Time{}); err != nil {
		return nil, fmt.Errorf("clear proxy protocol header deadline: %w", err)
	}
	return newConn(raw, reader, header), nil
}

func (c compiledConfig) trusts(address net.Addr) bool {
	ip, ok := addressIP(address)
	if !ok {
		return false
	}
	for _, prefix := range c.trusted {
		if prefix.Contains(ip) || (ip.Is4In6() && prefix.Contains(ip.Unmap())) {
			return true
		}
	}
	return false
}

func addressIP(address net.Addr) (netip.Addr, bool) {
	if address == nil {
		return netip.Addr{}, false
	}
	if proxied, ok := address.(*Addr); ok {
		return addressIP(proxied.Direct())
	}
	if tcpAddress, ok := address.(*net.TCPAddr); ok {
		ip, ok := netip.AddrFromSlice(tcpAddress.IP)
		return ip, ok
	}
	host, _, err := net.SplitHostPort(address.String())
	if err != nil {
		return netip.Addr{}, false
	}
	ip, err := netip.ParseAddr(strings.Trim(host, "[]"))
	return ip, err == nil
}

func (l *Listener) beginParse(connection net.Conn) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return false
	}
	l.parsing[connection] = struct{}{}
	return true
}

func (l *Listener) endParse(connection net.Conn) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.parsing, connection)
	return !l.closed
}

func (l *Listener) reject(address net.Addr, err error) {
	if err == nil || l.config.onReject == nil {
		return
	}
	event := RejectEvent{Err: err}
	if address != nil {
		event.DirectRemoteAddr = address.String()
	}
	defer func() { _ = recover() }()
	l.config.onReject(event)
}

// Close closes the underlying listener and any connections currently blocked
// while sending their header.
func (l *Listener) Close() error {
	l.closeOnce.Do(func() {
		l.mu.Lock()
		l.closed = true
		connections := make([]net.Conn, 0, len(l.parsing))
		for connection := range l.parsing {
			connections = append(connections, connection)
		}
		l.mu.Unlock()

		l.closeErr = l.listener.Close()
		for _, connection := range connections {
			_ = connection.Close()
		}
	})
	return l.closeErr
}

func parseHeader(reader *bufio.Reader) (Header, error) {
	prefix, err := reader.Peek(len("PROXY"))
	if err != nil {
		return Header{}, errors.Join(
			fmt.Errorf("%w: incomplete prefix", ErrHeaderRequired),
			err,
		)
	}
	if bytes.Equal(prefix, []byte("PROXY")) {
		return parseV1(reader)
	}
	signature, err := reader.Peek(len(v2Signature))
	if err != nil {
		return Header{}, errors.Join(ErrHeaderRequired, err)
	}
	if !bytes.Equal(signature, v2Signature) {
		return Header{}, ErrHeaderRequired
	}
	return parseV2(reader)
}

func parseV1(reader *bufio.Reader) (Header, error) {
	line, err := reader.ReadSlice('\n')
	if errors.Is(err, bufio.ErrBufferFull) || len(line) > maxV1HeaderBytes {
		return Header{}, fmt.Errorf("%w: v1 header exceeds %d bytes", ErrMalformedHeader, maxV1HeaderBytes)
	}
	if err != nil {
		return Header{}, malformedRead("incomplete v1 header", err)
	}
	if len(line) < 2 || !bytes.HasSuffix(line, []byte("\r\n")) {
		return Header{}, fmt.Errorf("%w: v1 header must end with CRLF", ErrMalformedHeader)
	}
	fields := strings.Split(string(line[:len(line)-2]), " ")
	if len(fields) < 2 || fields[0] != "PROXY" {
		return Header{}, fmt.Errorf("%w: invalid v1 prefix", ErrMalformedHeader)
	}
	if fields[1] == "UNKNOWN" {
		return Header{Version: 1, Command: CommandUnknown, Protocol: "UNKNOWN"}, nil
	}
	if len(fields) != 6 || (fields[1] != "TCP4" && fields[1] != "TCP6") {
		return Header{}, fmt.Errorf("%w: invalid v1 address fields", ErrMalformedHeader)
	}
	source, err := netip.ParseAddr(fields[2])
	if err != nil {
		return Header{}, fmt.Errorf("%w: invalid v1 source address", ErrMalformedHeader)
	}
	destination, err := netip.ParseAddr(fields[3])
	if err != nil {
		return Header{}, fmt.Errorf("%w: invalid v1 destination address", ErrMalformedHeader)
	}
	if source.Zone() != "" || destination.Zone() != "" {
		return Header{}, fmt.Errorf("%w: v1 addresses must not contain IPv6 zones", ErrMalformedHeader)
	}
	if fields[1] == "TCP4" && (!source.Is4() || !destination.Is4()) {
		return Header{}, fmt.Errorf("%w: TCP4 requires IPv4 addresses", ErrMalformedHeader)
	}
	if fields[1] == "TCP6" && (strings.Contains(fields[2], ".") || strings.Contains(fields[3], ".") ||
		!source.Is6() || source.Is4In6() || !destination.Is6() || destination.Is4In6()) {
		return Header{}, fmt.Errorf("%w: TCP6 requires IPv6 addresses", ErrMalformedHeader)
	}
	sourcePort, err := parsePort(fields[4])
	if err != nil {
		return Header{}, fmt.Errorf("%w: invalid v1 source port", ErrMalformedHeader)
	}
	destinationPort, err := parsePort(fields[5])
	if err != nil {
		return Header{}, fmt.Errorf("%w: invalid v1 destination port", ErrMalformedHeader)
	}
	return Header{
		Version:     1,
		Command:     CommandProxy,
		Protocol:    fields[1],
		Source:      netip.AddrPortFrom(source, sourcePort),
		Destination: netip.AddrPortFrom(destination, destinationPort),
	}, nil
}

func parsePort(value string) (uint16, error) {
	if value == "" || (len(value) > 1 && value[0] == '0') {
		return 0, errors.New("port must use canonical decimal form")
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return 0, errors.New("port must use canonical decimal form")
		}
	}
	port, err := strconv.ParseUint(value, 10, 16)
	if err != nil {
		return 0, err
	}
	return uint16(port), nil
}

func parseV2(reader *bufio.Reader) (Header, error) {
	var fixed [16]byte
	if _, err := io.ReadFull(reader, fixed[:]); err != nil {
		return Header{}, malformedRead("incomplete v2 fixed header", err)
	}
	if !bytes.Equal(fixed[:12], v2Signature) {
		return Header{}, fmt.Errorf("%w: invalid v2 signature", ErrMalformedHeader)
	}
	versionCommand := fixed[12]
	if versionCommand>>4 != 2 {
		return Header{}, fmt.Errorf("%w: invalid v2 version", ErrMalformedHeader)
	}
	command := versionCommand & 0x0f
	length := int(binary.BigEndian.Uint16(fixed[14:16]))
	if command == 0 {
		if err := discard(reader, length); err != nil {
			return Header{}, malformedRead("incomplete v2 LOCAL payload", err)
		}
		return Header{Version: 2, Command: CommandLocal, Protocol: "LOCAL"}, nil
	}
	if command != 1 {
		return Header{}, fmt.Errorf("%w: invalid v2 command", ErrMalformedHeader)
	}

	familyProtocol := fixed[13]
	if familyProtocol == 0 {
		if err := discard(reader, length); err != nil {
			return Header{}, malformedRead("incomplete v2 UNSPEC payload", err)
		}
		return Header{Version: 2, Command: CommandProxy, Protocol: "UNSPEC"}, nil
	}

	switch familyProtocol {
	case 0x11:
		return parseV2TCP4(reader, length)
	case 0x21:
		return parseV2TCP6(reader, length)
	case 0x12:
		return parseV2Unsupported(reader, length, 12)
	case 0x22:
		return parseV2Unsupported(reader, length, 36)
	case 0x31, 0x32:
		return parseV2Unsupported(reader, length, 216)
	default:
		return Header{}, fmt.Errorf("%w: invalid v2 family/protocol combination", ErrUnsupportedHeader)
	}
}

func parseV2Unsupported(reader *bufio.Reader, length int, addressBytes int) (Header, error) {
	if length < addressBytes {
		return Header{}, fmt.Errorf("%w: short v2 unsupported address", ErrMalformedHeader)
	}
	if err := discard(reader, length); err != nil {
		return Header{}, malformedRead("incomplete v2 unsupported address", err)
	}
	return Header{Version: 2, Command: CommandProxy, Protocol: "UNSPEC"}, nil
}

func parseV2TCP4(reader *bufio.Reader, length int) (Header, error) {
	const addressBytes = 12
	if length < addressBytes {
		return Header{}, fmt.Errorf("%w: short v2 TCP4 address", ErrMalformedHeader)
	}
	var address [addressBytes]byte
	if _, err := io.ReadFull(reader, address[:]); err != nil {
		return Header{}, malformedRead("incomplete v2 TCP4 address", err)
	}
	if err := discard(reader, length-addressBytes); err != nil {
		return Header{}, malformedRead("incomplete v2 TCP4 TLVs", err)
	}
	source := netip.AddrFrom4([4]byte(address[0:4]))
	destination := netip.AddrFrom4([4]byte(address[4:8]))
	return Header{
		Version:     2,
		Command:     CommandProxy,
		Protocol:    "TCP4",
		Source:      netip.AddrPortFrom(source, binary.BigEndian.Uint16(address[8:10])),
		Destination: netip.AddrPortFrom(destination, binary.BigEndian.Uint16(address[10:12])),
		TLVBytes:    length - addressBytes,
	}, nil
}

func parseV2TCP6(reader *bufio.Reader, length int) (Header, error) {
	const addressBytes = 36
	if length < addressBytes {
		return Header{}, fmt.Errorf("%w: short v2 TCP6 address", ErrMalformedHeader)
	}
	var address [addressBytes]byte
	if _, err := io.ReadFull(reader, address[:]); err != nil {
		return Header{}, malformedRead("incomplete v2 TCP6 address", err)
	}
	if err := discard(reader, length-addressBytes); err != nil {
		return Header{}, malformedRead("incomplete v2 TCP6 TLVs", err)
	}
	var sourceBytes [16]byte
	var destinationBytes [16]byte
	copy(sourceBytes[:], address[0:16])
	copy(destinationBytes[:], address[16:32])
	return Header{
		Version:     2,
		Command:     CommandProxy,
		Protocol:    "TCP6",
		Source:      netip.AddrPortFrom(netip.AddrFrom16(sourceBytes), binary.BigEndian.Uint16(address[32:34])),
		Destination: netip.AddrPortFrom(netip.AddrFrom16(destinationBytes), binary.BigEndian.Uint16(address[34:36])),
		TLVBytes:    length - addressBytes,
	}, nil
}

func discard(reader io.Reader, bytes int) error {
	if bytes == 0 {
		return nil
	}
	_, err := io.CopyN(io.Discard, reader, int64(bytes))
	return err
}

func malformedRead(reason string, err error) error {
	return errors.Join(fmt.Errorf("%w: %s", ErrMalformedHeader, reason), err)
}

// Conn preserves bytes buffered while parsing the header and exposes both
// effective and direct connection addresses.
type Conn struct {
	net.Conn
	readMu     sync.Mutex
	reader     *bufio.Reader
	remoteAddr net.Addr
	localAddr  net.Addr
	info       Info
}

var _ net.Listener = (*Listener)(nil)
var _ net.Conn = (*Conn)(nil)

func newConn(raw net.Conn, reader *bufio.Reader, header Header) *Conn {
	directRemote := raw.RemoteAddr()
	directLocal := raw.LocalAddr()
	effectiveRemote := directRemote
	effectiveLocal := directLocal
	if header.Source.IsValid() {
		effectiveRemote = tcpAddr(header.Source)
	}
	if header.Destination.IsValid() {
		effectiveLocal = tcpAddr(header.Destination)
	}
	info := Info{
		Header:           header,
		DirectRemoteAddr: addrString(directRemote),
		DirectLocalAddr:  addrString(directLocal),
	}
	return &Conn{
		Conn:       raw,
		reader:     reader,
		remoteAddr: &Addr{effective: effectiveRemote, direct: directRemote, info: info},
		localAddr:  &Addr{effective: effectiveLocal, direct: directLocal, info: info},
		info:       info,
	}
}

func tcpAddr(address netip.AddrPort) *net.TCPAddr {
	return &net.TCPAddr{
		IP:   net.IP(address.Addr().AsSlice()),
		Port: int(address.Port()),
	}
}

func addrString(address net.Addr) string {
	if address == nil {
		return ""
	}
	return address.String()
}

// Read consumes any application bytes buffered while parsing before reading
// from the underlying socket.
func (c *Conn) Read(buffer []byte) (int, error) {
	c.readMu.Lock()
	defer c.readMu.Unlock()
	return c.reader.Read(buffer)
}

// WriteBuffers writes through the original socket when connection is a
// PROXY-wrapped connection. This preserves net.TCPConn's gather-write path;
// PROXY protocol affects only bytes read at connection startup.
func WriteBuffers(buffers *net.Buffers, connection net.Conn) (int64, error) {
	return buffers.WriteTo(writeConnection(connection))
}

func writeConnection(connection net.Conn) net.Conn {
	if proxied, ok := connection.(*Conn); ok && proxied != nil && proxied.Conn != nil {
		return proxied.Conn
	}
	return connection
}

func (c *Conn) RemoteAddr() net.Addr { return c.remoteAddr }
func (c *Conn) LocalAddr() net.Addr  { return c.localAddr }

// ProxyProtocolInfo returns the immutable connection metadata.
func (c *Conn) ProxyProtocolInfo() (Info, bool) {
	if c == nil {
		return Info{}, false
	}
	return c.info, true
}

// Addr is a net.Addr whose String value is the effective advertised address.
// Direct returns the actual socket address.
type Addr struct {
	effective net.Addr
	direct    net.Addr
	info      Info
}

func (a *Addr) Network() string {
	if a == nil {
		return ""
	}
	if a.effective != nil {
		return a.effective.Network()
	}
	if a.direct != nil {
		return a.direct.Network()
	}
	return ""
}

func (a *Addr) String() string {
	if a == nil {
		return ""
	}
	return addrString(a.effective)
}

// Direct returns the actual socket address before PROXY protocol replacement.
func (a *Addr) Direct() net.Addr {
	if a == nil {
		return nil
	}
	return a.direct
}

// ProxyProtocolInfo returns the immutable connection metadata.
func (a *Addr) ProxyProtocolInfo() (Info, bool) {
	if a == nil {
		return Info{}, false
	}
	return a.info, true
}

type infoProvider interface {
	ProxyProtocolInfo() (Info, bool)
}

// InfoFrom returns PROXY protocol metadata from a wrapped connection, address,
// or session exposing RemoteAddr() net.Addr.
func InfoFrom(value any) (Info, bool) {
	if value == nil {
		return Info{}, false
	}
	if provider, ok := value.(infoProvider); ok {
		return provider.ProxyProtocolInfo()
	}
	if source, ok := value.(interface{ RemoteAddr() net.Addr }); ok {
		return InfoFrom(source.RemoteAddr())
	}
	return Info{}, false
}

type infoContextKey struct{}

// WithInfo attaches connection metadata to a request or RPC context.
func WithInfo(ctx context.Context, info Info) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, infoContextKey{}, info)
}

// FromContext returns connection metadata attached by a KPro native transport.
func FromContext(ctx context.Context) (Info, bool) {
	if ctx == nil {
		return Info{}, false
	}
	info, ok := ctx.Value(infoContextKey{}).(Info)
	return info, ok
}
