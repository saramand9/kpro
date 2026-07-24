# Dependency Boundaries

KPro keeps process orchestration independent from concrete protocols. The
`server` package owns lifecycle, middleware propagation, readiness, validation,
mounted runners, and managed resources; it imports only `core` and the shared
`transport` contracts.

```text
application
  -> server
       -> core
       -> transport contracts
  -> selected concrete transports
       -> httpx / tcp / grpcx / quic / ...
```

This direction prevents an HTTP-only application from compiling every built-in
protocol merely because it needs the common server lifecycle.

## Assembly

Construct message transports in their protocol package and mount them through
`Server.Mount`:

```go
tcpEndpoint := app.Mount(tcp.New(tcp.Config{
	Addr:  ":8088",
	Codec: codec.KProV1(),
}))
tcpEndpoint.HandleSync(msgEcho, echo)
```

Construct native runners such as HTTP, SSE, HTTP/3, and gRPC separately. Keep
the concrete value for route or service registration, then mount it:

```go
httpEndpoint, err := server.MountNativeChecked(app, httpx.New(httpx.Config{
	Addr: ":8080",
}))
if err != nil {
	return err
}
httpEndpoint.Get("/healthz", health)
```

`MountNative` and `MountNativeChecked` are generic dependency-neutral helpers:
they preserve the endpoint's concrete type without importing it into `server`.
Direct `MountRunner` calls remain available when the concrete return value is
already held by the application.

App-level middleware registered before or after mounting is still applied to
every runner implementing `transport.MiddlewareTarget`. Startup preflight,
readiness, shutdown, and component-error behavior are unchanged.

## Boundary Rule

Production files in `server/` must not import a concrete package below
`transport/`. Protocol packages may depend on `core` and shared transport
contracts, but the common lifecycle must not depend back on them.

Keep protocol-specific configuration, certificates, codecs, routes, and service
registration in application composition code. Do not add a new convenience
method to `Server` that returns a concrete protocol type; doing so recreates the
transitive dependency fan-out.

## Module Splitting

Package-level dependency inversion is the first stage. Separate `go.mod` files
are justified only when measurements show material module-graph, build, update,
or vulnerability-management cost.

When that stage is needed, split the least-coupled heavy adapters first:

1. ClickHouse, Elasticsearch, MongoDB, Neo4j, and InfluxDB adapters.
2. gRPC, QUIC/HTTP/3, and KCP transports.
3. Redis and GORM/eventing integrations after their shared contracts are clear.

The root module must never import an optional child module. Local multi-module
development may use a Go workspace, while CI must also test every module with
`GOWORK=off` so missing versioned dependencies are visible.

## Measuring the Boundary

Use the same package-level query before and after dependency changes:

```powershell
go list -deps -f "{{if not .Standard}}{{.ImportPath}}{{end}}" ./server |
  Where-Object { $_ -ne '' } |
  Sort-Object -Unique
```

At the initial dependency-inversion change, this production-package query fell
from 170 non-standard-library packages to three (`core`, root `transport`, and
`server`). This measures compile-time package fan-out, not the complete module
graph recorded by the root `go.mod`.

Track dependency count, cold build time, binary size, `govulncheck`, and the
existing runtime benchmarks. A dependency change is not allowed to weaken
lifecycle semantics or widen a performance budget merely to make the graph
smaller.
