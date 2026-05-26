package wire

import (
	"bufio"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
)

const wsMagic = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

func DialWebSocket(rawURL string) (net.Conn, *bufio.Reader, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, nil, err
	}
	host := u.Host
	if !strings.Contains(host, ":") {
		host += ":80"
	}
	conn, err := net.Dial("tcp", host)
	if err != nil {
		return nil, nil, err
	}
	key, err := websocketKey()
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
	if resp.Header.Get("Sec-WebSocket-Accept") != websocketAccept(key) {
		_ = conn.Close()
		return nil, nil, fmt.Errorf("websocket accept key mismatch")
	}
	return conn, reader, nil
}

func WriteWebSocketBinary(w io.Writer, payload []byte) error {
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

func ReadWebSocketBinary(r *bufio.Reader, maxSize int) ([]byte, error) {
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

func websocketKey() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(b[:]), nil
}

func websocketAccept(key string) string {
	sum := sha1.Sum([]byte(key + wsMagic))
	return base64.StdEncoding.EncodeToString(sum[:])
}
