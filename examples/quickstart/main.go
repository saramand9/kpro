// File: examples/quickstart/main.go
// Purpose: Provides the minimal recommended KPro HTTP application setup.

package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/saramand9/kpro/middleware"
	"github.com/saramand9/kpro/server"
	"github.com/saramand9/kpro/transport/httpx"
)

const (
	maxBodyBytes   = 1 << 20
	shutdownWindow = 5 * time.Second
)

func main() {
	addr := flag.String("addr", ":8080", "HTTP listen address")
	flag.Parse()

	app, err := server.NewChecked(server.Config{
		Name:            "quickstart",
		ShutdownTimeout: shutdownWindow,
	})
	if err != nil {
		log.Fatal(err)
	}
	metrics := middleware.NewMetrics()
	app.Use(
		middleware.Recover(),
		metrics.Middleware(),
		middleware.MaxPayload(maxBodyBytes),
	)

	app.HTTP(*addr, httpx.Config{
		ReadTimeout:     5 * time.Second,
		WriteTimeout:    10 * time.Second,
		IdleTimeout:     60 * time.Second,
		ShutdownTimeout: shutdownWindow,
		MaxBodyBytes:    maxBodyBytes,
	}).
		Get("/health", func(ctx *httpx.Context) error {
			return ctx.Text(http.StatusOK, "ok\n")
		}).
		Get("/metrics", func(ctx *httpx.Context) error {
			return ctx.JSON(http.StatusOK, metrics.Snapshot())
		}).
		Post("/echo", func(ctx *httpx.Context) error {
			body, err := ctx.Body()
			if err != nil {
				return err
			}
			return ctx.JSON(http.StatusOK, map[string]any{
				"echo": string(body),
			})
		})

	if err := run(app, *addr); err != nil {
		log.Fatal(err)
	}
}

func run(app *server.Server, addr string) error {
	baseCtx, cancelRun := context.WithCancel(context.Background())
	defer cancelRun()
	signalCtx, stopSignals := signal.NotifyContext(
		baseCtx,
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer stopSignals()

	handle, err := app.Start(signalCtx)
	if err != nil {
		return err
	}

	log.Printf("quickstart starting on %s", addr)
	readyCtx, cancelReady := context.WithTimeout(context.Background(), shutdownWindow)
	if err := handle.WaitReady(readyCtx); err != nil {
		cancelReady()
		cancelRun()
		runErr := handle.Wait()
		if server.IsContextTermination(runErr) {
			runErr = nil
		}
		if runErr != nil {
			return runErr
		}
		return err
	}
	cancelReady()
	log.Printf("quickstart ready on %s", addr)
	err = handle.Wait()
	if server.IsContextTermination(err) && signalCtx.Err() != nil {
		return nil
	}
	return err
}
