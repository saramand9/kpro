// File: examples/modular/server/main.go
// Purpose: Provides runnable KPro example code.

package main

import (
	"flag"
	"log"

	routeapp "github.com/saramand9/kpro/examples/modular/server/internal/app"
	"github.com/saramand9/kpro/server"
)

func main() {
	tcpAddr := flag.String("tcp", ":8088", "tcp listen address, empty disables tcp")
	udpAddr := flag.String("udp", ":8089", "udp listen address, empty disables udp")
	wsAddr := flag.String("ws", ":8091", "websocket listen address, empty disables websocket")
	wsPath := flag.String("ws-path", "/ws", "websocket path")
	httpAddr := flag.String("http", ":8080", "http listen address, empty disables http")
	httpsAddr := flag.String("https", "", "https listen address, empty disables https")
	sseAddr := flag.String("sse", ":8081", "sse listen address, empty disables sse")
	ssePath := flag.String("sse-path", "/events", "sse path")
	flag.Parse()

	cfg := routeapp.Config{
		TCPAddr:  *tcpAddr,
		UDPAddr:  *udpAddr,
		WSAddr:   *wsAddr,
		WSPath:   *wsPath,
		HTTPAddr: *httpAddr,
		// HTTPS reuses the same HTTP route modules when enabled.
		HTTPSAddr: *httpsAddr,
		SSEAddr:   *sseAddr,
		SSEPath:   *ssePath,
	}
	if cfg.HTTPSAddr != "" {
		certFile, keyFile, cleanup, err := routeapp.SelfSignedCert()
		if err != nil {
			log.Fatal(err)
		}
		defer cleanup()
		cfg.HTTPSCertFile = certFile
		cfg.HTTPSKeyFile = keyFile
	}

	app := server.New(server.Config{Name: "modular-example"})
	routeapp.RegisterRoutes(app, cfg)

	log.Printf("modular example listening tcp=%s udp=%s ws=%s%s http=%s https=%s sse=%s%s",
		*tcpAddr, *udpAddr, *wsAddr, *wsPath, *httpAddr, *httpsAddr, *sseAddr, *ssePath)
	log.Fatal(app.ListenAndServe())
}
