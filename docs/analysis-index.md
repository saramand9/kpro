# Analysis Index

This index is optimized for quickly reopening the repository and getting back
to useful technical analysis. It points to stable reading entry points, key code
paths, and checks that establish current evidence.

## Current State Summary

KPro is a pre-1.0 Go framework for assembling a multi-transport application
server. The current implementation includes:

- checked server construction and mounted-component validation;
- explicit server and transport lifecycle states;
- bounded message Router queues/in-flight admission, globally and per-session
  bounded `BackpressureWait` callers, and graceful draining of accepted
  sync/async work;
- transport capacity limits, count/byte bounded outbound writes, observable
  high/low-watermark state, bounded advisory writable waiting, and optional
  pre-drain quiesce gates;
- optional health, Prometheus, OTel, and Zap packages;
- GORM-backed transactional delivery/outbox and projector controls;
- unified service scaffolding, versioned local overlays, contract catalogs,
  server/client code generation, and transactional API additions through
  `cmd/kpro`;
- multi-sample benchmark budget enforcement through `cmd/benchguard`, including
  zero-allocation outbound-queue and Router/HTTP/codec regression guards.

The repository provides the initial `v0.1.0` developer release, but not a v1
compatibility guarantee, production certification, identity/policy management,
telemetry backends, or workload-specific capacity promises.

## Fast Analysis Route

Use this route for a general project assessment:

1. Read [README.md](../README.md) for status, architecture, and supported
   transports.
2. Read [project-map.md](project-map.md) for package ownership and runtime flow.
3. Read [framework-quality-assessment.md](framework-quality-assessment.md) for
   the current scored baseline, evidence gaps, and reassessment rules.
4. Read [golden-path.md](golden-path.md) for recommended application assembly.
5. Read [code-generation.md](code-generation.md) for the unified `service`
   profile, generated ownership, and OpenAPI, protobuf, and Message backends.
6. Read [dependency-boundaries.md](dependency-boundaries.md) before changing
   server/transport imports or introducing another module.
7. Read [production-readiness.md](production-readiness.md) for built-in versus
   external production work.
8. Read [api-stability.md](api-stability.md) before evaluating public API
   maturity.
9. Check [benchmark/budgets.json](../benchmark/budgets.json) and
   [benchmark/RESULTS.md](../benchmark/RESULTS.md) before making performance
   claims.

Use this route for code-level runtime analysis:

1. `server/server.go`
2. `server/mount.go`
3. `core/router.go`
4. `core/invocation.go`
5. `core/outbound_state.go`
6. `transport/transport.go`
7. `transport/capability.go`
8. `transport/internal/dispatchgate/gate.go`
9. `transport/internal/outbound/queue.go`
10. `transport/validation.go`
11. The specific transport package being changed.

Use this route for eventing/data analysis:

1. `eventing/events/`
2. `eventing/delivery/store.go`
3. `eventing/delivery/gorm_store.go`
4. `eventing/projector/runner.go`
5. `docs/database-architecture.md`
6. `docs/database-production.md`

## Key Runtime Files

| Concern | Files |
| --- | --- |
| Server construction and lifecycle | `server/server.go`, `server/mount.go`, `server/lifecycle.go`, `server/lifecycle_test.go` |
| Router admission and dispatch | `core/router.go`, `core/router_config.go`, `core/backpressure.go`, `core/router_backpressure_test.go` |
| Middleware contract | `core/invocation.go`, `middleware/*.go`, `docs/invocation-middleware-design.md` |
| Context/session lifecycle and outbound state | `core/context.go`, `core/session.go`, `core/session_lifecycle.go`, `core/outbound_state.go` |
| Transport contracts and quiesce lifecycle | `transport/transport.go`, `transport/capability.go`, `transport/validation.go`, `server/server.go`, `server/endpoint_registration_test.go` |
| Shared transport validation | `transport/internal/configcheck/` |
| Shared cancellation helpers | `transport/internal/serveutil/` |
| Shared message dispatch quiesce gate | `transport/internal/dispatchgate/` |
| Shared outbound count/byte accounting | `transport/internal/outbound/` |
| HTTP path | `transport/httpx/`, `transport/http3/` |
| Message transports | `transport/tcp/`, `transport/kcp/`, `transport/quic/`, `transport/udp/`, `transport/websocket/` |
| Streaming/native RPC | `transport/sse/`, `transport/grpcx/` |
| Observability | `observability/healthx/`, `observability/prometheusx/`, `observability/otelx/`, `observability/zapx/` |
| Event delivery | `eventing/delivery/`, `eventing/projector/` |
| Scaffolding, template overlays, API catalogs, incremental additions, and code generation | `cmd/kpro/`, `cmd/kpro/scaffold_template.go`, `cmd/kpro/command_catalog.go`, `cmd/kpro/service_project.go`, `cmd/kpro/templates/profiles/service/`, `cmd/kpro/templates/service-project/`, `internal/codegen/`, `docs/code-generation.md` |
| Performance budgets | `benchmark/`, `cmd/benchguard/` |

## Checks To Reuse

Focused framework check:

```sh
go test ./core ./server ./transport/... ./observability/... ./eventing/... ./cmd/kpro ./cmd/benchguard -count=1
```

Full repository check:

```sh
go test -p 1 ./... -count=1
```

Race-sensitive runtime check:

```sh
go test -race ./core ./server ./transport/... ./observability/... ./eventing/... ./cmd/kpro ./cmd/benchguard -count=1
```

Formatting and dependency checks:

```sh
gofmt -w <changed-go-files>
go mod tidy -diff
go vet ./...
```

Benchmark budget check:

```sh
go test -p 1 ./core ./transport/httpx ./transport/internal/outbound ./benchmark -run '^$' -bench 'Benchmark(RouterDispatchSync|EndpointServeHTTP|OutboundQueueRoundTrip|KProCodec)$' -benchmem -benchtime=250ms -count=3 > benchmark.out
go run ./cmd/benchguard -budget benchmark/budgets.json -input benchmark.out
```

Database integration compile or execution depends on external engine
availability:

```sh
go test -tags dbintegration ./database/gorm ./eventing/delivery
```

## Assessment Anchors

When evaluating the project, separate these categories:

- Implemented mechanism: code and tests exist in the repository.
- Configurable framework control: the framework exposes limits or hooks, but
  the application must choose values.
- External production responsibility: deployment, identity, telemetry backends,
  release process, load testing, and service-level objectives.

This separation is important because KPro deliberately avoids claiming that an
implemented transport or adapter is a production certification.

Use [framework-quality-assessment.md](framework-quality-assessment.md) when a
numeric score is required. Preserve its baseline commit, evidence gaps, and
gate rules rather than replacing the score with an unversioned impression.

## Known Boundaries

- `v0.1.0` is the initial developer release; there is no v1 compatibility
  guarantee yet.
- Authentication and authorization remain outside the framework core.
- HTTP and HTTP/3 routing uses Go `ServeMux` patterns and includes checked
  registration, path parameters, route groups, and route-local middleware.
- Prometheus, OTel, Zap, and health helpers are integration points; the
  application owns exporter/backend/sampler/log-retention policy.
- Performance budgets are regression thresholds for repository benchmarks, not
  service capacity objectives.
- `BackpressureWait` defaults to at most 1024 blocked dispatches globally and
  16 per session (further capped by in-flight defaults). One dispatch consumes
  one ticket across all wait stages; snapshot/Prometheus rejection counters
  distinguish the two bounds.
- TCP, KCP, QUIC, UDP, and WebSocket support optional pre-drain quiesce. A
  custom message transport without `transport.Quiescer` keeps the compatible
  Router-drain-then-shutdown path but not the same receive-side cutover.
- Outbound writability state and `SessionWaitOutboundWritable` are advisory.
  Waiting is bounded per session and reserves no capacity for the next
  `Send`; hard-full behavior still differs between connection-oriented
  built-ins and UDP.
- Database adapters still require deployment-specific tests against the chosen
  engine, topology, TLS mode, and failure policy.

## Do Not Recreate As Repo Artifacts

Do not commit editor indexes, `gopls` caches, local search databases, generated
temporary analysis dumps, or machine-specific benchmark output. Keep durable
analysis in this document,
[framework-quality-assessment.md](framework-quality-assessment.md),
[project-map.md](project-map.md), or the relevant guide.
