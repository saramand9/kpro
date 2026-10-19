# Contributing to KPro

KPro welcomes focused bug reports, design feedback, reproducible performance
results, and narrowly scoped changes. The project is currently pre-1.0, so
feedback about API ergonomics and production boundaries is especially useful.

## Before changing code

1. Search existing issues and describe the concrete workload or failure mode.
2. For public API changes, explain why an optional capability or additive API
   cannot solve the problem.
3. Keep the dependency direction `core -> optional integrations` one-way:
   `core` must not import transport implementations, observability exporters,
   database drivers, or application modules.
4. Do not include credentials, private certificates, production payloads, or
   customer data in issues, tests, benchmark output, or commits.

## Local checks

Run from the repository root:

```sh
gofmt -w <changed-go-files>
go test ./...
go vet ./...
go test -race ./core ./server ./transport/... ./eventing/... ./observability/...
go mod verify
```

The complete race suite can take longer. Database integration tests require the
environment described in [Database Production](docs/database-production.md).

For hot-path changes, run the budgeted microbenchmarks:

```sh
go test -p 1 ./core ./transport/httpx ./transport/internal/outbound ./transport/internal/dispatchgate ./transport/tcp ./transport/websocket ./benchmark -run '^$' \
  -bench 'Benchmark(RouterDispatchSync|EndpointServeHTTP|OutboundQueueRoundTrip|OutboundQueueDrainAvailable|GateBeginEnd|KProCodec|SessionSendContiguous|SessionSendSegmented|ReserveOutboundBufferCommit|TCPSessionBatchWrite16x1KiB|WebSocketSessionBatchWrite16x1KiB)$' \
  -benchmem -benchtime=250ms -count=3 > benchmark-current.txt
go run ./cmd/benchguard -budget benchmark/budgets.json -input benchmark-current.txt
```

Include repeated benchmark output and environment details when claiming a
performance improvement. Localhost end-to-end timings are noisy; prefer
medians and do not infer production capacity from one machine.

## Change expectations

- Add regression tests before or with a bug fix.
- Preserve cancellation, shutdown, and ownership semantics on all error paths.
- Keep metric labels bounded; never use connection IDs, remote addresses,
  event IDs, or raw error messages as Prometheus labels.
- Document public behavior and migration impact.
- Update [CHANGELOG.md](CHANGELOG.md) for user-visible changes.
- Follow the compatibility and deprecation process in
  [API Stability](docs/api-stability.md).

Maintainers may ask for a smaller change when a proposal combines unrelated
architecture, behavior, and formatting work. This keeps review and rollback
practical.
