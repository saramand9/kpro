package fasthttpx

import "time"

const (
	DefaultAddr            = ":8080"
	DefaultReadTimeout     = 30 * time.Second
	DefaultWriteTimeout    = 30 * time.Second
	DefaultIdleTimeout     = 60 * time.Second
	DefaultShutdownTimeout = 5 * time.Second
	DefaultReadBufferSize  = 4 << 10
	DefaultWriteBufferSize = 4 << 10
	DefaultMaxBodyBytes    = 4 << 20
)

// Config controls the optional fasthttp-backed HTTP/1.1 endpoint.
type Config struct {
	Addr     string
	TLS      bool
	CertFile string
	KeyFile  string

	ReadTimeout     time.Duration
	WriteTimeout    time.Duration
	IdleTimeout     time.Duration
	ShutdownTimeout time.Duration

	// ReadBufferSize is the per-connection request buffer and therefore also
	// the maximum request-line and header size accepted by fasthttp.
	ReadBufferSize  int
	WriteBufferSize int
	MaxBodyBytes    int64

	// Concurrency caps concurrently served connections. Zero uses fasthttp's
	// default. MaxConnsPerIP and MaxRequestsPerConn use zero for unlimited.
	Concurrency        int
	MaxConnsPerIP      int
	MaxRequestsPerConn int

	TCPKeepalive      bool
	ReduceMemoryUsage bool
	Logger            interface {
		Printf(string, ...any)
	}
}

func normalizeConfig(config Config) Config {
	if config.Addr == "" {
		config.Addr = DefaultAddr
	}
	if config.ReadTimeout == 0 {
		config.ReadTimeout = DefaultReadTimeout
	}
	if config.WriteTimeout == 0 {
		config.WriteTimeout = DefaultWriteTimeout
	}
	if config.IdleTimeout == 0 {
		config.IdleTimeout = DefaultIdleTimeout
	}
	if config.ShutdownTimeout == 0 {
		config.ShutdownTimeout = DefaultShutdownTimeout
	}
	if config.ReadBufferSize == 0 {
		config.ReadBufferSize = DefaultReadBufferSize
	}
	if config.WriteBufferSize == 0 {
		config.WriteBufferSize = DefaultWriteBufferSize
	}
	if config.MaxBodyBytes == 0 {
		config.MaxBodyBytes = DefaultMaxBodyBytes
	}
	return config
}
