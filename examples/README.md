# KPro examples

Each protocol example has a standalone server and client. Start the server in one terminal, then run the matching client in another terminal.

Commands below use forward slashes and run from the repository root in POSIX
shells and PowerShell. Use `go run ./cmd/kpro doctor` to print local Go/module
diagnostics and `go run ./cmd/kpro version` to inspect CLI build metadata.

## Recommended Quickstart

For a first application, run the production-shaped HTTP example with explicit limits, middleware, signal handling, and graceful shutdown:

```sh
go run ./examples/quickstart -addr :8080
```

Then follow the requests and code walkthrough in [the 10-minute guide](../docs/quickstart.md). Authentication and authorization are application integrations and intentionally not part of this minimal example.

Create a separate starter service in a missing or empty directory with:

```sh
go run ./cmd/kpro new ../hello-kpro --module example.com/hello-kpro --name hello-kpro
```

## Observability and Health

This example keeps application traffic on `:8080` and standard-library health
and Prometheus handlers on an independently bounded admin listener at `:9090`:

```sh
go run ./examples/observability -api-addr :8080 -admin-addr :9090
curl http://127.0.0.1:8080/hello
curl http://127.0.0.1:9090/livez
curl http://127.0.0.1:9090/readyz
curl http://127.0.0.1:9090/metrics
```

It wires `healthx`, `prometheusx`, `zapx`, `otelx`, checked server
configuration, `Server.Start`/`RunHandle.WaitReady`, and a custom runner
descriptor/validator. The OTel middleware
uses the global provider in this runnable example; no exporter is installed. A
real application must configure and own its SDK/exporter lifecycle. Every
health check must honor the context supplied by `healthx`. The admin listener's
production exposure, TLS, and access policy are application/platform concerns.

## TCP

```sh
go run ./examples/tcp/server -addr :8088
go run ./examples/tcp/client -addr 127.0.0.1:8088 -text "hello tcp"
```

## KCP

```sh
go run ./examples/kcp/server -addr :8090
go run ./examples/kcp/client -addr 127.0.0.1:8090 -text "hello kcp"
```

## QUIC message streams

The server creates an in-memory self-signed localhost certificate. The client
skips verification only for this local example.

```sh
go run ./examples/quic/server -addr :8444
go run ./examples/quic/client -addr 127.0.0.1:8444 -text "hello quic"
```

## UDP

```sh
go run ./examples/udp/server -addr :8089
go run ./examples/udp/client -addr 127.0.0.1:8089 -text "hello udp"
```

## WebSocket

```sh
go run ./examples/websocket/server -addr :8091 -path /ws
go run ./examples/websocket/client -url ws://127.0.0.1:8091/ws -text "hello websocket"
```

## HTTP

```sh
go run ./examples/http/server -addr :8080
go run ./examples/http/client -base http://127.0.0.1:8080 -text "hello http"
```

## HTTPS

The HTTPS server generates a local self-signed certificate at startup. The client skips certificate verification because this is a local example.

```sh
go run ./examples/https/server -addr :8443
go run ./examples/https/client -base https://127.0.0.1:8443 -text "hello https"
```

## HTTP/2

The HTTP/2 example uses TLS and verifies that the client negotiated `HTTP/2.0`.

```sh
go run ./examples/http2/server -addr :8442
go run ./examples/http2/client -base https://127.0.0.1:8442 -text "hello http2"
```

## HTTP/3

The HTTP/3 example listens on UDP through QUIC and verifies that the client
negotiated `HTTP/3.0`. HTTP/3 0-RTT is disabled by default because early data
can be replayed. Production endpoints should also set `MaxConnections`,
`MaxBodyBytes`, header limits, and timeouts explicitly.

```sh
go run ./examples/http3/server -addr :8443
go run ./examples/http3/client -base https://127.0.0.1:8443 -text "hello http3"
```

## SSE

The SSE client reads a few events and exits.

```sh
go run ./examples/sse/server -addr :8081 -path /events
go run ./examples/sse/client -url http://127.0.0.1:8081/events -events 3
```

## gRPC

```sh
go run ./examples/grpc/server -addr :9090
go run ./examples/grpc/client -addr 127.0.0.1:9090 -name "hello grpc"
```

## Modular Route Examples

These demos keep startup code, route assembly, and feature modules separate.
Modular variants are provided for the protocols listed below.
Message-protocol modular examples use `HandleSync` by default; use `HandleAsync` for routes that can run concurrently and return out of order.

### HTTP Modular

```sh
go run ./examples/http-modular/server -addr :8080
go run ./examples/http/client -base http://127.0.0.1:8080 -text "hello modular http"
curl http://127.0.0.1:8080/users
curl http://127.0.0.1:8080/users/active
```

### HTTPS Modular

```sh
go run ./examples/https-modular/server -addr :8443
go run ./examples/https/client -base https://127.0.0.1:8443 -text "hello modular https"
```

### TCP Modular

```sh
go run ./examples/tcp-modular/server -addr :8088
go run ./examples/tcp/client -addr 127.0.0.1:8088 -text "hello modular tcp"
```

### UDP Modular

```sh
go run ./examples/udp-modular/server -addr :8089
go run ./examples/udp/client -addr 127.0.0.1:8089 -text "hello modular udp"
```

### WebSocket Modular

```sh
go run ./examples/websocket-modular/server -addr :8091 -path /ws
go run ./examples/websocket/client -url ws://127.0.0.1:8091/ws -text "hello modular websocket"
```

### SSE Modular

```sh
go run ./examples/sse-modular/server -addr :8081 -path /events
go run ./examples/sse/client -url http://127.0.0.1:8081/events -events 3
```

### gRPC Modular

```sh
go run ./examples/grpc-modular/server -addr :9090
go run ./examples/grpc/client -addr 127.0.0.1:9090 -name "hello modular grpc"
```

### Multi-Protocol Modular

This demo uses one modular route assembly layer for HTTP, HTTPS, TCP, UDP, WebSocket and SSE. The `echo` module registers both HTTP and message-protocol routes.

```sh
go run ./examples/modular/server
go run ./examples/http/client -base http://127.0.0.1:8080 -text "hello modular http"
go run ./examples/tcp/client -addr 127.0.0.1:8088 -text "hello modular tcp"
go run ./examples/udp/client -addr 127.0.0.1:8089 -text "hello modular udp"
go run ./examples/websocket/client -url ws://127.0.0.1:8091/ws -text "hello modular websocket"
go run ./examples/sse/client -url http://127.0.0.1:8081/events -events 3
curl http://127.0.0.1:8080/users
```

Enable HTTPS on the same HTTP route modules:

```sh
go run ./examples/modular/server -https :8443
go run ./examples/https/client -base https://127.0.0.1:8443 -text "hello modular https"
```

## Compile All Examples

```sh
go test ./examples/...
```
