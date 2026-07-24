// Package zapx provides structured invocation logging for zap users.
package zapx

import (
	"fmt"
	"time"

	"github.com/saramand9/kpro/core"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// Config controls successful invocation verbosity and slow-call reporting.
type Config struct {
	SuccessLevel  zapcore.Level
	SlowThreshold time.Duration
	IncludeRemote bool
}

// Middleware emits one completion log for each invocation. Errors and panics
// are logged at error level; slow successful calls are logged at warn level.
func Middleware(logger *zap.Logger, config ...Config) core.Middleware {
	if logger == nil {
		logger = zap.NewNop()
	}
	cfg := normalizeConfig(config)
	return func(next core.InvocationHandler) core.InvocationHandler {
		return func(invocation core.Invocation) (err error) {
			started := time.Now()
			defer func() {
				duration := time.Since(started)
				fields := invocationFields(invocation, duration, cfg.IncludeRemote)
				if recovered := recover(); recovered != nil {
					fields = append(fields, zap.String("panic", fmt.Sprint(recovered)))
					logger.Error("kpro invocation panicked", fields...)
					panic(recovered)
				}
				if err != nil {
					logger.Error("kpro invocation failed", append(fields, zap.Error(err))...)
					return
				}
				if cfg.SlowThreshold > 0 && duration >= cfg.SlowThreshold {
					logger.Warn("kpro invocation slow", fields...)
					return
				}
				logger.Log(cfg.SuccessLevel, "kpro invocation completed", fields...)
			}()
			return next(invocation)
		}
	}
}

func invocationFields(invocation core.Invocation, duration time.Duration, includeRemote bool) []zap.Field {
	fields := []zap.Field{
		zap.String("transport", string(invocation.Transport())),
		zap.String("operation", invocation.Operation()),
		zap.Duration("duration", duration),
	}
	if connID := invocation.ConnID(); connID != 0 {
		fields = append(fields, zap.Uint64("conn_id", connID))
	}
	if includeRemote {
		if remote := invocation.RemoteAddr(); remote != "" {
			fields = append(fields, zap.String("remote_addr", remote))
		}
	}
	spanContext := trace.SpanContextFromContext(invocation.Context())
	if spanContext.IsValid() {
		fields = append(fields,
			zap.String("trace_id", spanContext.TraceID().String()),
			zap.String("span_id", spanContext.SpanID().String()),
		)
	}
	return fields
}

func normalizeConfig(config []Config) Config {
	cfg := Config{SuccessLevel: zap.InfoLevel}
	if len(config) > 0 {
		cfg = config[0]
		if cfg.SuccessLevel < zap.DebugLevel || cfg.SuccessLevel > zap.ErrorLevel {
			cfg.SuccessLevel = zap.InfoLevel
		}
	}
	return cfg
}
