// File: examples/modular/server/internal/app/routes.go
// Purpose: Provides runnable KPro example code.

package app

import (
	"github.com/saramand9/kpro/codec"
	"github.com/saramand9/kpro/examples/modular/server/internal/modules/echo"
	"github.com/saramand9/kpro/examples/modular/server/internal/modules/events"
	"github.com/saramand9/kpro/examples/modular/server/internal/modules/health"
	"github.com/saramand9/kpro/examples/modular/server/internal/modules/users"
	"github.com/saramand9/kpro/middleware"
	"github.com/saramand9/kpro/server"
	"github.com/saramand9/kpro/transport/httpx"
	"github.com/saramand9/kpro/transport/sse"
	"github.com/saramand9/kpro/transport/tcp"
	"github.com/saramand9/kpro/transport/udp"
	"github.com/saramand9/kpro/transport/websocket"
)

type Config struct {
	TCPAddr string
	UDPAddr string
	WSAddr  string
	WSPath  string

	HTTPAddr      string
	HTTPSAddr     string
	HTTPSCertFile string
	HTTPSKeyFile  string

	SSEAddr string
	SSEPath string
}

type HTTPModule interface {
	RegisterHTTP(*httpx.Endpoint)
}

type MessageModule interface {
	RegisterMessage(*server.Endpoint)
}

type SSEModule interface {
	RegisterSSE(*sse.Endpoint)
}

func RegisterRoutes(app *server.Server, cfg Config) {
	httpModules := []HTTPModule{
		health.Module{},
		echo.Module{},
		users.Module{},
	}
	messageModules := []MessageModule{
		echo.Module{},
	}

	if cfg.HTTPAddr != "" {
		httpEndpoint := httpx.New(httpx.Config{Addr: cfg.HTTPAddr})
		app.MountRunner(httpEndpoint)
		registerHTTP(httpEndpoint, httpModules...)
	}
	if cfg.HTTPSAddr != "" {
		httpsEndpoint := httpx.New(httpx.Config{
			Addr:     cfg.HTTPSAddr,
			TLS:      true,
			CertFile: cfg.HTTPSCertFile,
			KeyFile:  cfg.HTTPSKeyFile,
		})
		app.MountRunner(httpsEndpoint)
		registerHTTP(httpsEndpoint, httpModules...)
	}

	registerMessages(mountMessageEndpoints(app, cfg), messageModules...)

	if cfg.SSEAddr != "" {
		sseEndpoint := sse.New(sse.Config{Addr: cfg.SSEAddr, Path: cfg.SSEPath})
		app.MountRunner(sseEndpoint)
		registerSSE(sseEndpoint, events.Module{})
	}
}

func mountMessageEndpoints(app *server.Server, cfg Config) []*server.Endpoint {
	endpoints := make([]*server.Endpoint, 0, 3)
	if cfg.TCPAddr != "" {
		endpoints = append(endpoints, app.Mount(tcp.New(tcp.Config{
			Addr:         cfg.TCPAddr,
			Codec:        codec.KProV1(),
			MaxFrameSize: 64 * 1024,
		})).Use(middleware.Recover(), middleware.MaxPayload(64*1024)))
	}
	if cfg.UDPAddr != "" {
		endpoints = append(endpoints, app.Mount(udp.New(udp.Config{
			Addr:          cfg.UDPAddr,
			Codec:         codec.KProV1(),
			MaxPacketSize: 2048,
		})).Use(middleware.Recover(), middleware.MaxPayload(1500)))
	}
	if cfg.WSAddr != "" {
		endpoints = append(endpoints, app.Mount(websocket.New(websocket.Config{
			Addr:       cfg.WSAddr,
			Path:       cfg.WSPath,
			Codec:      codec.KProV1(),
			MaxMessage: 64 * 1024,
		})).Use(middleware.Recover(), middleware.MaxPayload(64*1024)))
	}
	return endpoints
}

func registerHTTP(endpoint *httpx.Endpoint, modules ...HTTPModule) {
	for _, module := range modules {
		module.RegisterHTTP(endpoint)
	}
}

func registerMessages(endpoints []*server.Endpoint, modules ...MessageModule) {
	for _, endpoint := range endpoints {
		for _, module := range modules {
			module.RegisterMessage(endpoint)
		}
	}
}

func registerSSE(endpoint *sse.Endpoint, modules ...SSEModule) {
	for _, module := range modules {
		module.RegisterSSE(endpoint)
	}
}
