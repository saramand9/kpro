# KPro app server

`cmd/app/main.go` is the current application server entry point for KPro. It
assembles one multi-transport app server and mounts message transports and
native HTTP-style transports under a shared lifecycle.

This is the package direction for the app server:

```text
kpro/core              Message, Context, Session, Router
kpro/codec          StreamCodec and PacketCodec
kpro/transport         shared transport interface
kpro/transport/tcp     TCP transport
kpro/transport/kcp     KCP reliable UDP stream transport
kpro/transport/quic    QUIC stream transport
kpro/transport/udp     UDP transport
kpro/transport/grpcx   gRPC server wrapper and service bindings
kpro/transport/http3   HTTP/3 transport over QUIC
kpro/middleware        reusable middleware
kpro/server            app server assembly layer
```

The core message-transport flow is:

```text
transport receives bytes/datagrams
  -> protocol decodes Message
  -> endpoint middleware
  -> endpoint router
  -> business handler
  -> protocol encodes Message
  -> transport sends bytes/datagrams
```

HTTP, HTTPS, HTTP/3, and SSE use their own request or stream contexts while
sharing the same app server lifecycle and middleware model.

## Example

The examples below define `msgEcho = 100` locally. It is a sample application message ID, not a core framework-reserved or built-in ID.

### TCP

```go
const msgEcho uint32 = 100

app := server.New(server.Config{Name: "gateway"})

app.TCP(":8088", tcp.Config{
	Codec: codec.KProV1(),
}).
	Use(middleware.Recover()).
	Handle(msgEcho, echo)

handle, err := app.Start(context.Background())
if err != nil {
	log.Fatal(err)
}
if err := handle.WaitReady(context.Background()); err != nil {
	log.Fatal(err)
}
if err := handle.Wait(); err != nil {
	log.Fatal(err)
}
```

### KCP

KCP is still available as a reliable UDP stream transport.

```go
app.KCP(":8090", kcp.Config{
	Codec: codec.KProV1(),
}).
	Use(middleware.Recover()).
	Handle(msgEcho, echo)
```

### UDP

```go
app.UDP(":8089", udp.Config{
	Codec:      codec.KProV1(),
	MaxPacketSize: 2048,
}).
	Use(middleware.Recover()).
	Handle(msgEcho, echo)
```

UDP currently uses the same KProV1 packet format, but one datagram must contain
one full frame.

### QUIC

QUIC uses bidirectional QUIC streams as `core.Session` instances. It listens on
UDP, requires TLS certificates, and runs KPro messages directly over each
stream. `quictransport.Config.MaxConnections` caps physical QUIC connections,
while `MaxStreams` independently caps active stream sessions.

```go
app.QUIC(":8444", quictransport.Config{
	Codec: codec.KProV1(),
	CertFile: "server.crt",
	KeyFile:  "server.key",
}).
	Use(middleware.Recover()).
	Handle(msgEcho, echo)
```

### WebSocket

WebSocket uses one WebSocket message as one protocol packet.

```go
app.WebSocket(":8091", "/ws", websocket.Config{
	Codec:   codec.KProV1(),
	MaxMessage: 64 * 1024,
}).
	Use(middleware.Recover()).
	Handle(msgEcho, echo)
```

When `CheckOrigin` is nil, a browser upgrade that includes `Origin` is accepted
only when the origin host matches the HTTP `Host`; clients that omit `Origin`
remain supported. Set `CheckOrigin` when the browser origin and WebSocket host
intentionally differ, for example behind a gateway with an explicit origin
allowlist:

```go
app.WebSocket(":8091", "/ws", websocket.Config{
	Codec: codec.KProV1(),
	CheckOrigin: func(r *http.Request) bool {
		origins := r.Header.Values("Origin")
		if len(origins) != 1 {
			return false
		}
		origin, err := url.Parse(origins[0])
		return err == nil && origin.Scheme == "https" &&
			strings.EqualFold(origin.Host, "app.example.com")
	},
})
```

The callback is the complete policy. Do not use an unconditional `true` for a
browser-facing endpoint, and do not trust forwarded host/origin headers unless
the proxy strips untrusted values and supplies its own.

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
	ShutdownTimeout: 10 * time.Second,
}).
	Get("/health", func(ctx *http3x.Context) error {
		return ctx.Text(200, "ok\n")
	})
```

HTTP/3 shutdown sends GOAWAY and lets accepted requests finish until the
shutdown context expires; expiry closes the remaining connections and returns
the context error. Size `ShutdownTimeout` and the platform termination grace
period from measured request duration, and make handlers observe their request
context. This graceful request drain is different from message-transport
shutdown, which does not guarantee reply delivery.

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

Handlers for message transports receive a transport-neutral context:

```go
func echo(ctx core.Context) error {
	msg := ctx.Message()
	return ctx.Reply(msg.ID, msg.Data)
}
```

## Run

The default app server enables TCP, KCP, UDP, WebSocket, HTTP, and SSE:

```powershell
go run .\cmd\app -tcp :8088 -kcp :8090 -udp :8089 -ws :8091 -http :8080 -sse :8081
```

HTTP/3 is optional because it requires certificates:

```powershell
go run .\cmd\app -http3 :8443 -http3-cert server.crt -http3-key server.key
```

QUIC message transport is optional for the same reason:

```powershell
go run .\cmd\app -quic :8444 -quic-cert server.crt -quic-key server.key
```


