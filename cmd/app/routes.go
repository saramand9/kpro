// File: cmd/app/routes.go
// Purpose: Registers application routes and message protocols.

package main

import (
	commercehandler "github.com/saramand9/bignet/kpro/internal/modules/commerce/handler"
	projecthandler "github.com/saramand9/bignet/kpro/internal/modules/projects/handler"
	userhandler "github.com/saramand9/bignet/kpro/internal/modules/users/handler"
	"github.com/saramand9/bignet/kpro/middleware"
	"github.com/saramand9/bignet/kpro/server"
	"github.com/saramand9/bignet/kpro/transport/httpx"
)

func registerMessageRoutes(endpoint *server.Endpoint, services appServices) {
	endpoint.Handle(appMsgEcho, echo)
	if services.Users != nil {
		userhandler.RegisterMessage(endpoint, services.Users)
	}
}

func registerHTTPRoutes(endpoint *httpx.Endpoint, metrics *middleware.Metrics, services appServices) {
	endpoint.Get("/health", func(ctx *httpx.Context) error {
		return ctx.Text(200, "ok\n")
	})
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
