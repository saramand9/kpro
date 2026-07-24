// File: examples/gorm-http/server/internal/app/modules.go
// Purpose: Wires composed stores into protocol modules.

package app

import (
	"github.com/saramand9/kpro/codec"
	loginhandler "github.com/saramand9/kpro/internal/modules/login/handler"
	loginservice "github.com/saramand9/kpro/internal/modules/login/service"
	projecthandler "github.com/saramand9/kpro/internal/modules/projects/handler"
	projectservice "github.com/saramand9/kpro/internal/modules/projects/service"
	reporthandler "github.com/saramand9/kpro/internal/modules/reports/handler"
	reportservice "github.com/saramand9/kpro/internal/modules/reports/service"
	userhandler "github.com/saramand9/kpro/internal/modules/users/handler"
	userservice "github.com/saramand9/kpro/internal/modules/users/service"
	"github.com/saramand9/kpro/middleware"
	"github.com/saramand9/kpro/server"
	"github.com/saramand9/kpro/transport/grpcx"
	"github.com/saramand9/kpro/transport/httpx"
	"github.com/saramand9/kpro/transport/udp"
)

// RegisterHTTPModules wires HTTP modules from composed persistence ports.
func RegisterHTTPModules(endpoint *httpx.Endpoint, stores Stores) error {
	if err := stores.validate(); err != nil {
		return err
	}
	userhandler.RegisterHTTP(endpoint, userservice.NewWithStore(stores.Users))
	projecthandler.RegisterHTTP(endpoint, projectservice.NewWithStore(stores.Projects))
	return nil
}

// RegisterUDPModules wires UDP-only modules.
func RegisterUDPModules(endpoint *server.Endpoint) {
	loginhandler.RegisterUDP(endpoint, loginservice.New(loginservice.Config{}))
}

// RegisterGRPCModules wires gRPC-only modules.
func RegisterGRPCModules(endpoint *grpcx.Server) {
	reporthandler.RegisterGRPC(endpoint, reportservice.New(reportservice.Config{}))
}

// MountOptionalProtocolModules mounts optional non-HTTP modules for the example.
func MountOptionalProtocolModules(app *server.Server, config Config) {
	if config.UDPAddr != "" {
		udpEndpoint := app.Mount(udp.New(udp.Config{
			Addr:          config.UDPAddr,
			Codec:         codec.KProV1(),
			MaxPacketSize: 2048,
		})).Use(middleware.Recover(), middleware.MaxPayload(1500))
		RegisterUDPModules(udpEndpoint)
	}
	if config.GRPCAddr != "" {
		grpcEndpoint := grpcx.NewServer(config.GRPCAddr)
		app.MountRunner(grpcEndpoint)
		grpcEndpoint.Use(middleware.Recover())
		RegisterGRPCModules(grpcEndpoint)
	}
}
