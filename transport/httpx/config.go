package httpx

import (
	"errors"
	"log"
	"net/http"
	"time"
)

const (
	DefaultAddr            = ":8080"
	DefaultShutdownTimeout = 5 * time.Second
	DefaultMaxBodyBytes    = 4 << 20
)

type Config struct {
	Addr            string
	TLS             bool
	CertFile        string
	KeyFile         string
	ReadTimeout     time.Duration
	WriteTimeout    time.Duration
	IdleTimeout     time.Duration
	ShutdownTimeout time.Duration
	MaxBodyBytes    int64
	Logger          interface {
		Printf(string, ...interface{})
	}
}

func normalizeConfig(config Config) Config {
	if config.Addr == "" {
		config.Addr = DefaultAddr
	}
	if config.ShutdownTimeout <= 0 {
		config.ShutdownTimeout = DefaultShutdownTimeout
	}
	if config.MaxBodyBytes == 0 {
		config.MaxBodyBytes = DefaultMaxBodyBytes
	}
	return config
}

func ignoreServerClosed(err error) error {
	if errors.Is(err, http.ErrServerClosed) {
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
