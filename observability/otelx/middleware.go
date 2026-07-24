// Package otelx provides optional OpenTelemetry instrumentation without making
// the core runtime depend on an SDK or exporter.
package otelx

import (
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/saramand9/kpro/core"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

const defaultInstrumentationName = "github.com/saramand9/kpro"

// Config controls generic invocation tracing. Exporters and sampling remain an
// application concern and are configured on the supplied TracerProvider.
type Config struct {
	TracerProvider      trace.TracerProvider
	Propagator          propagation.TextMapPropagator
	InstrumentationName string
	// DisablePropagationExtraction avoids materializing lazy headers when a
	// deployment performs propagation in a protocol-native adapter instead.
	DisablePropagationExtraction bool
}

// Middleware starts one server span per invocation and passes a context that
// contains the span to downstream middleware and handlers.
func Middleware(config ...Config) core.Middleware {
	cfg := normalizeConfig(config)
	tracer := cfg.TracerProvider.Tracer(cfg.InstrumentationName)
	return func(next core.InvocationHandler) core.InvocationHandler {
		return func(invocation core.Invocation) (err error) {
			parent := invocation.Context()
			if !cfg.DisablePropagationExtraction {
				headers := invocation.Headers()
				if len(headers) > 0 {
					parent = cfg.Propagator.Extract(parent, caseInsensitiveCarrier(headers))
				}
			}
			ctx, span := tracer.Start(
				parent,
				invocation.Operation(),
				trace.WithSpanKind(trace.SpanKindServer),
				trace.WithAttributes(invocationAttributes(invocation)...),
			)
			defer func() {
				if recovered := recover(); recovered != nil {
					panicErr := fmt.Errorf("panic: %v", recovered)
					span.RecordError(panicErr)
					span.SetStatus(codes.Error, panicErr.Error())
					span.End()
					panic(recovered)
				}
				if err != nil {
					span.RecordError(err)
					span.SetStatus(codes.Error, err.Error())
				} else {
					span.SetStatus(codes.Ok, "")
				}
				span.End()
			}()
			return next(core.WithInvocationContext(invocation, ctx))
		}
	}
}

// caseInsensitiveCarrier preserves the lazy Invocation header representation
// while matching HTTP's case-insensitive field-name rules. It is also safe for
// message protocols whose logical header maps use lowercase keys.
type caseInsensitiveCarrier map[string]string

func (c caseInsensitiveCarrier) Get(key string) string {
	if value, ok := c[key]; ok {
		return value
	}
	for candidate, value := range c {
		if strings.EqualFold(candidate, key) {
			return value
		}
	}
	return ""
}

func (c caseInsensitiveCarrier) Set(key, value string) { c[key] = value }

func (c caseInsensitiveCarrier) Keys() []string {
	keys := make([]string, 0, len(c))
	for key := range c {
		keys = append(keys, key)
	}
	return keys
}

func invocationAttributes(invocation core.Invocation) []attribute.KeyValue {
	attributes := []attribute.KeyValue{
		attribute.String("kpro.transport", string(invocation.Transport())),
		attribute.String("kpro.operation", invocation.Operation()),
	}
	if connID := invocation.ConnID(); connID != 0 {
		attributes = append(attributes, attribute.String("kpro.connection.id", strconv.FormatUint(connID, 10)))
	}
	if remote := invocation.RemoteAddr(); remote != "" {
		host, portText, err := net.SplitHostPort(remote)
		if err == nil && host != "" {
			attributes = append(attributes, attribute.String("network.peer.address", host))
			if port, err := strconv.Atoi(portText); err == nil && port >= 0 && port <= 65535 {
				attributes = append(attributes, attribute.Int("network.peer.port", port))
			}
		} else {
			attributes = append(attributes, attribute.String("network.peer.address", remote))
		}
	}
	return attributes
}

func normalizeConfig(config []Config) Config {
	var cfg Config
	if len(config) > 0 {
		cfg = config[0]
	}
	if cfg.TracerProvider == nil {
		cfg.TracerProvider = otel.GetTracerProvider()
	}
	if cfg.Propagator == nil {
		cfg.Propagator = otel.GetTextMapPropagator()
	}
	if cfg.InstrumentationName == "" {
		cfg.InstrumentationName = defaultInstrumentationName
	}
	return cfg
}
