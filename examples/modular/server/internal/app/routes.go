package app

import (
	"github.com/saramand9/bignet/kpro/examples/modular/server/internal/modules/echo"
	"github.com/saramand9/bignet/kpro/examples/modular/server/internal/modules/events"
	"github.com/saramand9/bignet/kpro/examples/modular/server/internal/modules/health"
	"github.com/saramand9/bignet/kpro/examples/modular/server/internal/modules/users"
	"github.com/saramand9/bignet/kpro/middleware"
	"github.com/saramand9/bignet/kpro/protocol"
	"github.com/saramand9/bignet/kpro/server"
	"github.com/saramand9/bignet/kpro/transport/httpx"
	"github.com/saramand9/bignet/kpro/transport/sse"
	"github.com/saramand9/bignet/kpro/transport/tcp"
	"github.com/saramand9/bignet/kpro/transport/udp"
	"github.com/saramand9/bignet/kpro/transport/websocket"
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
		registerHTTP(app.HTTP(cfg.HTTPAddr), httpModules...)
	}
	if cfg.HTTPSAddr != "" {
		registerHTTP(app.HTTPS(cfg.HTTPSAddr, httpx.Config{
			CertFile: cfg.HTTPSCertFile,
			KeyFile:  cfg.HTTPSKeyFile,
		}), httpModules...)
	}

	registerMessages(mountMessageEndpoints(app, cfg), messageModules...)

	if cfg.SSEAddr != "" {
		registerSSE(app.SSE(cfg.SSEAddr, cfg.SSEPath), events.Module{})
	}
}

func mountMessageEndpoints(app *server.Server, cfg Config) []*server.Endpoint {
	endpoints := make([]*server.Endpoint, 0, 3)
	if cfg.TCPAddr != "" {
		endpoints = append(endpoints, app.TCP(cfg.TCPAddr, tcp.Config{
			Protocol:     protocol.KProV1(),
			MaxFrameSize: 64 * 1024,
		}).Use(middleware.Recover(), middleware.MaxPayload(64*1024)))
	}
	if cfg.UDPAddr != "" {
		endpoints = append(endpoints, app.UDP(cfg.UDPAddr, udp.Config{
			Protocol:      protocol.KProV1(),
			MaxPacketSize: 2048,
		}).Use(middleware.Recover(), middleware.MaxPayload(1500)))
	}
	if cfg.WSAddr != "" {
		endpoints = append(endpoints, app.WebSocket(cfg.WSAddr, cfg.WSPath, websocket.Config{
			Protocol:   protocol.KProV1(),
			MaxMessage: 64 * 1024,
		}).Use(middleware.Recover(), middleware.MaxPayload(64*1024)))
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
