// Command tcp-loadgen drives a KProV1 TCP endpoint with a bounded number of
// connections and in-flight echo requests. It is intended for local framework
// comparisons, not as a substitute for a distributed production load test.
package main

import (
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"sort"
	"sync"
	"time"
)

const (
	headerSize     = 20
	echoMessageID  = 100
	maxPayloadSize = 64 * 1024
)

type loadConfig struct {
	addr        string
	connections int
	payloadSize int
	inflight    int
	coalesce    int
	duration    time.Duration
	slowPercent int
	maxSamples  int
}

type runWindow struct {
	startedAt time.Time
	deadline  time.Time
}

type workerStats struct {
	healthySent     uint64
	slowSent        uint64
	completed       uint64
	bytesWritten    uint64
	validationError uint64
	healthyIOError  uint64
	slowIOError     uint64
	healthyTimeout  uint64
	slowTimeout     uint64
	samples         []int64
	firstError      string
}

type loadSummary struct {
	config           loadConfig
	healthyConns     int
	slowConns        int
	wallElapsed      time.Duration
	healthySent      uint64
	slowSent         uint64
	completed        uint64
	incomplete       uint64
	bytesWritten     uint64
	validationErrors uint64
	healthyIOErrors  uint64
	slowIOErrors     uint64
	healthyTimeouts  uint64
	slowTimeouts     uint64
	samples          []int64
	firstError       string
}

func main() {
	addr := flag.String("addr", "127.0.0.1:18088", "server address")
	connections := flag.Int("connections", 1, "connections")
	payloadSize := flag.Int("payload", 64, "payload bytes")
	inflight := flag.Int("inflight", 1, "requests sent before reading responses")
	coalesce := flag.Int("coalesce", 1, "frames combined in one write")
	duration := flag.Duration("duration", 3*time.Second, "fixed measurement duration")
	slowPercent := flag.Int("slow-percent", 0, "percentage of connections that do not read")
	maxSamples := flag.Int("max-samples", 200_000, "maximum latency reservoir samples")
	flag.Parse()

	config := loadConfig{
		addr:        *addr,
		connections: *connections,
		payloadSize: *payloadSize,
		inflight:    *inflight,
		coalesce:    *coalesce,
		duration:    *duration,
		slowPercent: *slowPercent,
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
	if code := summary.exitCode(); code != 0 {
		os.Exit(code)
	}
}

func (c loadConfig) validate() error {
	switch {
	case c.addr == "":
		return errors.New("addr must not be empty")
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
	case c.slowPercent < 0 || c.slowPercent > 100:
		return errors.New("slow-percent must be between 0 and 100")
	case c.maxSamples < 0:
		return errors.New("max-samples must not be negative")
	}
	frameSize := headerSize + c.payloadSize
	maxInt := int(^uint(0) >> 1)
	if frameSize > maxInt/c.coalesce {
		return errors.New("coalesced write buffer is too large")
	}
	return nil
}

func runLoad(config loadConfig) (loadSummary, error) {
	conns := make([]net.Conn, 0, config.connections)
	for index := 0; index < config.connections; index++ {
		conn, err := net.DialTimeout("tcp", config.addr, 5*time.Second)
		if err != nil {
			closeConnections(conns)
			return loadSummary{}, fmt.Errorf("dial %d: %w", index, err)
		}
		if tcpConn, ok := conn.(*net.TCPConn); ok {
			_ = tcpConn.SetNoDelay(true)
		}
		conns = append(conns, conn)
	}
	defer closeConnections(conns)

	slowCount := slowConnectionCount(config.connections, config.slowPercent)
	sampleBudgets := sampleBudgetsForWorkers(config.maxSamples, config.connections, slowCount)
	results := make([]workerStats, len(conns))
	start := make(chan struct{})
	var window runWindow
	var ready sync.WaitGroup
	var workers sync.WaitGroup
	ready.Add(len(conns))
	workers.Add(len(conns))
	for index, conn := range conns {
		slow := index < slowCount
		go runWorker(
			config,
			index,
			conn,
			slow,
			sampleBudgets[index],
			&window,
			start,
			&ready,
			&workers,
			&results[index],
		)
	}

	ready.Wait()
	window = newRunWindow(time.Now(), config.duration)
	close(start)
	workers.Wait()

	return summarize(config, slowCount, time.Since(window.startedAt), results), nil
}

func newRunWindow(startedAt time.Time, duration time.Duration) runWindow {
	return runWindow{
		startedAt: startedAt,
		deadline:  startedAt.Add(duration),
	}
}

func runWorker(
	config loadConfig,
	index int,
	conn net.Conn,
	slow bool,
	sampleBudget int,
	window *runWindow,
	start <-chan struct{},
	ready *sync.WaitGroup,
	workers *sync.WaitGroup,
	result *workerStats,
) {
	defer workers.Done()
	defer conn.Close()

	frameSize := headerSize + config.payloadSize
	writeBuffer := make([]byte, frameSize*config.coalesce)
	readBuffer := make([]byte, frameSize)
	sentAt := make([]time.Time, config.inflight)
	sentSequences := make([]uint64, config.inflight)
	latencies := newReservoir(sampleBudget, uint64(index+1))
	var sequence uint64

	// All allocation and worker-local initialization happens before readiness,
	// so the shared measurement window contains only workload activity.
	ready.Done()
	<-start

	if err := conn.SetDeadline(window.deadline); err != nil {
		recordIOFailure(result, slow, err)
		return
	}
	for time.Now().Before(window.deadline) {
		for first := 0; first < config.inflight; first += config.coalesce {
			if !time.Now().Before(window.deadline) {
				recordTimeout(result, slow)
				result.samples = latencies.values
				return
			}
			count := min(config.coalesce, config.inflight-first)
			sentTime := time.Now()
			for item := 0; item < count; item++ {
				sequence++
				frame := writeBuffer[item*frameSize : (item+1)*frameSize]
				encodeRequestFrame(frame, sequence, config.payloadSize)
				sentAt[first+item] = sentTime
				sentSequences[first+item] = sequence
			}
			bytes := count * frameSize
			written, err := writeFull(conn, writeBuffer[:bytes])
			result.bytesWritten += uint64(written)
			recordSent(result, slow, uint64(written/frameSize))
			if err != nil {
				recordIOFailure(result, slow, err)
				result.samples = latencies.values
				return
			}
		}
		if slow {
			continue
		}
		for item := 0; item < config.inflight; item++ {
			if _, err := io.ReadFull(conn, readBuffer); err != nil {
				recordIOFailure(result, false, err)
				result.samples = latencies.values
				return
			}
			if !time.Now().Before(window.deadline) {
				result.healthyTimeout++
				result.samples = latencies.values
				return
			}
			if err := validateResponseFrame(readBuffer, sentSequences[item], config.payloadSize); err != nil {
				result.validationError++
				setFirstError(result, fmt.Sprintf("connection %d: %v", index, err))
				result.samples = latencies.values
				return
			}
			observedAt := time.Now()
			if !observedAt.Before(window.deadline) {
				result.healthyTimeout++
				result.samples = latencies.values
				return
			}
			result.completed++
			latencies.add(observedAt.Sub(sentAt[item]).Nanoseconds())
		}
	}
	recordTimeout(result, slow)
	result.samples = latencies.values
}

func encodeRequestFrame(frame []byte, sequence uint64, payloadSize int) {
	binary.BigEndian.PutUint16(frame[0:2], 0x4b50)
	frame[2] = 1
	frame[3] = 0
	binary.BigEndian.PutUint32(frame[4:8], echoMessageID)
	binary.BigEndian.PutUint64(frame[8:16], sequence)
	binary.BigEndian.PutUint32(frame[16:20], uint32(payloadSize))
	for index := 0; index < payloadSize; index++ {
		frame[headerSize+index] = expectedPayloadByte(sequence, index)
	}
}

func validateResponseFrame(frame []byte, sequence uint64, payloadSize int) error {
	if len(frame) != headerSize+payloadSize {
		return fmt.Errorf("frame length %d, want %d", len(frame), headerSize+payloadSize)
	}
	switch {
	case binary.BigEndian.Uint16(frame[0:2]) != 0x4b50:
		return errors.New("invalid magic")
	case frame[2] != 1:
		return fmt.Errorf("version %d, want 1", frame[2])
	case frame[3] != 0:
		return fmt.Errorf("flags %d, want 0", frame[3])
	case binary.BigEndian.Uint32(frame[4:8]) != echoMessageID:
		return fmt.Errorf("message id %d, want %d", binary.BigEndian.Uint32(frame[4:8]), echoMessageID)
	case binary.BigEndian.Uint64(frame[8:16]) != sequence:
		return fmt.Errorf("sequence %d, want %d", binary.BigEndian.Uint64(frame[8:16]), sequence)
	case binary.BigEndian.Uint32(frame[16:20]) != uint32(payloadSize):
		return fmt.Errorf("payload length %d, want %d", binary.BigEndian.Uint32(frame[16:20]), payloadSize)
	}
	for index, value := range frame[headerSize:] {
		if want := expectedPayloadByte(sequence, index); value != want {
			return fmt.Errorf("payload byte %d = %d, want %d for sequence %d", index, value, want, sequence)
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

func recordSent(result *workerStats, slow bool, count uint64) {
	if slow {
		result.slowSent += count
		return
	}
	result.healthySent += count
}

func recordTimeout(result *workerStats, slow bool) {
	if slow {
		result.slowTimeout++
		return
	}
	result.healthyTimeout++
}

func recordIOFailure(result *workerStats, slow bool, err error) {
	if isMeasurementTimeout(err) {
		recordTimeout(result, slow)
		return
	}
	if slow {
		result.slowIOError++
	} else {
		result.healthyIOError++
	}
	setFirstError(result, err.Error())
}

func isMeasurementTimeout(err error) bool {
	var networkError net.Error
	return (errors.As(err, &networkError) && networkError.Timeout()) ||
		errors.Is(err, os.ErrDeadlineExceeded)
}

func setFirstError(result *workerStats, text string) {
	if result.firstError == "" {
		result.firstError = text
	}
}

func summarize(config loadConfig, slowCount int, wallElapsed time.Duration, results []workerStats) loadSummary {
	summary := loadSummary{
		config:       config,
		healthyConns: config.connections - slowCount,
		slowConns:    slowCount,
		wallElapsed:  wallElapsed,
		samples:      make([]int64, 0, config.maxSamples),
	}
	for _, result := range results {
		summary.healthySent += result.healthySent
		summary.slowSent += result.slowSent
		summary.completed += result.completed
		summary.bytesWritten += result.bytesWritten
		summary.validationErrors += result.validationError
		summary.healthyIOErrors += result.healthyIOError
		summary.slowIOErrors += result.slowIOError
		summary.healthyTimeouts += result.healthyTimeout
		summary.slowTimeouts += result.slowTimeout
		summary.samples = append(summary.samples, result.samples...)
		if summary.firstError == "" {
			summary.firstError = result.firstError
		}
	}
	if summary.healthySent >= summary.completed {
		summary.incomplete = summary.healthySent - summary.completed
	}
	sort.Slice(summary.samples, func(left, right int) bool {
		return summary.samples[left] < summary.samples[right]
	})
	return summary
}

func (s loadSummary) exitCode() int {
	if s.validationErrors > 0 || s.healthyIOErrors > 0 {
		return 1
	}
	if s.healthyConns > 0 && s.completed == 0 {
		return 1
	}
	return 0
}

func printSummary(summary loadSummary) {
	percentile := func(fraction float64) float64 {
		if len(summary.samples) == 0 {
			return 0
		}
		index := int(float64(len(summary.samples)-1) * fraction)
		return float64(summary.samples[index]) / 1_000
	}
	ioErrors := summary.healthyIOErrors + summary.slowIOErrors
	timeouts := summary.healthyTimeouts + summary.slowTimeouts
	throughput := float64(summary.completed) / summary.config.duration.Seconds()
	fmt.Printf(
		"connections=%d healthy_connections=%d slow_connections=%d payload=%d duration_s=%.3f wall_s=%.3f healthy_sent=%d slow_sent=%d completed=%d incomplete=%d validation_errors=%d io_errors=%d healthy_io_errors=%d slow_io_errors=%d timeouts=%d healthy_timeouts=%d slow_timeouts=%d bytes=%d throughput_msg_s=%.0f p50_us=%.2f p99_us=%.2f p999_us=%.2f samples=%d\n",
		summary.config.connections,
		summary.healthyConns,
		summary.slowConns,
		summary.config.payloadSize,
		summary.config.duration.Seconds(),
		summary.wallElapsed.Seconds(),
		summary.healthySent,
		summary.slowSent,
		summary.completed,
		summary.incomplete,
		summary.validationErrors,
		ioErrors,
		summary.healthyIOErrors,
		summary.slowIOErrors,
		timeouts,
		summary.healthyTimeouts,
		summary.slowTimeouts,
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
	return reservoir{
		values: make([]int64, 0, capacity),
		state:  seed,
	}
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

func sampleBudgetsForWorkers(total, connections, slowCount int) []int {
	budgets := make([]int, connections)
	healthy := connections - slowCount
	healthyBudgets := allocateSampleBudgets(total, healthy)
	for index := 0; index < healthy; index++ {
		budgets[slowCount+index] = healthyBudgets[index]
	}
	return budgets
}

func allocateSampleBudgets(total, connections int) []int {
	if connections <= 0 {
		return nil
	}
	budgets := make([]int, connections)
	if total <= 0 {
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

func slowConnectionCount(connections, percent int) int {
	if connections <= 0 || percent <= 0 {
		return 0
	}
	return int((uint64(connections)*uint64(percent) + 99) / 100)
}

func closeConnections(conns []net.Conn) {
	for _, conn := range conns {
		_ = conn.Close()
	}
}
