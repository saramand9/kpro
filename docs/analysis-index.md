# Analysis Index

This index is optimized for quickly reopening the repository and getting back
to useful technical analysis. It points to stable reading entry points, key code
paths, and checks that establish current evidence.

## Current State Summary

KPro is a pre-1.0 Go framework for assembling a multi-transport application
server. The current implementation includes:

- checked server construction and mounted-component validation;
- explicit server and transport lifecycle states;
- bounded message Router queues and in-flight admission;
- transport capacity limits and shutdown gates;
- optional health, Prometheus, OTel, and Zap packages;
- GORM-backed transactional delivery/outbox and projector controls;
- service scaffolding through `cmd/kpro`;
- benchmark budget enforcement through `cmd/benchguard`.

The repository does not currently provide release tags, a v1 compatibility
guarantee, production certification, identity/policy management, telemetry
backends, or workload-specific capacity promises.

## Fast Analysis Route

Use this route for a general project assessment:

1. Read [README.md](../README.md) for status, architecture, and supported
   transports.
2. Read [project-map.md](project-map.md) for package ownership and runtime flow.
3. Read [golden-path.md](golden-path.md) for recommended application assembly.
4. Read [production-readiness.md](production-readiness.md) for built-in versus
   external production work.
5. Read [api-stability.md](api-stability.md) before evaluating public API
   maturity.
6. Check [benchmark/budgets.json](../benchmark/budgets.json) and
   [benchmark/RESULTS.md](../benchmark/RESULTS.md) before making performance
   claims.

Use this route for code-level runtime analysis:

1. `server/server.go`
2. `core/router.go`
3. `core/invocation.go`
4. `transport/transport.go`
5. `transport/capability.go`
6. `transport/validation.go`
7. The specific transport package being changed.

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
| Server construction and lifecycle | `server/server.go`, `server/lifecycle.go`, `server/lifecycle_test.go` |
| Router admission and dispatch | `core/router.go`, `core/router_config.go`, `core/backpressure.go`, `core/router_backpressure_test.go` |
| Middleware contract | `core/invocation.go`, `middleware/*.go`, `docs/invocation-middleware-design.md` |
| Context/session lifecycle | `core/context.go`, `core/session.go`, `core/session_lifecycle.go` |
| Transport contracts | `transport/transport.go`, `transport/capability.go`, `transport/validation.go` |
| Shared transport validation | `transport/internal/configcheck/` |
| Shared cancellation helpers | `transport/internal/serveutil/` |
| HTTP path | `transport/httpx/`, `transport/http3/` |
| Message transports | `transport/tcp/`, `transport/kcp/`, `transport/quic/`, `transport/udp/`, `transport/websocket/` |
| Streaming/native RPC | `transport/sse/`, `transport/grpcx/` |
| Observability | `observability/healthx/`, `observability/prometheusx/`, `observability/otelx/`, `observability/zapx/` |
| Event delivery | `eventing/delivery/`, `eventing/projector/` |
| Scaffolding | `cmd/kpro/`, `cmd/kpro/templates/service/` |
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
go test -bench=. -benchmem ./benchmark > benchmark.out
go run ./cmd/benchguard -budgets benchmark/budgets.json -results benchmark.out
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

## Known Boundaries

- No release tags or v1 compatibility guarantee yet.
- Authentication and authorization remain outside the framework core.
- HTTP routing is exact method/path matching; route parameters and groups are
  not a built-in router feature.
- Prometheus, OTel, Zap, and health helpers are integration points; the
  application owns exporter/backend/sampler/log-retention policy.
- Performance budgets are regression thresholds for repository benchmarks, not
  service capacity objectives.
- Database adapters still require deployment-specific tests against the chosen
  engine, topology, TLS mode, and failure policy.

## Do Not Recreate As Repo Artifacts

Do not commit editor indexes, `gopls` caches, local search databases, generated
temporary analysis dumps, or machine-specific benchmark output. Keep durable
analysis in this document, [project-map.md](project-map.md), or the relevant
guide.
