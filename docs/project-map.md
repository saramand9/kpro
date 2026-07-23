# Project Map

This map explains how the repository is organized and where to start when
changing a runtime behavior. It is intentionally higher level than package
documentation and lower level than the README.

## System Shape

KPro separates application assembly from protocol implementation:

```text
server.Server
  -> endpoint or runner mounting
  -> transport implementation
  -> codec or native protocol bridge
  -> core.Router or protocol-native handler
  -> core.Invocation middleware
  -> application handler
```

Message-based transports use `core.Message` and `core.Context`. HTTP, SSE, and
gRPC keep protocol-native handler surfaces where that is the clearer contract
for application code.

## Top-Level Packages

| Path | Primary role | Key files |
| --- | --- | --- |
| `core/` | Transport-neutral runtime primitives and message dispatch. | `router.go`, `router_config.go`, `context.go`, `session_lifecycle.go`, `invocation.go`, `backpressure.go`, `lifecycle.go` |
| `server/` | Process-level owner for transports, middleware, runners, and managed resources. | `server.go`, `lifecycle.go` |
| `transport/` | Shared transport contracts, capability descriptors, validation, and built-in protocol packages. | `transport.go`, `capability.go`, `validation.go` |
| `codec/` | Binary protocol implementation for KPro message traffic. | `kpro.go`, `codec.go` |
| `middleware/` | Reusable invocation middleware. | `recover.go`, `ratelimit.go`, `limit.go`, `metrics.go` |
| `observability/` | Optional integrations for health, Prometheus, OTel, and Zap. | `healthx/`, `prometheusx/`, `otelx/`, `zapx/` |
| `eventing/` | Event envelopes, delivery store contracts, GORM outbox, projector runner. | `events/`, `delivery/`, `projector/` |
| `database/` | Database adapters and lifecycle/resource helpers. | `gorm/`, `redis/`, `es/`, `qdrant/`, `mongo/`, `clickhouse/`, `influx/`, `neo4j/`, `common/` |
| `cmd/kpro/` | CLI for scaffolding a checked starter service. | `main.go`, `templates/service/` |
| `cmd/benchguard/` | Benchmark budget checker used by CI. | `main.go` |
| `examples/` | Protocol and modular application examples. | `quickstart/`, protocol-specific directories |
| `benchmark/` | Microbenchmarks, budget config, and comparison fixtures. | `budgets.json`, `protocol_benchmark_test.go`, `compare/` |
| `docs/` | Guides for adoption, operation, extension, and internal flows. | `README.md`, `quickstart.md`, `golden-path.md`, `operations.md` |

## Runtime Flow

Server startup follows this path:

```text
application main
  -> server.NewChecked
  -> app.Use / app.HTTP / app.Mount / app.Manage
  -> app.ListenAndServe
  -> validate mounted components
  -> claim one-shot lifecycle
  -> start mounted runners
```

Shutdown follows this path:

```text
signal or runner failure
  -> app.Shutdown(ctx)
  -> stop accepting new sessions/listeners
  -> cancel endpoint contexts
  -> drain bounded in-flight work
  -> stop mounted runners
  -> close managed resources
```

Message transport dispatch follows this path:

```text
client bytes/datagram/frame
  -> transport session
  -> codec.KProV1 decode
  -> core.Router dispatch
  -> queue and in-flight admission
  -> core.Invocation middleware chain
  -> registered message handler
  -> ctx.Reply or session.Send
  -> codec encode
  -> transport write
```

HTTP-style dispatch follows this path:

```text
HTTP client
  -> httpx or http3 endpoint
  -> method/path route lookup
  -> core.Invocation middleware bridge
  -> httpx.Context handler
  -> response writer
```

gRPC dispatch follows this path:

```text
gRPC client
  -> grpcx server
  -> unary or stream interceptor bridge
  -> core.Invocation middleware chain
  -> registered gRPC service handler
```

## Lifecycle And Backpressure Controls

Use `server.NewChecked` for construction-time validation and let
`ListenAndServe` aggregate transport `Validate` failures before any serving
goroutine starts. Built-in servers and transports are one-shot: repeated starts
return `core.ErrAlreadyStarted`.

Use `core.RouterConfig` when message traffic needs explicit worker, queue,
per-session in-flight, global in-flight, idle, and shutdown behavior. Treat
Router and transport limits as admission controls, not business validation.

HTTP-style endpoints require explicit timeout and body-size decisions. The
production checklist records which limits are built in and which remain
application or platform work.

## Extension Points

Add a transport by implementing the common contracts in `transport/`, including
descriptors and validation. Follow [extending-kpro.md](extending-kpro.md) and
mirror existing package tests for lifecycle, capacity, closed-session sends,
and cancellation.

Add middleware through `core.Middleware` and `core.Invocation`; protocol bridges
should preserve stable operation names and bounded metadata.

Add database behavior behind module-owned ports where possible. The framework
adapters provide lifecycle and infrastructure primitives, but business modules
should avoid depending directly on concrete storage types.

## Evidence Files

- [production-readiness.md](production-readiness.md) defines built-in,
  configure-and-verify, and external responsibilities.
- [operations.md](operations.md) records overload, failure, and capacity
  planning guidance.
- [api-stability.md](api-stability.md) defines the current pre-1.0 compatibility
  posture.
- [benchmark/budgets.json](../benchmark/budgets.json) defines hot-path
  regression budgets.
- [CHANGELOG.md](../CHANGELOG.md) records repository-level changes.
