// File: cmd/app/routes.go
// Purpose: Registers application routes and message protocols.

package main

import (
	userhandler "github.com/saramand9/bignet/kpro/internal/modules/users/handler"
	userservice "github.com/saramand9/bignet/kpro/internal/modules/users/service"
	"github.com/saramand9/bignet/kpro/middleware"
	"github.com/saramand9/bignet/kpro/server"
	"github.com/saramand9/bignet/kpro/transport/httpx"
)

func registerMessageRoutes(endpoint *server.Endpoint, userService *userservice.Service) {
	endpoint.Handle(appMsgEcho, echo)
	if userService != nil {
		userhandler.RegisterMessage(endpoint, userService)
	}
}

func registerHTTPRoutes(endpoint *httpx.Endpoint, metrics *middleware.Metrics, userService *userservice.Service) {
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
	if userService != nil {
		userhandler.RegisterHTTP(endpoint, userService)
	}
}
