package httpx

import (
	"errors"
	"fmt"

	"github.com/saramand9/bignet/kpro/core"
	transportapi "github.com/saramand9/bignet/kpro/transport"
	"github.com/saramand9/bignet/kpro/transport/internal/configcheck"
)

var _ transportapi.Validator = (*Endpoint)(nil)

// Validate checks native HTTP startup invariants before serving.
func (e *Endpoint) Validate() error {
	if e == nil {
		return errors.New("http endpoint is nil")
	}
	validationErr := errors.Join(
		e.Descriptor().Validate(),
		configcheck.ListenAddress("HTTP listen address", e.config.Addr),
		configcheck.TypedNil("Logger", e.rawConfig.Logger),
		configcheck.NonNegativeDuration("ReadTimeout", e.rawConfig.ReadTimeout),
		configcheck.NonNegativeDuration("WriteTimeout", e.rawConfig.WriteTimeout),
		configcheck.NonNegativeDuration("IdleTimeout", e.rawConfig.IdleTimeout),
		configcheck.NonNegativeDuration("ShutdownTimeout", e.rawConfig.ShutdownTimeout),
		configcheck.NonNegativeInt64("MaxBodyBytes", e.rawConfig.MaxBodyBytes),
	)
	switch e.transport {
	case core.TransportHTTP:
	case core.TransportHTTPS:
		validationErr = errors.Join(validationErr, configcheck.TLSKeyPair("HTTPS", e.config.CertFile, e.config.KeyFile))
	default:
		validationErr = errors.Join(validationErr, fmt.Errorf("%w for transport %q", ErrNativeAdapterRequired, e.transport))
	}
	return validationErr
}
