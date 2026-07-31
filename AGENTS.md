# Agent Orientation

Use this file as the first pass when reopening the repository for analysis or
implementation work. It is an orientation aid, not a release contract.

## Project Snapshot

KPro is a pre-1.0 Go multi-transport application framework. It assembles TCP,
KCP, QUIC, UDP, WebSocket, HTTP, HTTPS, HTTP/2, HTTP/3, SSE, and gRPC behind one
server/runtime model while allowing protocol-native handlers where that is the
better application interface.

The repository is pre-1.0. `v0.1.0` is the initial developer release, not a v1
compatibility guarantee. Pin an exact release or reviewed commit and read
[docs/api-stability.md](docs/api-stability.md) before treating exported APIs as
stable.

Authentication and authorization are intentionally application or gateway
responsibilities. Do not add framework-level identity or policy features unless
the task explicitly changes that boundary.

## Fast Read Order

1. [README.md](README.md) for status, supported transports, and the top-level
   architecture.
2. [docs/analysis-index.md](docs/analysis-index.md) for the fastest analysis
   route, important files, and current evidence.
3. [docs/project-map.md](docs/project-map.md) for package ownership and runtime
   flow.
4. [docs/dependency-boundaries.md](docs/dependency-boundaries.md) before changing
   server/transport imports or module boundaries.
5. [docs/golden-path.md](docs/golden-path.md) for the recommended application
   shape.
6. [docs/production-readiness.md](docs/production-readiness.md) for production
   boundaries and required external validation.

## Package Map

| Path | Responsibility |
| --- | --- |
| `core/` | Message, Context, Session, Router, middleware invocation, lifecycle state, backpressure. |
| `server/` | Dependency-neutral process assembly, checked construction, endpoint mounting, runner ownership, shutdown. |
| `transport/` | Common transport contracts, descriptors, validation, and protocol implementations. |
| `codec/` | KPro binary message codecs for stream and packet transports. |
| `middleware/` | Shared invocation middleware such as recovery, limits, metrics, and rate limiting. |
| `observability/` | Optional health, Prometheus, OpenTelemetry, and Zap integrations. |
| `eventing/` | Event envelopes, transactional delivery store contracts, GORM outbox, projector runner. |
| `database/` | Database adapters and common lifecycle/resource helpers. |
| `cmd/kpro/` | Service scaffolding, local template overlays, contract catalogs, and code generation CLI. |
| `cmd/benchguard/` | Benchmark budget enforcement tool. |
| `examples/` | Standalone and modular protocol examples. |
| `benchmark/` | Hot-path benchmarks, comparison fixtures, and budget configuration. |
| `docs/` | Architecture, operation, extension, and readiness guides. |

## Worktree Rules

- Preserve user-owned changes. Do not revert or overwrite files you did not
  create unless the user explicitly asks for it.
- Do not read, stage, commit, or push untracked secret/key files unless the user
  explicitly names them as part of the task.
- Prefer small, targeted edits that follow existing package boundaries.
- Keep documentation factual: distinguish implemented mechanisms from production
  certification or service-level guarantees.

## Common Commands

Run focused checks for runtime/framework changes:

```sh
go test ./core ./server ./transport/... ./observability/... ./eventing/... ./cmd/kpro ./cmd/benchguard -count=1
```

Run the full repository test suite when touching shared contracts:

```sh
go test -p 1 ./... -count=1
```

Run race-sensitive coverage for core runtime packages:

```sh
go test -race ./core ./server ./transport/... ./observability/... ./eventing/... ./cmd/kpro ./cmd/benchguard -count=1
```

Check formatting, dependency metadata, and benchmark budgets before a release
or large commit:

```sh
gofmt -w <changed-go-files>
go mod tidy -diff
go vet ./...
go test -bench=. -benchmem ./benchmark > benchmark.out
go run ./cmd/benchguard -budget benchmark/budgets.json -input benchmark.out
```

Only run external database integration tests when the required engines and
connection settings are available:

```sh
go test -tags dbintegration ./database/gorm ./eventing/delivery
```

## Current Technical Posture

The framework now has checked server construction, transport preflight
validation, one-shot lifecycle ownership, bounded Router queues/in-flight
limits, transport capacity guards, graceful shutdown behavior, observability
adapters, event delivery/projector operational controls, scaffolding,
declarative template overlays, contract catalogs, typed OpenAPI clients, and
benchmark budgets.

Remaining maturity gaps are a v1 API freeze, workload-specific load tests,
failure-injection evidence, and production deployment records.
