package wsclient

import (
	"bufio"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"
	"time"
)

const (
	OpBinary = 0x2
	OpClose  = 0x8
	OpPing   = 0x9
	OpPong   = 0xa
)

var ErrBadHandshake = errors.New("bad websocket handshake")

type Conn struct {
	net.Conn
	reader *bufio.Reader
}

func Dial(rawURL string, timeout time.Duration) (*Conn, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	if u.Scheme != "ws" {
		return nil, fmt.Errorf("unsupported websocket scheme %q", u.Scheme)
	}
	addr := u.Host
	if !strings.Contains(addr, ":") {
		addr += ":80"
	}

	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return nil, err
	}
	reader := bufio.NewReader(conn)

	key, err := secWebSocketKey()
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	path := u.RequestURI()
	if path == "" {
		path = "/"
	}
	_, err = fmt.Fprintf(conn,
		"GET %s HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\n\r\n",
		path, u.Host, key)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	if err := readHandshake(reader); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return &Conn{Conn: conn, reader: reader}, nil
}

func (c *Conn) ReadFrame(maxSize int) (byte, []byte, error) {
	return ReadFrame(c.reader, maxSize)
}

func (c *Conn) WriteFrame(opcode byte, payload []byte) error {
	return WriteFrame(c.Conn, opcode, payload, true)
}

func ReadFrame(r io.Reader, maxSize int) (byte, []byte, error) {
	var header [2]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return 0, nil, err
	}
	opcode := header[0] & 0x0f
	masked := header[1]&0x80 != 0
	length := uint64(header[1] & 0x7f)
	switch length {
	case 126:
		var ext [2]byte
		if _, err := io.ReadFull(r, ext[:]); err != nil {
			return 0, nil, err
		}
		length = uint64(binary.BigEndian.Uint16(ext[:]))
	case 127:
		var ext [8]byte
		if _, err := io.ReadFull(r, ext[:]); err != nil {
			return 0, nil, err
		}
		length = binary.BigEndian.Uint64(ext[:])
	}
	if maxSize > 0 && length > uint64(maxSize) {
		return 0, nil, fmt.Errorf("websocket frame too large: %d > %d", length, maxSize)
	}
	var mask [4]byte
	if masked {
		if _, err := io.ReadFull(r, mask[:]); err != nil {
			return 0, nil, err
		}
	}
	payload := make([]byte, int(length))
	if length > 0 {
		if _, err := io.ReadFull(r, payload); err != nil {
			return 0, nil, err
		}
	}
	if masked {
		for i := range payload {
			payload[i] ^= mask[i%4]
		}
	}
	return opcode, payload, nil
}

func WriteFrame(w io.Writer, opcode byte, payload []byte, masked bool) error {
	header := []byte{0x80 | opcode}
	length := len(payload)
	maskBit := byte(0)
	if masked {
		maskBit = 0x80
	}
	switch {
	case length < 126:
		header = append(header, maskBit|byte(length))
	case length <= 0xffff:
		header = append(header, maskBit|126, 0, 0)
		binary.BigEndian.PutUint16(header[len(header)-2:], uint16(length))
	default:
		header = append(header, maskBit|127, 0, 0, 0, 0, 0, 0, 0, 0)
		binary.BigEndian.PutUint64(header[len(header)-8:], uint64(length))
	}
	var mask [4]byte
	if masked {
		if _, err := rand.Read(mask[:]); err != nil {
			return err
		}
		header = append(header, mask[:]...)
	}
	if _, err := w.Write(header); err != nil {
		return err
	}
	if len(payload) == 0 {
		return nil
	}
	if masked {
		body := append([]byte(nil), payload...)
		for i := range body {
			body[i] ^= mask[i%4]
		}
		_, err := w.Write(body)
		return err
	}
	_, err := w.Write(payload)
	return err
}

func secWebSocketKey() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(b[:]), nil
}

func readHandshake(r *bufio.Reader) error {
	status, err := r.ReadString('\n')
	if err != nil {
		return err
	}
	if !strings.Contains(status, " 101 ") {
		return fmt.Errorf("%w: %s", ErrBadHandshake, strings.TrimSpace(status))
	}
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return err
		}
		if line == "\r\n" {
			return nil
		}
	}
}
