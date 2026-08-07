// Package transportkit exposes the bounded validation and lifecycle helpers
// used by KPro's built-in transports so third-party transports can follow the
// same startup, cancellation, and stream-write contracts. Use the sibling
// transporttest package to verify the resulting live transport behavior.
package transportkit

import (
	"context"
	"crypto/tls"
	"io"
	"time"

	"github.com/saramand9/kpro/transport/internal/configcheck"
	"github.com/saramand9/kpro/transport/internal/serveutil"
	"github.com/saramand9/kpro/transport/internal/streamutil"
)

func ListenAddress(name, address string) error {
	return configcheck.ListenAddress(name, address)
}

func NonNegativeInt(name string, value int) error {
	return configcheck.NonNegativeInt(name, value)
}

func NonNegativeInt64(name string, value int64) error {
	return configcheck.NonNegativeInt64(name, value)
}

func NonNegativeDuration(name string, value time.Duration) error {
	return configcheck.NonNegativeDuration(name, value)
}

func IntRangeIfSet(name string, value, minimum, maximum int) error {
	return configcheck.IntRangeIfSet(name, value, minimum, maximum)
}

func HTTPPath(name, path string) error {
	return configcheck.HTTPPath(name, path)
}

func TypedNil(name string, value any) error {
	return configcheck.TypedNil(name, value)
}

func ALPN(name string, protocols []string) error {
	return configcheck.ALPN(name, protocols)
}

func TLSKeyPair(name, certFile, keyFile string) error {
	return configcheck.TLSKeyPair(name, certFile, keyFile)
}

func TLSConfig(name string, config *tls.Config) error {
	return configcheck.TLSConfig(name, config)
}

func ShutdownOnCancel(ctx context.Context, serveDone <-chan struct{}, shutdown func()) <-chan struct{} {
	return serveutil.ShutdownOnCancel(ctx, serveDone, shutdown)
}

func FinishContextWatcher(serveDone chan struct{}, watcherDone <-chan struct{}) {
	serveutil.FinishContextWatcher(serveDone, watcherDone)
}

func WriteFull(writer io.Writer, data []byte) error {
	return streamutil.WriteFull(writer, data)
}
