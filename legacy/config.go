// File: legacy/config.go
// Purpose: Implements or tests the legacy TCP validation server.

package legacy

import (
	"time"

	"github.com/saramand9/bignet/kpro/protocol"
)

const (
	DefaultHost                = "0.0.0.0"
	DefaultPort                = 8088
	DefaultMetricsHost         = "127.0.0.1"
	DefaultMetricsPort         = 9090
	DefaultMaxConnections      = 10000
	DefaultMaxConnectionsPerIP = 0
	DefaultMaxFrameSize        = 64 * 1024
	DefaultSendQueueSize       = 64
	DefaultWorkerCount         = 0
	DefaultWorkerQueueSize     = 16384
	DefaultReadTimeout         = 90 * time.Second
	DefaultWriteTimeout        = 5 * time.Second
	DefaultIdleTimeout         = 2 * time.Minute
	DefaultSweepInterval       = time.Second
	DefaultMaxInflightPerConn  = 32
)

// Config controls the legacy TCP validation server.
type Config struct {
	// Name labels log and metrics output.
	Name string
	// Host is the TCP listen host.
	Host string
	// Port is the TCP listen port.
	Port int
	// MetricsHost is the HTTP metrics listen host.
	MetricsHost string
	// MetricsPort is the HTTP metrics listen port.
	MetricsPort int
	// EnableMetrics starts the metrics and pprof HTTP server when true.
	EnableMetrics bool
	// MaxConnections caps active TCP connections.
	MaxConnections int
	// MaxConnectionsPerIP optionally caps active connections from one IP. Zero disables the limit.
	MaxConnectionsPerIP int
	// MaxFrameSize limits decoded frame payload size.
	MaxFrameSize uint32
	// SendQueueSize is the per-connection outbound frame queue length.
	SendQueueSize int
	// WorkerCount is the number of request workers. Values <= 0 derive from GOMAXPROCS.
	WorkerCount int
	// WorkerQueueSize is the shared request task queue length.
	WorkerQueueSize int
	// ReadTimeout is applied before each frame read.
	ReadTimeout time.Duration
	// WriteTimeout is applied before each frame write.
	WriteTimeout time.Duration
	// IdleTimeout closes connections that have not read frames recently.
	IdleTimeout time.Duration
	// SweepInterval controls how often idle connections are checked.
	SweepInterval time.Duration
	// MaxInflightPerConn limits queued or running requests per connection.
	MaxInflightPerConn int32
	// Protocol decodes and encodes frames. Nil uses protocol.KProV1().
	Protocol protocol.StreamProtocol
}

// DefaultConfig returns the legacy TCP validation defaults.
func DefaultConfig() Config {
	return Config{
		Name:                "kpro",
		Host:                DefaultHost,
		Port:                DefaultPort,
		MetricsHost:         DefaultMetricsHost,
		MetricsPort:         DefaultMetricsPort,
		EnableMetrics:       true,
		MaxConnections:      DefaultMaxConnections,
		MaxConnectionsPerIP: DefaultMaxConnectionsPerIP,
		MaxFrameSize:        DefaultMaxFrameSize,
		SendQueueSize:       DefaultSendQueueSize,
		WorkerCount:         DefaultWorkerCount,
		WorkerQueueSize:     DefaultWorkerQueueSize,
		ReadTimeout:         DefaultReadTimeout,
		WriteTimeout:        DefaultWriteTimeout,
		IdleTimeout:         DefaultIdleTimeout,
		SweepInterval:       DefaultSweepInterval,
		MaxInflightPerConn:  DefaultMaxInflightPerConn,
		Protocol:            protocol.KProV1(),
	}
}

func normalizeConfig(c Config) Config {
	if c.Name == "" {
		c.Name = "kpro"
	}
	if c.Host == "" {
		c.Host = DefaultHost
	}
	if c.Port == 0 {
		c.Port = DefaultPort
	}
	if c.MetricsHost == "" {
		c.MetricsHost = DefaultMetricsHost
	}
	if c.MetricsPort == 0 {
		c.MetricsPort = DefaultMetricsPort
	}
	if c.MaxConnections <= 0 {
		c.MaxConnections = DefaultMaxConnections
	}
	if c.MaxFrameSize == 0 {
		c.MaxFrameSize = DefaultMaxFrameSize
	}
	if c.SendQueueSize <= 0 {
		c.SendQueueSize = DefaultSendQueueSize
	}
	if c.WorkerQueueSize <= 0 {
		c.WorkerQueueSize = DefaultWorkerQueueSize
	}
	if c.ReadTimeout <= 0 {
		c.ReadTimeout = DefaultReadTimeout
	}
	if c.WriteTimeout <= 0 {
		c.WriteTimeout = DefaultWriteTimeout
	}
	if c.IdleTimeout <= 0 {
		c.IdleTimeout = DefaultIdleTimeout
	}
	if c.SweepInterval <= 0 {
		c.SweepInterval = DefaultSweepInterval
	}
	if c.MaxInflightPerConn <= 0 {
		c.MaxInflightPerConn = DefaultMaxInflightPerConn
	}
	if c.Protocol == nil {
		c.Protocol = protocol.KProV1()
	}
	return c
}
