// File: examples/observability/main.go
// Purpose: Demonstrates KPro health, metrics, logging, tracing, and transport descriptors.

package main

import (
	"context"
	"errors"
	"flag"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/saramand9/bignet/kpro/core"
	"github.com/saramand9/bignet/kpro/middleware"
	"github.com/saramand9/bignet/kpro/observability/healthx"
	"github.com/saramand9/bignet/kpro/observability/otelx"
	"github.com/saramand9/bignet/kpro/observability/prometheusx"
	"github.com/saramand9/bignet/kpro/observability/zapx"
	"github.com/saramand9/bignet/kpro/server"
	"github.com/saramand9/bignet/kpro/transport"
	"github.com/saramand9/bignet/kpro/transport/httpx"
	"go.uber.org/zap"
)

const (
	maxBodyBytes   = 1 << 20
	shutdownWindow = 5 * time.Second
)

func main() {
	apiAddr := flag.String("api-addr", ":8080", "application HTTP listen address")
	adminAddr := flag.String("admin-addr", ":9090", "health and metrics listen address")
	flag.Parse()

	logger, err := zap.NewProduction()
	if err != nil {
		panic(err)
	}
	defer func() { _ = logger.Sync() }()

	registry := prometheus.NewRegistry()
	metrics := prometheusx.MustNew(prometheusx.Config{
		Registerer: registry,
		Gatherer:   registry,
		Subsystem:  "example",
	})

	app, err := server.NewChecked(server.Config{
		Name:            "observability-example",
		ShutdownTimeout: shutdownWindow,
	})
	if err != nil {
		logger.Fatal("invalid server configuration", zap.Error(err))
	}
	lifecycleMetrics, err := prometheusx.NewLifecycleCollector(app, prometheusx.RuntimeCollectorConfig{
		Subsystem: "example",
		Name:      "application",
		Component: "server",
	})
	if err != nil {
		logger.Fatal("create lifecycle collector", zap.Error(err))
	}
	if err := registry.Register(lifecycleMetrics); err != nil {
		logger.Fatal("register lifecycle collector", zap.Error(err))
	}
	app.Use(
		middleware.Recover(),
		// otelx uses the global provider here. A real service installs and owns
		// its chosen SDK, sampler, and exporter before building this stack.
		otelx.Middleware(),
		zapx.Middleware(logger, zapx.Config{SlowThreshold: 500 * time.Millisecond}),
		metrics.Middleware(),
		middleware.MaxPayload(maxBodyBytes),
	)

	app.HTTP(*apiAddr, httpx.Config{
		ReadTimeout:     5 * time.Second,
		WriteTimeout:    10 * time.Second,
		IdleTimeout:     60 * time.Second,
		ShutdownTimeout: shutdownWindow,
		MaxBodyBytes:    maxBodyBytes,
	}).Get("/hello", func(ctx *httpx.Context) error {
		return ctx.JSON(http.StatusOK, map[string]string{"message": "hello"})
	})

	var ready atomic.Bool
	liveness := healthx.MustNew([]healthx.Check{
		{Name: "runtime", Run: contextCheck},
	})
	readiness := healthx.MustNew([]healthx.Check{
		{Name: "service", Run: readinessCheck(&ready)},
	}, healthx.Config{Timeout: time.Second})
	admin := newAdminServer(*adminAddr, liveness, readiness, metrics.Handler())
	descriptor := transport.Describe(admin)
	logger.Info("mounting native component",
		zap.String("name", descriptor.Name),
		zap.String("protocol", string(descriptor.Protocol)),
		zap.String("mode", string(descriptor.Mode)),
	)
	app.MountRunner(admin)

	if err := run(app, &ready, logger, *apiAddr, *adminAddr); err != nil {
		logger.Fatal("service stopped", zap.Error(err))
	}
}

func run(app *server.Server, ready *atomic.Bool, logger *zap.Logger, apiAddr, adminAddr string) error {
	signalCtx, stopSignals := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer stopSignals()

	ready.Store(true)
	defer ready.Store(false)
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- app.ListenAndServe()
	}()

	logger.Info("service starting",
		zap.String("api_address", apiAddr),
		zap.String("admin_address", adminAddr),
	)
	select {
	case err := <-serveErr:
		return err
	case <-signalCtx.Done():
		ready.Store(false)
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownWindow)
		defer cancel()
		shutdownErr := app.Shutdown(shutdownCtx)
		select {
		case err := <-serveErr:
			return errors.Join(shutdownErr, err)
		case <-shutdownCtx.Done():
			return errors.Join(shutdownErr, shutdownCtx.Err())
		}
	}
}

func contextCheck(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

func readinessCheck(ready *atomic.Bool) healthx.CheckFunc {
	return func(ctx context.Context) error {
		if err := contextCheck(ctx); err != nil {
			return err
		}
		if !ready.Load() {
			return errors.New("service is draining")
		}
		return nil
	}
}

type adminServer struct {
	server *http.Server
}

var (
	_ transport.Runner             = (*adminServer)(nil)
	_ transport.CapabilityProvider = (*adminServer)(nil)
	_ transport.Validator          = (*adminServer)(nil)
)

func newAdminServer(addr string, liveness, readiness, metrics http.Handler) *adminServer {
	mux := http.NewServeMux()
	mux.Handle("/livez", liveness)
	mux.Handle("/readyz", readiness)
	mux.Handle("/metrics", metrics)
	return &adminServer{server: &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       5 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}}
}

func (*adminServer) Name() string {
	return "observability-http"
}

func (*adminServer) Descriptor() transport.Descriptor {
	return transport.Descriptor{
		Name:         "observability-http",
		Protocol:     core.TransportHTTP,
		Mode:         transport.ModeNative,
		Capabilities: transport.CapabilityStream | transport.CapabilityRequestResponse,
	}
}

func (a *adminServer) Validate() error {
	if a == nil || a.server == nil {
		return errors.New("admin HTTP server is required")
	}
	var validationErr error
	validationErr = errors.Join(validationErr, a.Descriptor().Validate())
	if strings.TrimSpace(a.server.Addr) == "" {
		validationErr = errors.Join(validationErr, errors.New("admin HTTP address is required"))
	}
	return validationErr
}

func (a *adminServer) ListenAndServe(ctx context.Context) error {
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownWindow)
		defer cancel()
		_ = a.Shutdown(shutdownCtx)
	}()
	err := a.server.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (a *adminServer) Shutdown(ctx context.Context) error {
	return a.server.Shutdown(ctx)
}
