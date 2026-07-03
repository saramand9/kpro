// File: examples/dbx-http/server/internal/app/routes.go
// Purpose: Provides runnable KPro database example code.

package app

import (
	"context"

	"github.com/saramand9/bignet/kpro/codec"
	"github.com/saramand9/bignet/kpro/dbx"
	loginhandler "github.com/saramand9/bignet/kpro/internal/modules/login/handler"
	loginservice "github.com/saramand9/bignet/kpro/internal/modules/login/service"
	"github.com/saramand9/bignet/kpro/internal/modules/projects"
	projecthandler "github.com/saramand9/bignet/kpro/internal/modules/projects/handler"
	projectservice "github.com/saramand9/bignet/kpro/internal/modules/projects/service"
	reporthandler "github.com/saramand9/bignet/kpro/internal/modules/reports/handler"
	reportservice "github.com/saramand9/bignet/kpro/internal/modules/reports/service"
	"github.com/saramand9/bignet/kpro/internal/modules/users"
	userhandler "github.com/saramand9/bignet/kpro/internal/modules/users/handler"
	userservice "github.com/saramand9/bignet/kpro/internal/modules/users/service"
	"github.com/saramand9/bignet/kpro/middleware"
	"github.com/saramand9/bignet/kpro/server"
	"github.com/saramand9/bignet/kpro/transport/udp"
)

// Config controls mounted routes for the dbx HTTP example.
type Config struct {
	HTTPAddr string
	UDPAddr  string
	GRPCAddr string
}

// RegisterRoutes wires application modules to protocol endpoints.
func RegisterRoutes(ctx context.Context, app *server.Server, database *dbx.Database, config Config) error {
	if err := users.Init(ctx, database); err != nil {
		return err
	}
	if err := projects.Init(ctx, database); err != nil {
		return err
	}

	userService := userservice.New(database)
	projectService := projectservice.New(database)

	httpEndpoint := app.HTTP(config.HTTPAddr)
	userhandler.RegisterHTTP(httpEndpoint, userService)
	projecthandler.RegisterHTTP(httpEndpoint, projectService)

	if config.UDPAddr != "" {
		udpEndpoint := app.UDP(config.UDPAddr, udp.Config{
			Codec:         codec.KProV1(),
			MaxPacketSize: 2048,
		}).Use(middleware.Recover(), middleware.MaxPayload(1500))
		loginhandler.RegisterUDP(udpEndpoint, loginservice.New(loginservice.Config{}))
	}
	if config.GRPCAddr != "" {
		grpcEndpoint := app.GRPC(config.GRPCAddr).Use(middleware.Recover())
		reporthandler.RegisterGRPC(grpcEndpoint, reportservice.New(reportservice.Config{}))
	}
	return nil
}
