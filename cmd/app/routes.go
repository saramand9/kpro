// File: cmd/app/routes.go
// Purpose: Registers application routes and message protocols.

package main

import (
	"context"
	"net/http"
	"time"

	"github.com/saramand9/kpro/database/common/lifecycle"
	commercehandler "github.com/saramand9/kpro/internal/modules/commerce/handler"
	projecthandler "github.com/saramand9/kpro/internal/modules/projects/handler"
	userhandler "github.com/saramand9/kpro/internal/modules/users/handler"
	"github.com/saramand9/kpro/middleware"
	"github.com/saramand9/kpro/server"
	http3x "github.com/saramand9/kpro/transport/http3"
	"github.com/saramand9/kpro/transport/httpx"
)

const appReadinessTimeout = 2 * time.Second

type appResourceHealth interface {
	Managed() bool
	Health(context.Context) []lifecycle.Health
}

type appServingState interface {
	State() server.State
}

type probeResponse struct {
	Status string `json:"status"`
}

func registerMessageRoutes(endpoint *server.Endpoint, services appServices) {
	endpoint.Handle(appMsgEcho, echo)
	if services.Users != nil {
		userhandler.RegisterMessage(endpoint, services.Users)
	}
}

func installAppMiddleware(app *server.Server, metrics *middleware.Metrics) {
	app.Use(middleware.Recover(), metrics.Middleware())
}

func registerHTTPRoutes(endpoint *httpx.Endpoint, metrics *middleware.Metrics, services appServices, resources appResourceHealth, serving appServingState) {
	endpoint.Get("/health", readinessHandler(resources, serving))
	endpoint.Get("/livez", livenessHandler)
	endpoint.Get("/readyz", readinessHandler(resources, serving))
	endpoint.Get("/metrics", func(ctx *httpx.Context) error {
		return ctx.JSON(200, metrics.Snapshot())
	})
	endpoint.Post("/echo", func(ctx *httpx.Context) error {
		body, err := ctx.Body()
		if err != nil {
			return err
		}
		return ctx.JSON(200, map[string]any{"echo": string(body)})
	})
	if services.Users != nil {
		userhandler.RegisterHTTP(endpoint, services.Users)
	}
	if services.Projects != nil {
		projecthandler.RegisterHTTP(endpoint, services.Projects)
	}
	if services.Commerce != nil {
		commercehandler.RegisterHTTP(endpoint, services.Commerce)
	}
}

func registerHTTP3Routes(endpoint *http3x.Endpoint, metrics *middleware.Metrics, resources appResourceHealth, serving appServingState) {
	endpoint.Get("/health", readinessHandler(resources, serving))
	endpoint.Get("/livez", livenessHandler)
	endpoint.Get("/readyz", readinessHandler(resources, serving))
	endpoint.Get("/metrics", func(ctx *http3x.Context) error {
		return ctx.JSON(http.StatusOK, metrics.Snapshot())
	})
	endpoint.Post("/echo", func(ctx *http3x.Context) error {
		body, err := ctx.Body()
		if err != nil {
			return err
		}
		return ctx.JSON(http.StatusOK, map[string]any{"echo": string(body)})
	})
}

func livenessHandler(ctx *httpx.Context) error {
	return ctx.JSON(http.StatusOK, probeResponse{Status: "alive"})
}

func readinessHandler(resources appResourceHealth, serving appServingState) httpx.Handler {
	return readinessHandlerWithTimeout(resources, serving, appReadinessTimeout)
}

func readinessHandlerWithTimeout(resources appResourceHealth, serving appServingState, timeout time.Duration) httpx.Handler {
	return func(ctx *httpx.Context) error {
		if serving == nil || serving.State() != server.StateRunning {
			return ctx.JSON(http.StatusServiceUnavailable, probeResponse{Status: "not_ready"})
		}
		if !resourcesReady(ctx.Context(), resources, timeout) || serving.State() != server.StateRunning {
			return ctx.JSON(http.StatusServiceUnavailable, probeResponse{Status: "not_ready"})
		}
		return ctx.JSON(http.StatusOK, probeResponse{Status: "ready"})
	}
}

func resourcesReady(parent context.Context, resources appResourceHealth, timeout time.Duration) bool {
	if resources == nil || !resources.Managed() {
		return true
	}
	if parent == nil {
		parent = context.Background()
	}
	checkCtx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	report := resources.Health(checkCtx)
	if checkCtx.Err() != nil || len(report) == 0 {
		return false
	}
	for _, health := range report {
		if health.Status != lifecycle.StatusOK {
			return false
		}
	}
	return true
}
