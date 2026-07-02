package legacy

import (
	"encoding/json"
	"net/http"
	_ "net/http/pprof"
	"runtime"
	"sync/atomic"
	"time"
)

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

type MetricsSnapshot struct {
	UptimeSeconds       int64  `json:"uptime_seconds"`
	ActiveConnections   int64  `json:"active_connections"`
	TotalConnections    int64  `json:"total_connections"`
	RejectedConnections int64  `json:"rejected_connections"`
	RejectedMaxConns    int64  `json:"rejected_max_conns"`
	RejectedPerIP       int64  `json:"rejected_per_ip"`
	ClosedConnections   int64  `json:"closed_connections"`
	FramesIn            int64  `json:"frames_in"`
	FramesOut           int64  `json:"frames_out"`
	BytesIn             int64  `json:"bytes_in"`
	BytesOut            int64  `json:"bytes_out"`
	ReadErrors          int64  `json:"read_errors"`
	WriteErrors         int64  `json:"write_errors"`
	DecodeErrors        int64  `json:"decode_errors"`
	HandlerErrors       int64  `json:"handler_errors"`
	WorkerQueueFull     int64  `json:"worker_queue_full"`
	SendQueueFull       int64  `json:"send_queue_full"`
	InflightRejected    int64  `json:"inflight_rejected"`
	PanicRecovered      int64  `json:"panic_recovered"`
	Goroutines          int    `json:"goroutines"`
	HeapAllocBytes      uint64 `json:"heap_alloc_bytes"`
	HeapSysBytes        uint64 `json:"heap_sys_bytes"`
	NumGC               uint32 `json:"num_gc"`
}

func NewMetrics() *Metrics {
	return &Metrics{startUnix: time.Now().Unix()}
}

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
