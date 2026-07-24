// File: cmd/app/main.go
// Purpose: Provides the command-line entry point for the KPro application server.

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/saramand9/kpro/codec"
	"github.com/saramand9/kpro/core"
	"github.com/saramand9/kpro/internal/app/persistence"
	"github.com/saramand9/kpro/middleware"
	"github.com/saramand9/kpro/platform/logx"
	"github.com/saramand9/kpro/server"
	http3x "github.com/saramand9/kpro/transport/http3"
	"github.com/saramand9/kpro/transport/httpx"
	"github.com/saramand9/kpro/transport/kcp"
	quictransport "github.com/saramand9/kpro/transport/quic"
	"github.com/saramand9/kpro/transport/sse"
	"github.com/saramand9/kpro/transport/tcp"
	"github.com/saramand9/kpro/transport/udp"
	"go.uber.org/zap"
)

func main() {
	if err := run(); err != nil {
		log.Printf("kpro-app: %v", err)
		os.Exit(1)
	}
}

func run() (returnErr error) {
	tcpAddr := flag.String("tcp", ":8088", "tcp listen address")
	kcpAddr := flag.String("kcp", ":8090", "kcp listen address")
	quicAddr := flag.String("quic", "", "quic listen address, disabled when empty")
	quicCert := flag.String("quic-cert", "", "quic certificate file")
	quicKey := flag.String("quic-key", "", "quic private key file")
	quicMaxStreams := flag.Int("quic-max-streams", quictransport.DefaultMaxStreams, "quic max active bidirectional stream sessions")
	udpAddr := flag.String("udp", ":8089", "udp listen address")
	wsAddr := flag.String("ws", ":8091", "websocket listen address")
	httpAddr := flag.String("http", ":8080", "http listen address")
	http3Addr := flag.String("http3", "", "http3 listen address, disabled when empty")
	http3Cert := flag.String("http3-cert", "", "http3 certificate file")
	http3Key := flag.String("http3-key", "", "http3 private key file")
	sseAddr := flag.String("sse", ":8081", "sse listen address")
	gormDriver := flag.String("gorm-driver", "sqlite", "gorm driver: sqlite, mysql, or postgres")
	gormDSN := flag.String("gorm-dsn", "", "gorm data source name, disabled when empty")
	defaultLog := logx.DefaultConfig("kpro-app")
	defaultLog.FileName = defaultAppLogFile()
	logLevel := flag.String("log-level", defaultLog.Level, "log level: debug, info, warn, error, dpanic, panic, fatal")
	logFile := flag.String("log-file", defaultLog.FileName, "log file path")
	logMaxSize := flag.Int("log-max-size", defaultLog.MaxSize, "log max size in MB")
	logMaxBackups := flag.Int("log-max-backups", defaultLog.MaxBackups, "log max backup files")
	logMaxAge := flag.Int("log-max-age", defaultLog.MaxAge, "log max age in days")
	flag.Parse()

	logger, cleanup, err := logx.Init(logx.Config{
		Level:      *logLevel,
		FileName:   *logFile,
		MaxSize:    *logMaxSize,
		MaxBackups: *logMaxBackups,
		MaxAge:     *logMaxAge,
	})
	if err != nil {
		return fmt.Errorf("init logger: %w", err)
	}
	defer cleanup()

	ctx := context.Background()
	resources, services, err := initAppPersistence(ctx, persistence.Config{
		SQL: persistence.SQLConfig{
			Driver: *gormDriver,
			DSN:    *gormDSN,
		},
	})
	if err != nil {
		return fmt.Errorf("init gorm persistence: %w", err)
	}

	app, err := server.NewChecked(server.Config{
		Name:            "kpro-app",
		ShutdownTimeout: 10 * time.Second,
	})
	if err != nil {
		return fmt.Errorf("invalid server configuration: %w", err)
	}
	defer func() {
		shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancelShutdown()
		returnErr = errors.Join(returnErr, app.Shutdown(shutdownCtx))
	}()
	if resources.Managed() {
		app.Manage(resources)
	}
	metrics := middleware.NewMetrics()

	tcpEndpoint := app.TCP(*tcpAddr, tcp.Config{
		Codec:        codec.KProV1(),
		MaxFrameSize: 64 * 1024,
	}).Use(middleware.Recover(), metrics.Middleware(), middleware.MaxPayload(64*1024))
	registerMessageRoutes(tcpEndpoint, services)

	kcpEndpoint := app.KCP(*kcpAddr, kcp.Config{
		Codec:        codec.KProV1(),
		MaxFrameSize: 64 * 1024,
	}).Use(middleware.Recover(), metrics.Middleware(), middleware.MaxPayload(64*1024))
	registerMessageRoutes(kcpEndpoint, services)

	if *quicAddr != "" {
		if *quicCert == "" || *quicKey == "" {
			return errors.New("quic requires -quic-cert and -quic-key")
		}
		quicEndpoint := app.QUIC(*quicAddr, quictransport.Config{
			Codec:        codec.KProV1(),
			MaxFrameSize: 64 * 1024,
			MaxStreams:   *quicMaxStreams,
			CertFile:     *quicCert,
			KeyFile:      *quicKey,
		}).Use(middleware.Recover(), metrics.Middleware(), middleware.MaxPayload(64*1024))
		registerMessageRoutes(quicEndpoint, services)
	}

	udpEndpoint := app.UDP(*udpAddr, udp.Config{
		Codec:         codec.KProV1(),
		MaxPacketSize: 2048,
	}).Use(middleware.Recover(), metrics.Middleware(), middleware.MaxPayload(1500))
	registerMessageRoutes(udpEndpoint, services)

	wsEndpoint := app.WebSocket(*wsAddr, "/ws").Use(middleware.Recover(), metrics.Middleware(), middleware.MaxPayload(64*1024))
	registerMessageRoutes(wsEndpoint, services)

	httpEndpoint := app.HTTP(*httpAddr, httpx.Config{
		ReadTimeout:     5 * time.Second,
		WriteTimeout:    10 * time.Second,
		IdleTimeout:     60 * time.Second,
		ShutdownTimeout: 10 * time.Second,
		MaxBodyBytes:    1 << 20,
	})
	registerHTTPRoutes(httpEndpoint, metrics, services)

	if *http3Addr != "" {
		if *http3Cert == "" || *http3Key == "" {
			return errors.New("http3 requires -http3-cert and -http3-key")
		}
		app.HTTP3(*http3Addr, http3x.Config{
			CertFile: *http3Cert,
			KeyFile:  *http3Key,
		}).
			Get("/health", func(ctx *http3x.Context) error {
				return ctx.Text(200, "ok\n")
			}).
			Get("/metrics", func(ctx *http3x.Context) error {
				return ctx.JSON(200, metrics.Snapshot())
			}).
			Post("/echo", func(ctx *http3x.Context) error {
				body, err := ctx.Body()
				if err != nil {
					return err
				}
				return ctx.JSON(200, map[string]interface{}{"echo": string(body)})
			})
	}

	app.SSE(*sseAddr, "/events").
		Handle(func(ctx *sse.Context) error {
			ticker := time.NewTicker(time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return nil
				case t := <-ticker.C:
					if err := ctx.Send("time", []byte(t.Format(time.RFC3339))); err != nil {
						return err
					}
				}
			}
		})

	baseCtx, cancelRun := context.WithCancel(context.Background())
	defer cancelRun()
	runCtx, stopSignals := signal.NotifyContext(baseCtx, os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	handle, err := app.Start(runCtx)
	if err != nil {
		return fmt.Errorf("start app server: %w", err)
	}
	readyCtx, cancelReady := context.WithTimeout(context.Background(), 15*time.Second)
	if err := handle.WaitReady(readyCtx); err != nil {
		cancelReady()
		cancelRun()
		runErr := handle.Wait()
		if server.IsContextTermination(runErr) {
			runErr = nil
		}
		return errors.Join(fmt.Errorf("app server failed before readiness: %w", err), runErr)
	}
	cancelReady()

	logger.Info("app server listening",
		zap.String("tcp", *tcpAddr),
		zap.String("kcp", *kcpAddr),
		zap.String("quic", *quicAddr),
		zap.String("udp", *udpAddr),
		zap.String("ws", *wsAddr),
		zap.String("http", *httpAddr),
		zap.String("http3", *http3Addr),
		zap.String("sse", *sseAddr),
		zap.Bool("gorm_enabled", services.Users != nil),
		zap.String("gorm_driver", *gormDriver),
		zap.String("log_file", *logFile),
	)
	if err := handle.Wait(); err != nil {
		if server.IsContextTermination(err) && runCtx.Err() != nil {
			return nil
		}
		return fmt.Errorf("app server stopped: %w", err)
	}
	return nil
}

func defaultAppLogFile() string {
	base, err := os.UserCacheDir()
	if err != nil || base == "" {
		base = os.TempDir()
	}
	return filepath.Join(base, "kpro", "logs", "kpro-app.log")
}

func echo(ctx core.Context) error {
	msg := ctx.Message()
	return ctx.Reply(msg.ID, msg.Data)
}
