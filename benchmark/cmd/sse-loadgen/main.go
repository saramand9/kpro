// Command sse-loadgen consumes validated SSE streams for local implementation
// comparisons. It measures push throughput, inter-event gaps, and stream
// fairness; SSE has no request inflight or request/response latency metric.
package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"sort"
	"sync"
	"time"
)

const maxPayloadSize = 64 * 1024

var (
	eventLine  = []byte("event: message\n")
	dataPrefix = []byte("data: ")
	blankLine  = []byte("\n")
)

type loadConfig struct {
	addr        string
	streams     int
	payloadSize int
	duration    time.Duration
	startDelay  time.Duration
	maxSamples  int
}

type workerStats struct {
	completed       uint64
	bytes           uint64
	validationError uint64
	ioError         uint64
	timeout         uint64
	samples         []int64
	firstError      string
}

type loadSummary struct {
	config           loadConfig
	wallElapsed      time.Duration
	completed        uint64
	bytes            uint64
	validationErrors uint64
	ioErrors         uint64
	timeouts         uint64
	minStreamEvents  uint64
	maxStreamEvents  uint64
	samples          []int64
	firstError       string
}

type openResult struct {
	index    int
	response *http.Response
	err      error
}

func main() {
	addr := flag.String("addr", "127.0.0.1:18501", "server address")
	streams := flag.Int("streams", 1, "concurrent SSE streams")
	payloadSize := flag.Int("payload", 64, "application data bytes per event")
	duration := flag.Duration("duration", 3*time.Second, "measurement duration")
	startDelay := flag.Duration("start-delay", 2*time.Second, "delay allowing all streams to connect")
	maxSamples := flag.Int("max-samples", 200_000, "maximum inter-event gap samples")
	flag.Parse()

	config := loadConfig{
		addr:        *addr,
		streams:     *streams,
		payloadSize: *payloadSize,
		duration:    *duration,
		startDelay:  *startDelay,
		maxSamples:  *maxSamples,
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
	case c.streams < 1:
		return errors.New("streams must be positive")
	case c.payloadSize < 16 || c.payloadSize > maxPayloadSize:
		return fmt.Errorf("payload must be between 16 and %d", maxPayloadSize)
	case c.duration <= 0:
		return errors.New("duration must be positive")
	case c.startDelay < 500*time.Millisecond:
		return errors.New("start-delay must be at least 500ms")
	case c.maxSamples < 0:
		return errors.New("max-samples must not be negative")
	default:
		return nil
	}
}

func runLoad(config loadConfig) (loadSummary, error) {
	startAt := time.Now().Add(config.startDelay)
	deadline := startAt.Add(config.duration)
	requestCtx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()

	transport := &http.Transport{
		Proxy:                 nil,
		DialContext:           (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     false,
		DisableCompression:    true,
		MaxConnsPerHost:       config.streams,
		MaxIdleConns:          config.streams,
		MaxIdleConnsPerHost:   config.streams,
		ResponseHeaderTimeout: 5 * time.Second,
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}

	responses := make([]*http.Response, config.streams)
	opened := make(chan openResult, config.streams)
	for index := 0; index < config.streams; index++ {
		go func(streamID int) {
			url := fmt.Sprintf(
				"http://%s/events?payload=%d&stream=%d&start_ns=%d",
				config.addr, config.payloadSize, streamID, startAt.UnixNano(),
			)
			request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, url, nil)
			if err == nil {
				request.Header.Set("Accept", "text/event-stream")
			}
			var response *http.Response
			if err == nil {
				response, err = client.Do(request)
			}
			if err == nil && response.StatusCode != http.StatusOK {
				err = fmt.Errorf("status %s", response.Status)
			}
			if err == nil && response.Header.Get("Content-Type") != "text/event-stream" {
				err = fmt.Errorf("content type %q", response.Header.Get("Content-Type"))
			}
			opened <- openResult{index: streamID, response: response, err: err}
		}(index)
	}
	for range config.streams {
		result := <-opened
		if result.err != nil {
			closeResponses(responses)
			return loadSummary{}, fmt.Errorf("open stream %d: %w", result.index, result.err)
		}
		responses[result.index] = result.response
	}
	if remaining := time.Until(startAt); remaining < 250*time.Millisecond {
		closeResponses(responses)
		return loadSummary{}, fmt.Errorf("stream setup left only %s before synchronized start", remaining)
	}

	budgets := allocateSampleBudgets(config.maxSamples, config.streams)
	results := make([]workerStats, config.streams)
	var ready sync.WaitGroup
	var workers sync.WaitGroup
	start := make(chan struct{})
	ready.Add(config.streams)
	workers.Add(config.streams)
	for index, response := range responses {
		go runWorker(config, index, response, startAt, deadline, budgets[index], start, &ready, &workers, &results[index])
	}
	ready.Wait()
	close(start)
	workers.Wait()
	closeResponses(responses)
	return summarize(config, time.Since(startAt), results), nil
}

func runWorker(
	config loadConfig,
	streamID int,
	response *http.Response,
	startAt time.Time,
	deadline time.Time,
	sampleBudget int,
	start <-chan struct{},
	ready *sync.WaitGroup,
	workers *sync.WaitGroup,
	result *workerStats,
) {
	defer workers.Done()
	defer response.Body.Close()
	reader := bufio.NewReaderSize(response.Body, 128*1024)
	latencies := newReservoir(sampleBudget, uint64(streamID+1))
	var expectedSequence uint64 = 1
	var previous time.Time
	ready.Done()
	<-start

	for {
		validationFailure, err := readAndValidateEvent(reader, config.payloadSize, streamID, expectedSequence)
		observedAt := time.Now()
		if err != nil {
			if validationFailure {
				result.validationError++
				setFirstError(result, fmt.Sprintf("stream %d: %v", streamID, err))
			} else if !observedAt.Before(deadline.Add(-50*time.Millisecond)) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
				result.timeout++
			} else {
				result.ioError++
				setFirstError(result, err.Error())
			}
			break
		}
		expectedSequence++
		if observedAt.Before(startAt) {
			continue
		}
		if !observedAt.Before(deadline) {
			result.timeout++
			break
		}
		result.completed++
		result.bytes += uint64(config.payloadSize)
		if !previous.IsZero() {
			latencies.add(observedAt.Sub(previous).Nanoseconds())
		}
		previous = observedAt
	}
	result.samples = latencies.values
}

func readAndValidateEvent(reader *bufio.Reader, payloadSize, streamID int, expectedSequence uint64) (bool, error) {
	line, err := reader.ReadSlice('\n')
	if err != nil {
		return false, err
	}
	if !bytes.Equal(line, eventLine) {
		return true, fmt.Errorf("event line %q", line)
	}
	line, err = reader.ReadSlice('\n')
	if err != nil {
		return false, err
	}
	if len(line) != len(dataPrefix)+payloadSize+1 || !bytes.Equal(line[:len(dataPrefix)], dataPrefix) || line[len(line)-1] != '\n' {
		return true, fmt.Errorf("invalid data line length or prefix")
	}
	payload := line[len(dataPrefix) : len(line)-1]
	if err := validatePayload(payload, streamID, expectedSequence); err != nil {
		return true, err
	}
	line, err = reader.ReadSlice('\n')
	if err != nil {
		return false, err
	}
	if !bytes.Equal(line, blankLine) {
		return true, fmt.Errorf("event terminator %q", line)
	}
	return false, nil
}

func validatePayload(payload []byte, streamID int, expectedSequence uint64) error {
	if len(payload) < 16 {
		return io.ErrUnexpectedEOF
	}
	sequence, ok := parseHexSequence(payload[:16])
	if !ok {
		return errors.New("invalid hexadecimal sequence")
	}
	if sequence != expectedSequence {
		return fmt.Errorf("sequence %d, want %d", sequence, expectedSequence)
	}
	for index := 16; index < len(payload); index++ {
		want := byte('a' + (index+streamID)%26)
		if payload[index] != want {
			return fmt.Errorf("payload byte %d = %d, want %d", index, payload[index], want)
		}
	}
	return nil
}

func parseHexSequence(data []byte) (uint64, bool) {
	var value uint64
	for _, digit := range data {
		value <<= 4
		switch {
		case digit >= '0' && digit <= '9':
			value |= uint64(digit - '0')
		case digit >= 'a' && digit <= 'f':
			value |= uint64(digit-'a') + 10
		default:
			return 0, false
		}
	}
	return value, true
}

func summarize(config loadConfig, wallElapsed time.Duration, results []workerStats) loadSummary {
	summary := loadSummary{
		config:          config,
		wallElapsed:     wallElapsed,
		minStreamEvents: ^uint64(0),
		samples:         make([]int64, 0, config.maxSamples),
	}
	for _, result := range results {
		summary.completed += result.completed
		summary.bytes += result.bytes
		summary.validationErrors += result.validationError
		summary.ioErrors += result.ioError
		summary.timeouts += result.timeout
		summary.minStreamEvents = min(summary.minStreamEvents, result.completed)
		summary.maxStreamEvents = max(summary.maxStreamEvents, result.completed)
		summary.samples = append(summary.samples, result.samples...)
		if summary.firstError == "" {
			summary.firstError = result.firstError
		}
	}
	if len(results) == 0 {
		summary.minStreamEvents = 0
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
	eventsPerSecond := float64(summary.completed) / summary.config.duration.Seconds()
	mebibytesPerSecond := float64(summary.bytes) / summary.config.duration.Seconds() / (1024 * 1024)
	meanPerStream := float64(summary.completed) / float64(summary.config.streams)
	minMeanPercent := 0.0
	maxMeanPercent := 0.0
	if meanPerStream > 0 {
		minMeanPercent = float64(summary.minStreamEvents) / meanPerStream * 100
		maxMeanPercent = float64(summary.maxStreamEvents) / meanPerStream * 100
	}
	fmt.Printf(
		"streams=%d payload=%d duration_s=%.3f wall_s=%.3f completed=%d bytes=%d events_s=%.0f payload_mib_s=%.3f gap_p50_us=%.2f gap_p99_us=%.2f gap_p999_us=%.2f min_stream_events=%d max_stream_events=%d min_mean_pct=%.2f max_mean_pct=%.2f validation_errors=%d io_errors=%d timeouts=%d samples=%d\n",
		summary.config.streams,
		summary.config.payloadSize,
		summary.config.duration.Seconds(),
		summary.wallElapsed.Seconds(),
		summary.completed,
		summary.bytes,
		eventsPerSecond,
		mebibytesPerSecond,
		percentile(0.50),
		percentile(0.99),
		percentile(0.999),
		summary.minStreamEvents,
		summary.maxStreamEvents,
		minMeanPercent,
		maxMeanPercent,
		summary.validationErrors,
		summary.ioErrors,
		summary.timeouts,
		len(summary.samples),
	)
}

func allocateSampleBudgets(total, streams int) []int {
	budgets := make([]int, streams)
	if total <= 0 || streams <= 0 {
		return budgets
	}
	base := total / streams
	remainder := total % streams
	for index := range budgets {
		budgets[index] = base
		if index < remainder {
			budgets[index]++
		}
	}
	return budgets
}

func closeResponses(responses []*http.Response) {
	for _, response := range responses {
		if response != nil {
			_ = response.Body.Close()
		}
	}
}

func setFirstError(result *workerStats, text string) {
	if result.firstError == "" {
		result.firstError = text
	}
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
