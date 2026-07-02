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

type Config struct {
	Name                string
	Host                string
	Port                int
	MetricsHost         string
	MetricsPort         int
	EnableMetrics       bool
	MaxConnections      int
	MaxConnectionsPerIP int
	MaxFrameSize        uint32
	SendQueueSize       int
	WorkerCount         int
	WorkerQueueSize     int
	ReadTimeout         time.Duration
	WriteTimeout        time.Duration
	IdleTimeout         time.Duration
	SweepInterval       time.Duration
	MaxInflightPerConn  int32
	Protocol            protocol.StreamProtocol
}

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
