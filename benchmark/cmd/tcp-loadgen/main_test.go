package main

import (
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestWriteFullHandlesPartialWrites(t *testing.T) {
	writer := &limitedWriter{limit: 2}
	input := []byte("partial-write")
	written, err := writeFull(writer, input)
	if err != nil {
		t.Fatalf("writeFull() error = %v", err)
	}
	if written != len(input) {
		t.Fatalf("writeFull() bytes = %d, want %d", written, len(input))
	}
	if !slices.Equal(writer.data, input) {
		t.Fatalf("written data = %q, want %q", writer.data, input)
	}
	if writer.calls <= 1 {
		t.Fatalf("Write() calls = %d, want multiple partial writes", writer.calls)
	}
}

func TestWriteFullReportsPartialProgressOnError(t *testing.T) {
	wantErr := errors.New("write failed")
	writer := &errorAfterWriter{limit: 3, err: wantErr}
	written, err := writeFull(writer, []byte("payload"))
	if written != 3 {
		t.Fatalf("writeFull() bytes = %d, want 3", written)
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("writeFull() error = %v, want %v", err, wantErr)
	}
}

func TestWriteFullRejectsZeroProgress(t *testing.T) {
	written, err := writeFull(zeroWriter{}, []byte("data"))
	if written != 0 {
		t.Fatalf("writeFull() bytes = %d, want 0", written)
	}
	if !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("writeFull() error = %v, want %v", err, io.ErrShortWrite)
	}
}

func TestRequestFrameRoundTripValidation(t *testing.T) {
	const (
		sequence    = uint64(0x0102030405060708)
		payloadSize = 64
	)
	frame := make([]byte, headerSize+payloadSize)
	encodeRequestFrame(frame, sequence, payloadSize)
	if err := validateResponseFrame(frame, sequence, payloadSize); err != nil {
		t.Fatalf("validateResponseFrame() error = %v", err)
	}

	next := make([]byte, len(frame))
	encodeRequestFrame(next, sequence+1, payloadSize)
	if slices.Equal(frame[headerSize:], next[headerSize:]) {
		t.Fatal("payload did not vary with sequence")
	}
}

func TestResponseValidationChecksEveryFieldAndPayload(t *testing.T) {
	const (
		sequence    = uint64(17)
		payloadSize = 16
	)
	valid := make([]byte, headerSize+payloadSize)
	encodeRequestFrame(valid, sequence, payloadSize)
	tests := []struct {
		name   string
		mutate func([]byte)
		want   string
	}{
		{name: "magic", mutate: func(frame []byte) { frame[0] = 0 }, want: "magic"},
		{name: "version", mutate: func(frame []byte) { frame[2]++ }, want: "version"},
		{name: "flags", mutate: func(frame []byte) { frame[3] = 1 }, want: "flags"},
		{name: "message id", mutate: func(frame []byte) { frame[7]++ }, want: "message id"},
		{name: "sequence", mutate: func(frame []byte) { frame[15]++ }, want: "sequence"},
		{name: "payload length", mutate: func(frame []byte) { frame[19]++ }, want: "payload length"},
		{name: "payload", mutate: func(frame []byte) { frame[headerSize+9] ^= 0xff }, want: "payload byte"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			frame := slices.Clone(valid)
			test.mutate(frame)
			err := validateResponseFrame(frame, sequence, payloadSize)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validateResponseFrame() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestReservoirSamplesAcrossTheWholeStream(t *testing.T) {
	reservoir := newReservoir(8, 7)
	for value := int64(1); value <= 1_000; value++ {
		reservoir.add(value)
	}
	if reservoir.seen != 1_000 {
		t.Fatalf("reservoir.seen = %d, want 1000", reservoir.seen)
	}
	if len(reservoir.values) != 8 {
		t.Fatalf("reservoir samples = %d, want 8", len(reservoir.values))
	}
	late := false
	for _, value := range reservoir.values {
		if value > 8 {
			late = true
		}
	}
	if !late {
		t.Fatalf("reservoir retained only initial samples: %v", reservoir.values)
	}
}

func TestAllocateSampleBudgets(t *testing.T) {
	tests := []struct {
		name        string
		total       int
		connections int
		want        []int
	}{
		{name: "even", total: 12, connections: 3, want: []int{4, 4, 4}},
		{name: "remainder", total: 10, connections: 3, want: []int{4, 3, 3}},
		{name: "fewer samples", total: 2, connections: 5, want: []int{1, 1, 0, 0, 0}},
		{name: "zero samples", total: 0, connections: 3, want: []int{0, 0, 0}},
		{name: "zero connections", total: 10, connections: 0, want: nil},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := allocateSampleBudgets(test.total, test.connections)
			if !slices.Equal(got, test.want) {
				t.Fatalf("allocateSampleBudgets(%d, %d) = %v, want %v", test.total, test.connections, got, test.want)
			}
		})
	}
}

func TestSampleBudgetsExcludeSlowConnections(t *testing.T) {
	got := sampleBudgetsForWorkers(10, 5, 2)
	want := []int{0, 0, 4, 3, 3}
	if !slices.Equal(got, want) {
		t.Fatalf("sampleBudgetsForWorkers() = %v, want %v", got, want)
	}
}

func TestSlowConnectionCountRoundsUp(t *testing.T) {
	tests := []struct {
		connections int
		percent     int
		want        int
	}{
		{connections: 64, percent: 0, want: 0},
		{connections: 64, percent: 1, want: 1},
		{connections: 1_000, percent: 1, want: 10},
		{connections: 3, percent: 50, want: 2},
		{connections: 3, percent: 100, want: 3},
	}
	for _, test := range tests {
		if got := slowConnectionCount(test.connections, test.percent); got != test.want {
			t.Fatalf("slowConnectionCount(%d, %d) = %d, want %d", test.connections, test.percent, got, test.want)
		}
	}
}

func TestRunWindowUsesOneAbsoluteDeadline(t *testing.T) {
	startedAt := time.Unix(123, 456)
	window := newRunWindow(startedAt, 7*time.Second)
	if !window.startedAt.Equal(startedAt) {
		t.Fatalf("startedAt = %v, want %v", window.startedAt, startedAt)
	}
	if want := startedAt.Add(7 * time.Second); !window.deadline.Equal(want) {
		t.Fatalf("deadline = %v, want %v", window.deadline, want)
	}
}

func TestMeasurementTimeoutRequiresDeadlineError(t *testing.T) {
	if !isMeasurementTimeout(timeoutTestError{}) {
		t.Fatal("timeout net.Error was not classified as a measurement timeout")
	}
	if isMeasurementTimeout(io.EOF) {
		t.Fatal("EOF was incorrectly classified as a measurement timeout")
	}
}

func TestSummarizePreservesIncompleteAndErrorAccounting(t *testing.T) {
	config := loadConfig{connections: 2, duration: time.Second, maxSamples: 4}
	summary := summarize(config, 1, 1100*time.Millisecond, []workerStats{
		{
			healthySent:     10,
			completed:       8,
			bytesWritten:    100,
			validationError: 1,
			healthyTimeout:  1,
			samples:         []int64{2, 1},
			firstError:      "bad payload",
		},
		{
			slowSent:     20,
			bytesWritten: 200,
			slowIOError:  1,
			slowTimeout:  1,
			firstError:   "slow reset",
		},
	})
	if summary.healthySent != 10 ||
		summary.slowSent != 20 ||
		summary.completed != 8 ||
		summary.incomplete != 2 ||
		summary.bytesWritten != 300 ||
		summary.validationErrors != 1 ||
		summary.slowIOErrors != 1 ||
		summary.healthyTimeouts != 1 ||
		summary.slowTimeouts != 1 {
		t.Fatalf("summary = %+v", summary)
	}
	if !slices.Equal(summary.samples, []int64{1, 2}) {
		t.Fatalf("sorted samples = %v, want [1 2]", summary.samples)
	}
	if summary.firstError != "bad payload" {
		t.Fatalf("firstError = %q, want bad payload", summary.firstError)
	}
}

func TestSummaryExitCodeRejectsInvalidMeasurements(t *testing.T) {
	tests := []struct {
		name    string
		summary loadSummary
		want    int
	}{
		{name: "valid", summary: loadSummary{healthyConns: 1, completed: 1}, want: 0},
		{name: "validation", summary: loadSummary{healthyConns: 1, completed: 1, validationErrors: 1}, want: 1},
		{name: "healthy io", summary: loadSummary{healthyConns: 1, completed: 1, healthyIOErrors: 1}, want: 1},
		{name: "no completions", summary: loadSummary{healthyConns: 1}, want: 1},
		{name: "all slow", summary: loadSummary{slowConns: 1, slowIOErrors: 1}, want: 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.summary.exitCode(); got != test.want {
				t.Fatalf("exitCode() = %d, want %d", got, test.want)
			}
		})
	}
}

type limitedWriter struct {
	limit int
	calls int
	data  []byte
}

func (w *limitedWriter) Write(buffer []byte) (int, error) {
	w.calls++
	written := min(w.limit, len(buffer))
	w.data = append(w.data, buffer[:written]...)
	return written, nil
}

type errorAfterWriter struct {
	limit int
	err   error
}

func (w *errorAfterWriter) Write(buffer []byte) (int, error) {
	return min(w.limit, len(buffer)), w.err
}

type zeroWriter struct{}

func (zeroWriter) Write([]byte) (int, error) {
	return 0, nil
}

type timeoutTestError struct{}

func (timeoutTestError) Error() string   { return "timeout" }
func (timeoutTestError) Timeout() bool   { return true }
func (timeoutTestError) Temporary() bool { return false }
