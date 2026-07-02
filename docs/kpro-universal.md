# KPro universal server

This is the package direction for a multi-transport server:

```text
kpro/core              Message, Context, Session, Router
kpro/protocol          StreamProtocol and PacketProtocol
kpro/transport         shared transport interface
kpro/transport/tcp     TCP transport
kpro/transport/kcp     KCP reliable UDP stream transport
kpro/transport/quic    QUIC stream transport
kpro/transport/udp     UDP transport
kpro/transport/grpcx   gRPC server wrapper and service bindings
kpro/transport/http3   HTTP/3 transport over QUIC
kpro/middleware        reusable middleware
kpro/server            universal server assembly layer
```

The core idea is:

```text
transport receives bytes/datagrams
  -> protocol decodes Message
  -> endpoint middleware
  -> endpoint router
  -> business handler
  -> protocol encodes Message
  -> transport sends bytes/datagrams
```

## Example

### TCP

```go
app := server.New(server.Config{Name: "gateway"})

app.TCP(":8088", tcp.Config{
	Protocol: protocol.KProV1(),
}).
	Use(middleware.Recover()).
	Handle(core.MsgEcho, echo)

app.KCP(":8090", kcp.Config{
	Protocol: protocol.KProV1(),
}).
	Use(middleware.Recover()).
	Handle(core.MsgEcho, echo)

app.QUIC(":8444", quictransport.Config{
	Protocol: protocol.KProV1(),
	CertFile: "server.crt",
	KeyFile:  "server.key",
}).
	Use(middleware.Recover()).
	Handle(core.MsgEcho, echo)

app.UDP(":8089", udp.Config{
	Protocol: protocol.KProV1(),
}).
	Use(middleware.Recover()).
	Handle(core.MsgEcho, echo)

log.Fatal(app.ListenAndServe())
```

### UDP

```go
app.UDP(":8089", udp.Config{
	Protocol:      protocol.KProV1(),
	MaxPacketSize: 2048,
}).
	Use(middleware.Recover()).
	Handle(core.MsgEcho, echo)
```

### QUIC

QUIC uses bidirectional QUIC streams as `core.Session` instances. It listens on
UDP, requires TLS certificates, and runs KPro messages directly over each
stream. `quictransport.Config.MaxStreams` limits active stream sessions; the
older `MaxConnections` field is kept as a compatibility alias.

```go
app.QUIC(":8444", quictransport.Config{
	Protocol: protocol.KProV1(),
	CertFile: "server.crt",
	KeyFile:  "server.key",
}).
	Use(middleware.Recover()).
	Handle(core.MsgEcho, echo)
```

### WebSocket

WebSocket uses one WebSocket message as one protocol packet.

```go
app.WebSocket(":8091", "/ws", websocket.Config{
	Protocol:   protocol.KProV1(),
	MaxMessage: 64 * 1024,
}).
	Use(middleware.Recover()).
	Handle(core.MsgEcho, echo)
```

### HTTP

HTTP uses its own request context and path routing instead of msgID routing.

```go
app.HTTP(":8080").
	Get("/health", func(ctx *httpx.Context) error {
		return ctx.Text(200, "ok\n")
	}).
	Post("/echo", func(ctx *httpx.Context) error {
		body, err := ctx.Body()
		if err != nil {
			return err
		}
		return ctx.JSON(200, map[string]interface{}{
			"echo": string(body),
		})
	})
```

### HTTPS

HTTPS is HTTP with TLS configuration.

```go
app.HTTPS(":8443", httpx.Config{
	CertFile: "server.crt",
	KeyFile:  "server.key",
}).
	Get("/health", health)
```

### HTTP/3

HTTP/3 uses the same path-routing handler model as HTTP/HTTPS, but listens on
UDP through QUIC and always requires TLS certificates.

```go
app.HTTP3(":8443", http3x.Config{
	CertFile: "server.crt",
	KeyFile:  "server.key",
}).
	Get("/health", func(ctx *http3x.Context) error {
		return ctx.Text(200, "ok\n")
	})
```

### SSE

SSE is an HTTP streaming endpoint for server-to-client events.

```go
app.SSE(":8081", "/events").
	Handle(func(ctx *sse.Context) error {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return nil
			case t := <-ticker.C:
				if err := ctx.Send("time", []byte(t.Format(time.RFC3339))); err != nil {
					return err
				}
			}
		}
	})
```

Handlers receive a transport-neutral context:

```go
func echo(ctx core.Context) error {
	msg := ctx.Message()
	return ctx.Reply(msg.ID, msg.Data)
}
```

## Run

```powershell
go run .\cmd\kpro-universal-server -tcp :8088 -kcp :8090 -udp :8089 -ws :8091 -http :8080 -sse :8081
```

HTTP/3 is optional because it requires certificates:

```powershell
go run .\cmd\kpro-universal-server -http3 :8443 -http3-cert server.crt -http3-key server.key
```

QUIC message transport is optional for the same reason:

```powershell
go run .\cmd\kpro-universal-server -quic :8444 -quic-cert server.crt -quic-key server.key
```

TCP validation can reuse the existing client:

```powershell
go run .\cmd\kpro-client -addr 127.0.0.1:8088 -conns 1000 -rate 1 -duration 60s -ramp 10s
```

UDP currently uses the same KProV1 packet format, but one datagram must contain
one full frame.
