# KPro

KPro is a Go network framework experiment for building a multi-transport app server behind one application-level programming model.

The design goal is:

```text
business code handles protocol-native handlers or Message / Context
transport code handles TCP, KCP, QUIC, UDP, WebSocket, HTTP, HTTPS, HTTP/2, HTTP/3, SSE, gRPC
protocol code translates bytes, datagrams or frames into Message when applicable
server code assembles transports, middleware and routers
```

## Status

Current implementation:

```text
TCP         implemented
KCP         implemented
QUIC        implemented as KPro message streams over QUIC
UDP         implemented
WebSocket   implemented, supports RFC 6455 data-message fragmentation
HTTP        implemented
HTTPS       implemented through HTTP + TLS config
HTTP/2      implemented through HTTPS native Go negotiation
HTTP/3      implemented through QUIC + TLS config
SSE         implemented
gRPC        implemented through transport/grpcx and server.GRPC
```

## Architecture

High-level structure:

```mermaid
flowchart TD
    App[server.Server] --> TCP[transport/tcp]
    App --> KCP[transport/kcp]
    App --> QUIC[transport/quic]
    App --> UDP[transport/udp]
    App --> WS[transport/websocket]
    App --> HTTP[transport/httpx]
    App --> HTTP3[transport/http3]
    App --> SSE[transport/sse]
    App --> GRPC[transport/grpcx]

    TCP --> Protocol[codec.KProV1]
    KCP --> Protocol
    QUIC --> Protocol
    UDP --> Protocol
    WS --> Protocol

    Protocol --> Message[core.Message]
    Message --> Router[core.Router]
    Router --> Invocation[core.Invocation]
    Invocation --> Middleware[core.Middleware]
    Middleware --> Handler[Business Handler]

    HTTP --> HTTPRouter[method + path router]
    HTTP3 --> HTTPRouter
    SSE --> SSEHandler[SSE stream handler]
    GRPC --> GRPCService[gRPC service handlers]
```

```text
cmd/
  app/                     current application server

core/
  Message                  business packet
  Context                  handler context
  Session                  transport-neutral send/close handle
  Router                   msgID router and middleware chain
  Invocation               unified middleware context
  Middleware               shared invocation middleware

codec/
  StreamCodec           stream decoder/encoder, used by TCP
  PacketCodec           packet decoder/encoder, used by UDP/WebSocket
  KProV1                   default binary protocol

transport/
  transport.go             common transport interfaces
  tcp/                     TCP stream transport
  kcp/                     KCP reliable UDP stream transport
  quic/                    QUIC stream transport
  udp/                     UDP packet transport
  websocket/               WebSocket transport
  httpx/                   HTTP/HTTPS path-routed transport
  http3/                   HTTP/3 path-routed transport over QUIC
  grpcx/                   gRPC server wrapper and generated-style bindings
  sse/                     Server-Sent Events transport

transport/grpcx/
  Server                   gRPC runner and service registrar
  Middleware bridge        unary/stream RPC -> core.Invocation

middleware/
  Recover / RateLimit / MaxPayload / Metrics
                           unified invocation middleware

server/
  Server                   universal assembly layer
  Endpoint                 transport + middleware + router
```

The core runtime path for Message-based transports is:

```mermaid
sequenceDiagram
    participant Client
    participant Transport
    participant Protocol
    participant Router
    participant Handler

    Client->>Transport: bytes / datagram / WebSocket message
    Transport->>Codec: raw payload
    Protocol->>Router: core.Message
    Router->>Handler: core.Context
    Handler->>Router: ctx.Reply()
    Router->>Codec: response Message
    Protocol->>Transport: encoded response
    Transport->>Client: response
```

```text
	TCP / KCP / QUIC / UDP / WebSocket
  -> transport reads bytes, datagram or WebSocket message
  -> protocol decodes core.Message
  -> core.Router dispatches by Message.ID
  -> queue / scheduling
  -> core.Invocation
  -> core.Middleware
  -> business handler
  -> ctx.Reply / session.Send
  -> protocol encodes response
  -> transport writes response
```

HTTP and HTTPS keep protocol-native method + path routing:

```mermaid
flowchart LR
    HTTPClient[HTTP Client] --> HTTPRouter[method + path router]
    HTTPRouter --> HTTPHandler[httpx.Context handler]
    HTTPHandler --> HTTPResponse[Text / JSON response]

    SSEClient[SSE Client] --> SSEEndpoint[SSE endpoint]
    SSEEndpoint --> SSEContext[sse.Context]
    SSEContext --> Events[Send event stream]
```

```text
HTTP / HTTPS
  -> path + method router
  -> core.Invocation
  -> core.Middleware
  -> httpx.Context

SSE
  -> HTTP streaming endpoint
  -> core.Invocation
  -> core.Middleware at stream setup
  -> sse.Context.Send(event, data)
```

## Default Protocol

`codec.KProV1()` is the default binary codec.

```text
20-byte big-endian header + body

uint16 magic       0x4b50
uint8  version     1
uint8  flags
uint32 message id
uint64 sequence
uint32 body length
bytes  body
```

Built-in message IDs:

```text
1          heartbeat
2          pong
0xffffffff error
```

Application message IDs are user-defined. The examples below use `100` as a local echo message ID.

## App Server Example

```go
package main

import (
	"log"
	"time"

	"github.com/saramand9/bignet/kpro/core"
	"github.com/saramand9/bignet/kpro/middleware"
	"github.com/saramand9/bignet/kpro/codec"
	"github.com/saramand9/bignet/kpro/server"
	http3x "github.com/saramand9/bignet/kpro/transport/http3"
	"github.com/saramand9/bignet/kpro/transport/httpx"
	"github.com/saramand9/bignet/kpro/transport/kcp"
	quictransport "github.com/saramand9/bignet/kpro/transport/quic"
	"github.com/saramand9/bignet/kpro/transport/sse"
	"github.com/saramand9/bignet/kpro/transport/tcp"
	"github.com/saramand9/bignet/kpro/transport/udp"
	"github.com/saramand9/bignet/kpro/transport/websocket"
)

const msgEcho uint32 = 100

func main() {
	app := server.New(server.Config{Name: "gateway"})
	metrics := middleware.NewMetrics()

	app.TCP(":8088", tcp.Config{
		Codec: codec.KProV1(),
	}).Use(middleware.Recover(), metrics.Middleware()).
		Handle(msgEcho, echo)

	app.KCP(":8090", kcp.Config{
		Codec: codec.KProV1(),
	}).Use(middleware.Recover(), metrics.Middleware()).
		Handle(msgEcho, echo)

	app.QUIC(":8444", quictransport.Config{
		Codec: codec.KProV1(),
		CertFile: "server.crt",
		KeyFile:  "server.key",
	}).Use(middleware.Recover(), metrics.Middleware()).
		Handle(msgEcho, echo)

	app.UDP(":8089", udp.Config{
		Codec:      codec.KProV1(),
		MaxPacketSize: 2048,
	}).Use(middleware.Recover(), metrics.Middleware()).
		Handle(msgEcho, echo)

	app.WebSocket(":8091", "/ws", websocket.Config{
		Codec:   codec.KProV1(),
		MaxMessage: 64 * 1024,
	}).Use(middleware.Recover(), metrics.Middleware()).
		Handle(msgEcho, echo)

	app.HTTP(":8080").
		Get("/health", func(ctx *httpx.Context) error {
			return ctx.Text(200, "ok\n")
		}).
		Get("/metrics", func(ctx *httpx.Context) error {
			return ctx.JSON(200, metrics.Snapshot())
		}).
		Post("/echo", func(ctx *httpx.Context) error {
			body, err := ctx.Body()
			if err != nil {
				return err
			}
			return ctx.JSON(200, map[string]interface{}{"echo": string(body)})
		})

	app.HTTP3(":8443", http3x.Config{
		CertFile: "server.crt",
		KeyFile:  "server.key",
	}).Get("/health", func(ctx *http3x.Context) error {
		return ctx.Text(200, "ok\n")
	})

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

	log.Fatal(app.ListenAndServe())
}

func echo(ctx core.Context) error {
	msg := ctx.Message()
	return ctx.Reply(msg.ID, msg.Data)
}
```

## Run

```powershell
cd D:\dev\saramand9\bignet\kpro
go run .\cmd\app -tcp :8088 -kcp :8090 -udp :8089 -ws :8091 -http :8080 -sse :8081 -gorm-driver sqlite -gorm-dsn file:kpro-app.db
```

HTTP/3 is disabled by default in the app server because it requires TLS
certificates and listens on UDP:

```powershell
go run .\cmd\app -http3 :8443 -http3-cert server.crt -http3-key server.key
```

QUIC message transport is also disabled by default because it requires TLS
certificates and listens on UDP:

```powershell
go run .\cmd\app -quic :8444 -quic-cert server.crt -quic-key server.key
```

Limit active QUIC stream sessions with:

```powershell
go run .\cmd\app -quic :8444 -quic-cert server.crt -quic-key server.key -quic-max-streams 10000
```

HTTP validation:

```powershell
Invoke-RestMethod -Uri http://127.0.0.1:8080/health
Invoke-RestMethod -Uri http://127.0.0.1:8080/echo -Method Post -Body "hello" -ContentType "text/plain"
```

## Examples

Standalone examples live under [examples/](examples/). Each protocol has a server and client:

```text
examples/tcp/server          examples/tcp/client
examples/udp/server          examples/udp/client
examples/websocket/server    examples/websocket/client
examples/http/server         examples/http/client
examples/https/server        examples/https/client
examples/http2/server        examples/http2/client
examples/http3/server        examples/http3/client
examples/sse/server          examples/sse/client
examples/grpc/server         examples/grpc/client
```

See [examples/README.md](examples/README.md) for startup commands.

## Design Notes

- TCP, KCP and QUIC are stream-oriented, so they use `codec.StreamCodec`.
- UDP and WebSocket have message boundaries, so they use `codec.PacketCodec`.
- HTTP/HTTPS keep native method + path routing for REST-style APIs and do not convert requests into `core.Message`.
- HTTP/3 uses the same path-routing handler model over QUIC/UDP and requires TLS certificates.
- QUIC uses bidirectional QUIC streams as `core.Session` instances and runs KPro messages directly over them. `quic.Config.MaxStreams` limits active stream sessions; the older `MaxConnections` field is kept as a compatibility alias.
- `server.Server.Use(...)` installs unified `core.Middleware` for TCP, UDP, WebSocket, HTTP, HTTPS, SSE and gRPC endpoints mounted before or after the call.
- `core.Middleware` runs on `core.Invocation`; TCP, UDP and WebSocket build the invocation at real handler execution time after Router scheduling.
- SSE is one-way server push and has its own `sse.Context`; the stream setup can reuse unified `core.Middleware`.
- gRPC uses native gRPC service and method routing. Unary and stream RPCs reuse unified `core.Middleware` through `server.GRPC` / `grpcx.Server.Use`; they are not converted into `core.Message`.
- The framework avoids exposing a Netty-style pipeline API. The public model is `Protocol + Middleware + Router + Handler`.
- App server usage is documented in [docs/app-server.md](docs/app-server.md).
- GORM database and eventing architecture are documented in [docs/database-architecture.md](docs/database-architecture.md).
- More detailed call-flow diagrams are in [docs/call-flow.md](docs/call-flow.md).
- TCP internal flow guide is in [docs/tcp-internal-flow.md](docs/tcp-internal-flow.md).
- Network correctness requirements for message-based transports are in [docs/network-test-requirements.md](docs/network-test-requirements.md).

## Test

```powershell
$env:GOCACHE='C:\tmp\go-build-bignet-kpro'
gofmt -l .
go test ./...
go vet ./...

Push/PR CI also runs:

go test -race ./...

cd .\benchmark\compare
go test ./...
go vet ./...
```








