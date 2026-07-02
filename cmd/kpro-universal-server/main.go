// File: cmd/kpro-universal-server/main.go
// Purpose: Provides a command-line entry point for KPro tooling or examples.

package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/saramand9/bignet/kpro/core"
	"github.com/saramand9/bignet/kpro/internal/yokalog"
	"github.com/saramand9/bignet/kpro/middleware"
	"github.com/saramand9/bignet/kpro/protocol"
	"github.com/saramand9/bignet/kpro/server"
	http3x "github.com/saramand9/bignet/kpro/transport/http3"
	"github.com/saramand9/bignet/kpro/transport/httpx"
	"github.com/saramand9/bignet/kpro/transport/kcp"
	quictransport "github.com/saramand9/bignet/kpro/transport/quic"
	"github.com/saramand9/bignet/kpro/transport/sse"
	"github.com/saramand9/bignet/kpro/transport/tcp"
	"github.com/saramand9/bignet/kpro/transport/udp"
	"go.uber.org/zap"
)

func main() {
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
	defaultLog := yokalog.DefaultConfig("kpro-universal")
	logLevel := flag.String("log-level", defaultLog.Level, "log level: debug, info, warn, error, dpanic, panic, fatal")
	logFile := flag.String("log-file", defaultLog.FileName, "log file path")
	logMaxSize := flag.Int("log-max-size", defaultLog.MaxSize, "log max size in MB")
	logMaxBackups := flag.Int("log-max-backups", defaultLog.MaxBackups, "log max backup files")
	logMaxAge := flag.Int("log-max-age", defaultLog.MaxAge, "log max age in days")
	flag.Parse()

	logger, cleanup, err := yokalog.Init(yokalog.Config{
		Level:      *logLevel,
		FileName:   *logFile,
		MaxSize:    *logMaxSize,
		MaxBackups: *logMaxBackups,
		MaxAge:     *logMaxAge,
	})
	if err != nil {
		log.Fatalf("init logger: %v", err)
	}
	defer cleanup()

	app := server.New(server.Config{Name: "kpro-universal"})
	metrics := middleware.NewMetrics()

	app.TCP(*tcpAddr, tcp.Config{
		Protocol:     protocol.KProV1(),
		MaxFrameSize: 64 * 1024,
	}).
		Use(middleware.Recover(), metrics.Middleware(), middleware.MaxPayload(64*1024)).
		Handle(core.MsgEcho, echo)

	app.KCP(*kcpAddr, kcp.Config{
		Protocol:     protocol.KProV1(),
		MaxFrameSize: 64 * 1024,
	}).
		Use(middleware.Recover(), metrics.Middleware(), middleware.MaxPayload(64*1024)).
		Handle(core.MsgEcho, echo)

	if *quicAddr != "" {
		if *quicCert == "" || *quicKey == "" {
			logger.Fatal("quic requires -quic-cert and -quic-key")
		}
		app.QUIC(*quicAddr, quictransport.Config{
			Protocol:     protocol.KProV1(),
			MaxFrameSize: 64 * 1024,
			MaxStreams:   *quicMaxStreams,
			CertFile:     *quicCert,
			KeyFile:      *quicKey,
		}).
			Use(middleware.Recover(), metrics.Middleware(), middleware.MaxPayload(64*1024)).
			Handle(core.MsgEcho, echo)
	}

	app.UDP(*udpAddr, udp.Config{
		Protocol:      protocol.KProV1(),
		MaxPacketSize: 2048,
	}).
		Use(middleware.Recover(), metrics.Middleware(), middleware.MaxPayload(1500)).
		Handle(core.MsgEcho, echo)

	app.WebSocket(*wsAddr, "/ws").
		Use(middleware.Recover(), metrics.Middleware(), middleware.MaxPayload(64*1024)).
		Handle(core.MsgEcho, echo)

	app.HTTP(*httpAddr).
		Get("/health", func(ctx *httpx.Context) error {
			return ctx.Text(200, "ok\n")
		}).
		Get("/metrics", func(ctx *httpx.Context) error {
			return ctx.JSON(200, metrics.Snapshot())
		}).
		Post("/echo", func(ctx *httpx.Context) error {
			body, err := ctx.Body()
			if err != nil {
				return err
			}
			return ctx.JSON(200, map[string]interface{}{"echo": string(body)})
		})

	if *http3Addr != "" {
		if *http3Cert == "" || *http3Key == "" {
			logger.Fatal("http3 requires -http3-cert and -http3-key")
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

	go func() {
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
		<-ch
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		logger.Info("shutdown requested")
		_ = app.Shutdown(ctx)
	}()

	logger.Info("universal server listening",
		zap.String("tcp", *tcpAddr),
		zap.String("kcp", *kcpAddr),
		zap.String("quic", *quicAddr),
		zap.String("udp", *udpAddr),
		zap.String("ws", *wsAddr),
		zap.String("http", *httpAddr),
		zap.String("http3", *http3Addr),
		zap.String("sse", *sseAddr),
		zap.String("log_file", *logFile),
	)
	if err := app.ListenAndServe(); err != nil {
		logger.Fatal("universal server stopped", zap.Error(err))
	}
}

func echo(ctx core.Context) error {
	msg := ctx.Message()
	return ctx.Reply(msg.ID, msg.Data)
}
