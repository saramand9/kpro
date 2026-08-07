// Command ws-loadgen drives an RFC 6455 binary endpoint carrying KProV1
// packets. It validates every completed echo and is intended for local
// implementation comparisons, not distributed production load testing.
package main

import (
	"bufio"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	headerSize     = 20
	echoMessageID  = 100
	maxPayloadSize = 64 * 1024
	webSocketGUID  = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"
)

type loadConfig struct {
	addr        string
	path        string
	connections int
	payloadSize int
	inflight    int
	coalesce    int
	duration    time.Duration
	maxSamples  int
}

type runWindow struct {
	startedAt time.Time
	deadline  time.Time
}

type wsConnection struct {
	net.Conn
	reader *bufio.Reader
	seed   uint64
}

type workerStats struct {
	sent            uint64
	completed       uint64
	bytesWritten    uint64
	validationError uint64
	ioError         uint64
	timeout         uint64
	samples         []int64
	firstError      string
}

type loadSummary struct {
	config           loadConfig
	wallElapsed      time.Duration
	sent             uint64
	completed        uint64
	incomplete       uint64
	bytesWritten     uint64
	validationErrors uint64
	ioErrors         uint64
	timeouts         uint64
	samples          []int64
	firstError       string
}

func main() {
	addr := flag.String("addr", "127.0.0.1:18401", "server address")
	path := flag.String("path", "/ws", "websocket path")
	connections := flag.Int("connections", 1, "connections")
	payloadSize := flag.Int("payload", 64, "KPro application payload bytes")
	inflight := flag.Int("inflight", 1, "messages sent before reading responses")
	coalesce := flag.Int("coalesce", 1, "websocket frames combined in one TCP write")
	duration := flag.Duration("duration", 3*time.Second, "fixed measurement duration")
	maxSamples := flag.Int("max-samples", 200_000, "maximum latency reservoir samples")
	flag.Parse()

	config := loadConfig{
		addr:        *addr,
		path:        *path,
		connections: *connections,
		payloadSize: *payloadSize,
		inflight:    *inflight,
		coalesce:    *coalesce,
		duration:    *duration,
		maxSamples:  *maxSamples,
	}
	if config.coalesce > config.inflight {
		config.coalesce = config.inflight
	}
	if err := config.validate(); err != nil {
		fmt.Fprintf(os.Stderr, "invalid arguments: %v\n", err)
		os.Exit(2)
	}

	summary, err := runLoad(config)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	printSummary(summary)
	if summary.firstError != "" {
		fmt.Fprintf(os.Stderr, "first_error=%q\n", summary.firstError)
	}
	if summary.validationErrors > 0 || summary.ioErrors > 0 || summary.completed == 0 {
		os.Exit(1)
	}
}

func (c loadConfig) validate() error {
	switch {
	case c.addr == "":
		return errors.New("addr must not be empty")
	case c.path == "" || c.path[0] != '/':
		return errors.New("path must start with /")
	case c.connections < 1:
		return errors.New("connections must be positive")
	case c.payloadSize < 0 || c.payloadSize > maxPayloadSize:
		return fmt.Errorf("payload must be between 0 and %d bytes", maxPayloadSize)
	case c.inflight < 1:
		return errors.New("inflight must be positive")
	case c.coalesce < 1 || c.coalesce > c.inflight:
		return errors.New("coalesce must be between 1 and inflight")
	case c.duration <= 0:
		return errors.New("duration must be positive")
	case c.maxSamples < 0:
		return errors.New("max-samples must not be negative")
	}
	frameSize := clientFrameSize(headerSize + c.payloadSize)
	maxInt := int(^uint(0) >> 1)
	if frameSize > maxInt/c.coalesce {
		return errors.New("coalesced write buffer is too large")
	}
	return nil
}

func runLoad(config loadConfig) (loadSummary, error) {
	connections := make([]*wsConnection, 0, config.connections)
	for index := 0; index < config.connections; index++ {
		conn, err := dialWebSocket(config.addr, config.path)
		if err != nil {
			closeConnections(connections)
			return loadSummary{}, fmt.Errorf("dial %d: %w", index, err)
		}
		connections = append(connections, conn)
	}
	defer closeConnections(connections)

	sampleBudgets := allocateSampleBudgets(config.maxSamples, config.connections)
	results := make([]workerStats, len(connections))
	start := make(chan struct{})
	var window runWindow
	var ready sync.WaitGroup
	var workers sync.WaitGroup
	ready.Add(len(connections))
	workers.Add(len(connections))
	for index, conn := range connections {
		go runWorker(config, index, conn, sampleBudgets[index], &window, start, &ready, &workers, &results[index])
	}

	ready.Wait()
	window = runWindow{startedAt: time.Now()}
	window.deadline = window.startedAt.Add(config.duration)
	close(start)
	workers.Wait()
	return summarize(config, time.Since(window.startedAt), results), nil
}

func dialWebSocket(addr, path string) (*wsConnection, error) {
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		return nil, err
	}
	keep := false
	defer func() {
		if !keep {
			_ = conn.Close()
		}
	}()
	if tcpConn, ok := conn.(*net.TCPConn); ok {
		_ = tcpConn.SetNoDelay(true)
	}
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return nil, err
	}

	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	key := base64.StdEncoding.EncodeToString(nonce[:])
	request := fmt.Sprintf(
		"GET %s HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\n\r\n",
		path, addr, key,
	)
	if _, err := io.WriteString(conn, request); err != nil {
		return nil, err
	}
	reader := bufio.NewReaderSize(conn, 32*1024)
	response, err := http.ReadResponse(reader, &http.Request{Method: http.MethodGet})
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusSwitchingProtocols {
		return nil, fmt.Errorf("handshake status %s", response.Status)
	}
	if !strings.EqualFold(response.Header.Get("Upgrade"), "websocket") {
		return nil, errors.New("handshake missing websocket Upgrade header")
	}
	digest := sha1.Sum([]byte(key + webSocketGUID))
	wantAccept := base64.StdEncoding.EncodeToString(digest[:])
	if response.Header.Get("Sec-WebSocket-Accept") != wantAccept {
		return nil, errors.New("invalid Sec-WebSocket-Accept")
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		return nil, err
	}

	var seedBytes [8]byte
	if _, err := rand.Read(seedBytes[:]); err != nil {
		return nil, err
	}
	seed := binary.LittleEndian.Uint64(seedBytes[:])
	if seed == 0 {
		seed = 1
	}
	keep = true
	return &wsConnection{Conn: conn, reader: reader, seed: seed}, nil
}

func runWorker(
	config loadConfig,
	index int,
	conn *wsConnection,
	sampleBudget int,
	window *runWindow,
	start <-chan struct{},
	ready *sync.WaitGroup,
	workers *sync.WaitGroup,
	result *workerStats,
) {
	defer workers.Done()
	defer conn.Close()

	packetSize := headerSize + config.payloadSize
	wireFrameSize := clientFrameSize(packetSize)
	writeBuffer := make([]byte, wireFrameSize*config.coalesce)
	readBuffer := make([]byte, packetSize)
	sentAt := make([]time.Time, config.inflight)
	sentSequences := make([]uint64, config.inflight)
	latencies := newReservoir(sampleBudget, uint64(index+1))
	maskState := conn.seed
	var sequence uint64

	ready.Done()
	<-start
	if err := conn.SetDeadline(window.deadline); err != nil {
		recordIOFailure(result, err)
		return
	}
	for time.Now().Before(window.deadline) {
		for first := 0; first < config.inflight; first += config.coalesce {
			if !time.Now().Before(window.deadline) {
				result.timeout++
				result.samples = latencies.values
				return
			}
			count := min(config.coalesce, config.inflight-first)
			sentTime := time.Now()
			offset := 0
			for item := 0; item < count; item++ {
				sequence++
				size := encodeClientFrame(writeBuffer[offset:], sequence, config.payloadSize, &maskState)
				offset += size
				sentAt[first+item] = sentTime
				sentSequences[first+item] = sequence
			}
			written, err := writeFull(conn, writeBuffer[:offset])
			result.bytesWritten += uint64(written)
			result.sent += uint64(written / wireFrameSize)
			if err != nil {
				recordIOFailure(result, err)
				result.samples = latencies.values
				return
			}
		}
		for item := 0; item < config.inflight; item++ {
			if err := readServerBinaryFrame(conn.reader, readBuffer); err != nil {
				recordIOFailure(result, err)
				result.samples = latencies.values
				return
			}
			observedAt := time.Now()
			if !observedAt.Before(window.deadline) {
				result.timeout++
				result.samples = latencies.values
				return
			}
			if err := validateResponsePacket(readBuffer, sentSequences[item], config.payloadSize); err != nil {
				result.validationError++
				setFirstError(result, fmt.Sprintf("connection %d: %v", index, err))
				result.samples = latencies.values
				return
			}
			result.completed++
			latencies.add(observedAt.Sub(sentAt[item]).Nanoseconds())
		}
	}
	result.timeout++
	result.samples = latencies.values
}

func clientFrameSize(payloadSize int) int {
	switch {
	case payloadSize <= 125:
		return 2 + 4 + payloadSize
	case payloadSize <= 65535:
		return 2 + 2 + 4 + payloadSize
	default:
		return 2 + 8 + 4 + payloadSize
	}
}

func encodeClientFrame(dst []byte, sequence uint64, payloadSize int, maskState *uint64) int {
	packetSize := headerSize + payloadSize
	dst[0] = 0x82
	position := 2
	switch {
	case packetSize <= 125:
		dst[1] = 0x80 | byte(packetSize)
	case packetSize <= 65535:
		dst[1] = 0x80 | 126
		binary.BigEndian.PutUint16(dst[position:position+2], uint16(packetSize))
		position += 2
	default:
		dst[1] = 0x80 | 127
		binary.BigEndian.PutUint64(dst[position:position+8], uint64(packetSize))
		position += 8
	}

	mask := nextMask(maskState)
	dst[position+0] = byte(mask >> 24)
	dst[position+1] = byte(mask >> 16)
	dst[position+2] = byte(mask >> 8)
	dst[position+3] = byte(mask)
	maskBytes := dst[position : position+4]
	position += 4
	payload := dst[position : position+packetSize]
	binary.BigEndian.PutUint16(payload[0:2], 0x4b50)
	payload[2] = 1
	payload[3] = 0
	binary.BigEndian.PutUint32(payload[4:8], echoMessageID)
	binary.BigEndian.PutUint64(payload[8:16], sequence)
	binary.BigEndian.PutUint32(payload[16:20], uint32(payloadSize))
	for index := 0; index < payloadSize; index++ {
		payload[headerSize+index] = expectedPayloadByte(sequence, index)
	}
	for index := range payload {
		payload[index] ^= maskBytes[index&3]
	}
	return position + packetSize
}

func nextMask(state *uint64) uint32 {
	value := *state
	value ^= value << 13
	value ^= value >> 7
	value ^= value << 17
	*state = value
	return uint32(value ^ (value >> 32))
}

func readServerBinaryFrame(reader *bufio.Reader, payload []byte) error {
	var header [2]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return err
	}
	if header[0]&0x80 == 0 {
		return errors.New("fragmented server frame")
	}
	if opcode := header[0] & 0x0f; opcode != 0x2 {
		return fmt.Errorf("server opcode %d, want binary", opcode)
	}
	if header[1]&0x80 != 0 {
		return errors.New("server frame must not be masked")
	}
	length := uint64(header[1] & 0x7f)
	switch length {
	case 126:
		var extended [2]byte
		if _, err := io.ReadFull(reader, extended[:]); err != nil {
			return err
		}
		length = uint64(binary.BigEndian.Uint16(extended[:]))
	case 127:
		var extended [8]byte
		if _, err := io.ReadFull(reader, extended[:]); err != nil {
			return err
		}
		length = binary.BigEndian.Uint64(extended[:])
	}
	if length != uint64(len(payload)) {
		return fmt.Errorf("server payload length %d, want %d", length, len(payload))
	}
	_, err := io.ReadFull(reader, payload)
	return err
}

func validateResponsePacket(packet []byte, sequence uint64, payloadSize int) error {
	if len(packet) != headerSize+payloadSize {
		return fmt.Errorf("packet length %d, want %d", len(packet), headerSize+payloadSize)
	}
	switch {
	case binary.BigEndian.Uint16(packet[0:2]) != 0x4b50:
		return errors.New("invalid KPro magic")
	case packet[2] != 1:
		return fmt.Errorf("version %d, want 1", packet[2])
	case packet[3] != 0:
		return fmt.Errorf("flags %d, want 0", packet[3])
	case binary.BigEndian.Uint32(packet[4:8]) != echoMessageID:
		return fmt.Errorf("message id %d, want %d", binary.BigEndian.Uint32(packet[4:8]), echoMessageID)
	case binary.BigEndian.Uint64(packet[8:16]) != sequence:
		return fmt.Errorf("sequence %d, want %d", binary.BigEndian.Uint64(packet[8:16]), sequence)
	case binary.BigEndian.Uint32(packet[16:20]) != uint32(payloadSize):
		return fmt.Errorf("payload length %d, want %d", binary.BigEndian.Uint32(packet[16:20]), payloadSize)
	}
	for index, value := range packet[headerSize:] {
		if want := expectedPayloadByte(sequence, index); value != want {
			return fmt.Errorf("payload byte %d = %d, want %d", index, value, want)
		}
	}
	return nil
}

func expectedPayloadByte(sequence uint64, index int) byte {
	shift := uint(index&7) * 8
	return byte(sequence>>shift) ^ byte(index*31+17)
}

func writeFull(writer io.Writer, buffer []byte) (int, error) {
	total := 0
	for len(buffer) > 0 {
		written, err := writer.Write(buffer)
		if written < 0 || written > len(buffer) {
			return total, fmt.Errorf("invalid write count %d for buffer length %d", written, len(buffer))
		}
		total += written
		buffer = buffer[written:]
		if err != nil {
			return total, err
		}
		if written == 0 {
			return total, io.ErrShortWrite
		}
	}
	return total, nil
}

func recordIOFailure(result *workerStats, err error) {
	if isMeasurementTimeout(err) {
		result.timeout++
		return
	}
	result.ioError++
	setFirstError(result, err.Error())
}

func isMeasurementTimeout(err error) bool {
	var networkError net.Error
	return (errors.As(err, &networkError) && networkError.Timeout()) || errors.Is(err, os.ErrDeadlineExceeded)
}

func setFirstError(result *workerStats, text string) {
	if result.firstError == "" {
		result.firstError = text
	}
}

func summarize(config loadConfig, wallElapsed time.Duration, results []workerStats) loadSummary {
	summary := loadSummary{
		config:      config,
		wallElapsed: wallElapsed,
		samples:     make([]int64, 0, config.maxSamples),
	}
	for _, result := range results {
		summary.sent += result.sent
		summary.completed += result.completed
		summary.bytesWritten += result.bytesWritten
		summary.validationErrors += result.validationError
		summary.ioErrors += result.ioError
		summary.timeouts += result.timeout
		summary.samples = append(summary.samples, result.samples...)
		if summary.firstError == "" {
			summary.firstError = result.firstError
		}
	}
	if summary.sent >= summary.completed {
		summary.incomplete = summary.sent - summary.completed
	}
	sort.Slice(summary.samples, func(left, right int) bool { return summary.samples[left] < summary.samples[right] })
	return summary
}

func printSummary(summary loadSummary) {
	percentile := func(fraction float64) float64 {
		if len(summary.samples) == 0 {
			return 0
		}
		index := int(float64(len(summary.samples)-1) * fraction)
		return float64(summary.samples[index]) / 1_000
	}
	throughput := float64(summary.completed) / summary.config.duration.Seconds()
	fmt.Printf(
		"connections=%d payload=%d inflight=%d coalesce=%d duration_s=%.3f wall_s=%.3f sent=%d completed=%d incomplete=%d validation_errors=%d io_errors=%d timeouts=%d bytes=%d throughput_msg_s=%.0f p50_us=%.2f p99_us=%.2f p999_us=%.2f samples=%d\n",
		summary.config.connections,
		summary.config.payloadSize,
		summary.config.inflight,
		summary.config.coalesce,
		summary.config.duration.Seconds(),
		summary.wallElapsed.Seconds(),
		summary.sent,
		summary.completed,
		summary.incomplete,
		summary.validationErrors,
		summary.ioErrors,
		summary.timeouts,
		summary.bytesWritten,
		throughput,
		percentile(0.50),
		percentile(0.99),
		percentile(0.999),
		len(summary.samples),
	)
}

type reservoir struct {
	values []int64
	seen   uint64
	state  uint64
}

func newReservoir(capacity int, seed uint64) reservoir {
	if seed == 0 {
		seed = 1
	}
	return reservoir{values: make([]int64, 0, capacity), state: seed}
}

func (r *reservoir) add(value int64) {
	r.seen++
	if cap(r.values) == 0 {
		return
	}
	if len(r.values) < cap(r.values) {
		r.values = append(r.values, value)
		return
	}
	r.state ^= r.state << 13
	r.state ^= r.state >> 7
	r.state ^= r.state << 17
	index := r.state % r.seen
	if index < uint64(len(r.values)) {
		r.values[index] = value
	}
}

func allocateSampleBudgets(total, connections int) []int {
	budgets := make([]int, connections)
	if total <= 0 || connections <= 0 {
		return budgets
	}
	base := total / connections
	remainder := total % connections
	for index := range budgets {
		budgets[index] = base
		if index < remainder {
			budgets[index]++
		}
	}
	return budgets
}

func closeConnections(connections []*wsConnection) {
	for _, conn := range connections {
		_ = conn.Close()
	}
}
