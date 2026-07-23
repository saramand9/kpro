// File: examples/quickstart/main.go
// Purpose: Provides the minimal recommended KPro HTTP application setup.

package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/saramand9/bignet/kpro/middleware"
	"github.com/saramand9/bignet/kpro/server"
	"github.com/saramand9/bignet/kpro/transport/httpx"
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
	signalCtx, stopSignals := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer stopSignals()

	serveErr := make(chan error, 1)
	go func() {
		serveErr <- app.ListenAndServe()
	}()

	log.Printf("quickstart starting on %s", addr)
	select {
	case err := <-serveErr:
		return err
	case <-signalCtx.Done():
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
