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
kpro/server            dependency-neutral app lifecycle and mounting layer
```

`server` imports only `core` and the shared `transport` contracts. Applications
construct the concrete protocols they use and mount them explicitly. This keeps
an HTTP-only service from compiling KCP, QUIC, HTTP/3, or gRPC transitively.

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

tcpEndpoint := app.Mount(tcp.New(tcp.Config{
	Addr:  ":8088",
	Codec: codec.KProV1(),
}))
tcpEndpoint.
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
app.Mount(kcp.New(kcp.Config{
	Addr:  ":8090",
	Codec: codec.KProV1(),
})).
	Use(middleware.Recover()).
	Handle(msgEcho, echo)
```

### UDP

```go
app.Mount(udp.New(udp.Config{
	Addr:          ":8089",
	Codec:         codec.KProV1(),
	MaxPacketSize: 2048,
})).
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
app.Mount(quictransport.New(quictransport.Config{
	Addr:     ":8444",
	Codec:    codec.KProV1(),
	CertFile: "server.crt",
	KeyFile:  "server.key",
})).
	Use(middleware.Recover()).
	Handle(msgEcho, echo)
```

### WebSocket

WebSocket uses one WebSocket message as one protocol packet.

```go
app.Mount(websocket.New(websocket.Config{
	Addr:       ":8091",
	Path:       "/ws",
	Codec:      codec.KProV1(),
	MaxMessage: 64 * 1024,
})).
	Use(middleware.Recover()).
	Handle(msgEcho, echo)
```

When `CheckOrigin` is nil, a browser upgrade that includes `Origin` is accepted
only when the origin host matches the HTTP `Host`; clients that omit `Origin`
remain supported. Set `CheckOrigin` when the browser origin and WebSocket host
intentionally differ, for example behind a gateway with an explicit origin
allowlist:

```go
app.Mount(websocket.New(websocket.Config{
	Addr:  ":8091",
	Path:  "/ws",
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
}))
```

The callback is the complete policy. Do not use an unconditional `true` for a
browser-facing endpoint, and do not trust forwarded host/origin headers unless
the proxy strips untrusted values and supplies its own.

### HTTP

HTTP uses its own request context and path routing instead of msgID routing.

```go
httpEndpoint := server.MountNative(app, httpx.New(httpx.Config{Addr: ":8080"}))
httpEndpoint.Get("/health", func(ctx *httpx.Context) error {
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

HTTP and HTTPS attempt graceful request drain first. If the shutdown context
expires or graceful shutdown otherwise fails, the endpoint closes remaining
non-hijacked HTTP connections and returns the joined error. Handlers must still
observe request cancellation; connections explicitly hijacked from `net/http`
are owned by the hijacking component.

`MaxBodyBytes` is installed on the native request before middleware and route
handlers run. Reads through `Context.Request().Body` and `Context.Body()` share
the same endpoint-wide request budget; middleware may impose a stricter limit,
but cannot make the configured endpoint limit larger. HTTP/3 uses the same
request-body behavior.

### HTTPS

HTTPS is HTTP with TLS configuration.

```go
httpsEndpoint := server.MountNative(app, httpx.New(httpx.Config{
	Addr:     ":8443",
	TLS:      true,
	CertFile: "server.crt",
	KeyFile:  "server.key",
}))
httpsEndpoint.Get("/health", health)
```

### HTTP/3

HTTP/3 uses the same path-routing handler model as HTTP/HTTPS, but listens on
UDP through QUIC and always requires TLS certificates.

```go
http3Endpoint := server.MountNative(app, http3x.New(http3x.Config{
	Addr:            ":8443",
	CertFile:        "server.crt",
	KeyFile:         "server.key",
	ShutdownTimeout: 10 * time.Second,
}))
http3Endpoint.Get("/health", func(ctx *http3x.Context) error {
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
sseEndpoint := server.MountNative(app, sse.New(sse.Config{Addr: ":8081", Path: "/events"}))
sseEndpoint.Handle(func(ctx *sse.Context) error {
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

`sse.Config.WriteTimeout` bounds the initial stream flush and one
`Context.Send` call, including its flush, and is renewed for every event; it is
not a lifetime limit for the stream. A zero value leaves those writes without a
deadline. The standard `net/http` HTTP/1 and HTTP/2 response writers support
this control. A custom response writer used in front of the endpoint must
expose `SetWriteDeadline`, or provide an `Unwrap() http.ResponseWriter` method;
otherwise a configured timeout makes stream setup or `Send` fail with an error
matching `http.ErrNotSupported`. Writes made directly to the embedded
`ResponseWriter` are outside the `Send` deadline contract.

SSE shutdown cancels stream request contexts before draining. If a slow client
keeps a write blocked through the drain deadline, the endpoint force-closes the
non-hijacked connection so the write can return.

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

The reference app exposes `/livez`, `/readyz`, and a compatibility `/health`
alias on HTTP and HTTP/3. Readiness succeeds only while the whole application
server is running. It needs no external database by default; when GORM is
enabled it also performs a bounded SQL ping and returns a generic 503 on
failure. SQLite defaults to one open/idle connection, while MySQL/PostgreSQL
default to 16 open and 8 idle connections with 30-minute lifetime and 5-minute
idle limits. The `-gorm-max-open-conns`, `-gorm-max-idle-conns`,
`-gorm-conn-max-lifetime`, and `-gorm-conn-max-idle-time` flags override them.

HTTP/3 is optional because it requires certificates:

```powershell
go run .\cmd\app -http3 :8443 -http3-cert server.crt -http3-key server.key
```

QUIC message transport is optional for the same reason:

```powershell
go run .\cmd\app -quic :8444 -quic-cert server.crt -quic-key server.key
```


