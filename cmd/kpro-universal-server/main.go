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
	"github.com/saramand9/bignet/kpro/transport/httpx"
	"github.com/saramand9/bignet/kpro/transport/sse"
	"github.com/saramand9/bignet/kpro/transport/tcp"
	"github.com/saramand9/bignet/kpro/transport/udp"
	"go.uber.org/zap"
)

func main() {
	tcpAddr := flag.String("tcp", ":8088", "tcp listen address")
	udpAddr := flag.String("udp", ":8089", "udp listen address")
	wsAddr := flag.String("ws", ":8091", "websocket listen address")
	httpAddr := flag.String("http", ":8080", "http listen address")
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
		log.Fatalf("init yokaman logger: %v", err)
	}
	defer cleanup()

	app := server.New(server.Config{Name: "kpro-universal"})

	app.TCP(*tcpAddr, tcp.Config{
		Protocol:     protocol.KProV1(),
		MaxFrameSize: 64 * 1024,
	}).
		Use(middleware.Recover(), middleware.MaxPayload(64*1024)).
		Handle(core.MsgEcho, echo)

	app.UDP(*udpAddr, udp.Config{
		Protocol:      protocol.KProV1(),
		MaxPacketSize: 2048,
	}).
		Use(middleware.Recover(), middleware.MaxPayload(1500)).
		Handle(core.MsgEcho, echo)

	app.WebSocket(*wsAddr, "/ws").
		Use(middleware.Recover(), middleware.MaxPayload(64*1024)).
		Handle(core.MsgEcho, echo)

	app.HTTP(*httpAddr).
		Get("/health", func(ctx *httpx.Context) error {
			return ctx.Text(200, "ok\n")
		}).
		Post("/echo", func(ctx *httpx.Context) error {
			body, err := ctx.Body()
			if err != nil {
				return err
			}
			return ctx.JSON(200, map[string]interface{}{"echo": string(body)})
		})

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
		zap.String("udp", *udpAddr),
		zap.String("ws", *wsAddr),
		zap.String("http", *httpAddr),
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
