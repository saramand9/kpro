package httpx

import (
	"errors"
	"log"
	"net"
	"net/http"
	"time"
)

const (
	DefaultAddr              = ":8080"
	DefaultReadHeaderTimeout = 5 * time.Second
	DefaultReadTimeout       = 30 * time.Second
	DefaultWriteTimeout      = 30 * time.Second
	DefaultIdleTimeout       = 60 * time.Second
	DefaultShutdownTimeout   = 5 * time.Second
	DefaultMaxHeaderBytes    = 1 << 20
	DefaultMaxBodyBytes      = 4 << 20
)

type Config struct {
	Addr              string
	TLS               bool
	CertFile          string
	KeyFile           string
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	ShutdownTimeout   time.Duration
	MaxHeaderBytes    int
	MaxBodyBytes      int64
	Logger            interface {
		Printf(string, ...interface{})
	}
}

func normalizeConfig(config Config) Config {
	if config.Addr == "" {
		config.Addr = DefaultAddr
	}
	if config.ReadHeaderTimeout == 0 {
		config.ReadHeaderTimeout = DefaultReadHeaderTimeout
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
	if config.MaxHeaderBytes == 0 {
		config.MaxHeaderBytes = DefaultMaxHeaderBytes
	}
	if config.MaxBodyBytes == 0 {
		config.MaxBodyBytes = DefaultMaxBodyBytes
	}
	return config
}

func ignoreServerClosed(err error) error {
	if errors.Is(err, http.ErrServerClosed) || errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}

func errorLogger(logger interface {
	Printf(string, ...interface{})
}) *log.Logger {
	if logger == nil {
		return nil
	}
	return log.New(&logWriter{logger: logger}, "", 0)
}

type logWriter struct {
	logger interface {
		Printf(string, ...interface{})
	}
}

func (l *logWriter) Write(p []byte) (int, error) {
	l.logger.Printf("%s", string(p))
	return len(p), nil
}
