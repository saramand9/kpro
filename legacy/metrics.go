// File: legacy/metrics.go
// Purpose: Implements or tests the legacy TCP validation server.

package legacy

import (
	"encoding/json"
	"net/http"
	_ "net/http/pprof"
	"runtime"
	"sync/atomic"
	"time"
)

// Metrics collects legacy TCP counters for JSON export.
type Metrics struct {
	startUnix int64

	activeConnections   int64
	totalConnections    int64
	rejectedConnections int64
	closedConnections   int64
	rejectedMaxConns    int64
	rejectedPerIP       int64

	framesIn  int64
	framesOut int64
	bytesIn   int64
	bytesOut  int64

	readErrors       int64
	writeErrors      int64
	decodeErrors     int64
	handlerErrors    int64
	workerQueueFull  int64
	sendQueueFull    int64
	inflightRejected int64
	panicRecovered   int64
}

// MetricsSnapshot is the JSON-serializable view of legacy server counters.
type MetricsSnapshot struct {
	// UptimeSeconds is elapsed wall time since Metrics creation.
	UptimeSeconds int64 `json:"uptime_seconds"`
	// ActiveConnections is the current number of open TCP connections.
	ActiveConnections int64 `json:"active_connections"`
	// TotalConnections is the cumulative accepted connection count.
	TotalConnections int64 `json:"total_connections"`
	// RejectedConnections is the cumulative rejected connection count.
	RejectedConnections int64 `json:"rejected_connections"`
	// RejectedMaxConns counts connections rejected by MaxConnections.
	RejectedMaxConns int64 `json:"rejected_max_conns"`
	// RejectedPerIP counts connections rejected by MaxConnectionsPerIP.
	RejectedPerIP int64 `json:"rejected_per_ip"`
	// ClosedConnections is the cumulative closed connection count.
	ClosedConnections int64 `json:"closed_connections"`
	// FramesIn is the cumulative decoded inbound frame count.
	FramesIn int64 `json:"frames_in"`
	// FramesOut is the cumulative outbound frame count.
	FramesOut int64 `json:"frames_out"`
	// BytesIn is the cumulative inbound wire byte count.
	BytesIn int64 `json:"bytes_in"`
	// BytesOut is the cumulative outbound wire byte count.
	BytesOut int64 `json:"bytes_out"`
	// ReadErrors counts frame read failures.
	ReadErrors int64 `json:"read_errors"`
	// WriteErrors counts frame write failures.
	WriteErrors int64 `json:"write_errors"`
	// DecodeErrors counts protocol decode failures.
	DecodeErrors int64 `json:"decode_errors"`
	// HandlerErrors counts handler and middleware errors.
	HandlerErrors int64 `json:"handler_errors"`
	// WorkerQueueFull counts requests rejected by a full worker queue.
	WorkerQueueFull int64 `json:"worker_queue_full"`
	// SendQueueFull counts connections closed because their send queue filled.
	SendQueueFull int64 `json:"send_queue_full"`
	// InflightRejected counts connections closed after exceeding in-flight limits.
	InflightRejected int64 `json:"inflight_rejected"`
	// PanicRecovered counts handler panics converted into errors.
	PanicRecovered int64 `json:"panic_recovered"`
	// Goroutines is the runtime goroutine count at snapshot time.
	Goroutines int `json:"goroutines"`
	// HeapAllocBytes is runtime.MemStats.HeapAlloc at snapshot time.
	HeapAllocBytes uint64 `json:"heap_alloc_bytes"`
	// HeapSysBytes is runtime.MemStats.HeapSys at snapshot time.
	HeapSysBytes uint64 `json:"heap_sys_bytes"`
	// NumGC is runtime.MemStats.NumGC at snapshot time.
	NumGC uint32 `json:"num_gc"`
}

// NewMetrics creates a Metrics collector with the current start time.
func NewMetrics() *Metrics {
	return &Metrics{startUnix: time.Now().Unix()}
}

// Snapshot returns an atomic point-in-time metrics view.
func (m *Metrics) Snapshot() MetricsSnapshot {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return MetricsSnapshot{
		UptimeSeconds:       time.Now().Unix() - atomic.LoadInt64(&m.startUnix),
		ActiveConnections:   atomic.LoadInt64(&m.activeConnections),
		TotalConnections:    atomic.LoadInt64(&m.totalConnections),
		RejectedConnections: atomic.LoadInt64(&m.rejectedConnections),
		RejectedMaxConns:    atomic.LoadInt64(&m.rejectedMaxConns),
		RejectedPerIP:       atomic.LoadInt64(&m.rejectedPerIP),
		ClosedConnections:   atomic.LoadInt64(&m.closedConnections),
		FramesIn:            atomic.LoadInt64(&m.framesIn),
		FramesOut:           atomic.LoadInt64(&m.framesOut),
		BytesIn:             atomic.LoadInt64(&m.bytesIn),
		BytesOut:            atomic.LoadInt64(&m.bytesOut),
		ReadErrors:          atomic.LoadInt64(&m.readErrors),
		WriteErrors:         atomic.LoadInt64(&m.writeErrors),
		DecodeErrors:        atomic.LoadInt64(&m.decodeErrors),
		HandlerErrors:       atomic.LoadInt64(&m.handlerErrors),
		WorkerQueueFull:     atomic.LoadInt64(&m.workerQueueFull),
		SendQueueFull:       atomic.LoadInt64(&m.sendQueueFull),
		InflightRejected:    atomic.LoadInt64(&m.inflightRejected),
		PanicRecovered:      atomic.LoadInt64(&m.panicRecovered),
		Goroutines:          runtime.NumGoroutine(),
		HeapAllocBytes:      ms.HeapAlloc,
		HeapSysBytes:        ms.HeapSys,
		NumGC:               ms.NumGC,
	}
}

// ServeHTTP writes the metrics snapshot as JSON.
func (m *Metrics) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(m.Snapshot())
}

func (m *Metrics) incActive() {
	atomic.AddInt64(&m.activeConnections, 1)
	atomic.AddInt64(&m.totalConnections, 1)
}

func (m *Metrics) decActive() {
	atomic.AddInt64(&m.activeConnections, -1)
	atomic.AddInt64(&m.closedConnections, 1)
}
