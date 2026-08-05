# Recommended Golden Path

This is the default application shape for teams adopting KPro. It favors the smallest public API surface, explicit limits, and an easy path from a local HTTP service to a multi-transport application.

KPro provides the assembly and transport primitives described here. It does not certify an application for production; deployment-specific security, sizing, observability, and failure testing remain the application's responsibility.

## 1. Create a reviewable starting point

Run the checked-in [quickstart](quickstart.md) when evaluating KPro. For a new
contract-first HTTP application, use the unified `service` profile:

```sh
go run ./cmd/kpro new ../orders \
  --module example.com/orders \
  --name orders \
  --profile service \
  --api http
```

The same profile can assemble multiple API models into one process. Repeat
`--api` rather than selecting a protocol-specific profile:

```sh
go run ./cmd/kpro new ../orders \
  --module example.com/orders \
  --name orders \
  --profile service \
  --api http \
  --api grpc \
  --api message \
  --transport tcp \
  --transport websocket
```

`--api` accepts `http`, `grpc`, and `message`. A Message API requires at least
one explicit, repeatable `--transport`; the supported values are `tcp`, `kcp`,
`quic`, `udp`, and `websocket`. Each chosen Message transport receives its own
endpoint and registration. A gRPC selection requires `protoc`,
`protoc-gen-go`, and `protoc-gen-go-grpc` on `PATH`. QUIC requires application
TLS certificate and private-key paths at runtime.

The old `http-api` profile has been removed; use
`--profile service --api http`. The hand-written `minimal` profile remains for
a small HTTP service that deliberately does not use contract generation:

```sh
go run ./cmd/kpro new ../orders \
  --module example.com/orders \
  --name orders \
  --profile minimal
```

The target must not exist. The source-mode generator auto-detects
this checkout and records it as a quoted local replacement; pass
`--kpro-replace <checkout>` when running the tool elsewhere. Current `main`
contains a source-incompatible dependency-boundary change planned for the next
minor release; its examples must not be mixed with the tagged `v0.1.0` module or
CLI.

The `service` profile creates the selected contracts, one generation manifest,
generated checked bindings, create-once implementations, a content-addressed
lock, a stable `cmd/<service>` entry point, and endpoint assembly under
`internal/kproapp`. Review the generated limits and transport configuration for
the workload, then add real health checks and follow the production checklist.
For an HTTP contract, generation also creates a typed client under
`internal/httpapi/kprohttpclient` that implements the same generated
`contract.API` interface as the server-side service.

Before editing application code, inspect the contract surface and selected
endpoint assembly:

```sh
kpro list api
kpro describe service
```

These commands describe version-controlled contracts, not conditional runtime
registrations. Their JSON form (`--format json`) is suitable for checked
developer tooling while the catalog remains `v1alpha1`.

To grow a current HTTP-only unified service after its client contract requires
gRPC:

```sh
kpro add --api grpc --dry-run
kpro add --api grpc
go mod tidy
go test ./...
kpro generate --check
```

The add command does not rewrite the process entry point or run dependency
tidying. It stages the service selection, generation manifest, protobuf
contract, generated endpoint/binding files, and lock, then installs them through
a checked compare-and-swap transaction with rollback. Repeating it is a no-op.
This path intentionally rejects legacy inline-main scaffolds instead of
attempting an ambiguous migration; recreate or migrate such a project
explicitly. See [Code Generation](code-generation.md#add-grpc-to-a-service)
for the exact project and tooling prerequisites, regeneration behavior,
supported contract subsets, Message bindings, and external-plugin trust
boundaries.

## 2. Start with the protocol your contract requires

| Need | Recommended starting point |
| --- | --- |
| Browser, REST-style, webhook, or ordinary request/response API | `httpx.New` mounted with `server.MountNative` |
| Measured HTTP/1.1 throughput or allocation pressure requiring a native fasthttp API | `fasthttpx.New` mounted with `server.MountNative` |
| Protobuf service contract and gRPC ecosystem interoperability | `grpcx.NewServer` mounted with `server.MountNative` |
| One-way server event stream to an HTTP client | `sse.New` mounted with `server.MountNative` |
| Persistent binary sessions, application message IDs, or custom framing | TCP, KCP, QUIC, UDP, or WebSocket message endpoints |
| HTTP semantics over QUIC | `http3.New` mounted with `server.MountNative` |

Do not add a second protocol only to make the deployment look more general. Add it when a client contract or measured workload requires it.

`fasthttpx` is intentionally not compatible with `net/http` and does not
support HTTP/2. Keep `httpx` when standard-library handlers, request-disconnect
cancellation, or HTTP/2 matter; see [Fast HTTP/1.1 Endpoint](fasthttp.md).

HTTP/3 early data is deliberately disabled by default. Set
`http3.Config.Allow0RTT` only when every accepted route is idempotent or the
application provides replay protection. Configure `MaxConnections`,
`MaxBodyBytes`, header limits, and timeouts explicitly rather than treating the
QUIC stream limit as a physical connection limit.

## 3. Assemble one application server

Use one checked `server.Server` as the process-level owner of transports, middleware, and managed resources:

```go
app, err := server.NewChecked(server.Config{
	Name:            "orders",
	ShutdownTimeout: 10 * time.Second,
})
if err != nil {
	return err
}

metrics, err := prometheusx.New(prometheusx.Config{
	Registerer: registry,
	Gatherer:   registry,
})
if err != nil {
	return err
}
app.Use(
	middleware.Recover(),
	otelx.Middleware(otelx.Config{TracerProvider: tracerProvider}),
	zapx.Middleware(logger),
	metrics.Middleware(),
	middleware.MaxPayload(1<<20),
)

httpAPI, err := server.MountNativeChecked(app, httpx.New(httpx.Config{
	Addr:            ":8080",
	ReadTimeout:     5 * time.Second,
	WriteTimeout:    10 * time.Second,
	IdleTimeout:     60 * time.Second,
	ShutdownTimeout: 10 * time.Second,
	MaxBodyBytes:    1 << 20,
}))
if err != nil {
	return err
}
registerOrderRoutes(httpAPI, orderService)
```

`NewChecked` rejects an invalid server config. On `Run`, KPro also
collects errors from every mounted component implementing
`transport.Validator` before it starts serving. Keep custom validation free of
side effects and return all independently useful problems with `errors.Join`.
`server.New` remains the compatibility constructor and applies the same runtime
defaults without the checked-construction error return.

Concrete protocols are constructed in their own packages and mounted through
the dependency-neutral `server` lifecycle. Importing `server` alone therefore
does not compile every built-in transport. Message transports use `Mount`;
native HTTP-style and RPC transports use the type-preserving
`server.MountNativeChecked` helper during checked assembly.

Here `logger`, the Prometheus `registry`, and `tracerProvider` are constructed
and owned by the application. Recovery is outermost; OTel installs the span
context before Zap reads trace IDs; Prometheus records the completed invocation;
payload limiting remains close to the handler. Choose the exact order from the
failure semantics you want and test panic/error paths.

Cross-protocol middleware should return an error carrying a `core.ErrorKind`
when clients need a stable outcome. Use `core.WithErrorKind(kind, err)` to
retain the original cause for logs and `errors.Is`/`errors.As`. HTTP and SSE
before stream start map rate limit, payload, and overload kinds to 429, 413, and
503; an active SSE stream sends a safe `error` event. gRPC maps those kinds to
`ResourceExhausted`. Unknown and recovered failures are emitted as a generic
internal error, never as raw error or panic text.

The `prometheusx` invocation middleware labels metrics only by transport,
stable operation, and result. Its lifecycle, Router, backlog, and projector
collectors take a complete set of names at construction and expose bounded
state/outcome labels. Do not turn raw URLs, peer addresses, connection IDs,
tenant IDs, event IDs, or arbitrary metadata into labels. `otelx` supplies
instrumentation only: the application selects and configures its SDK, exporter,
sampler, propagator, flush, and shutdown behavior. `zapx` uses the logger
supplied by the application.

Register process-wide middleware before routes so its order is visually clear. `Server.Use` also applies to compatible endpoints already mounted, but relying on a single assembly order is easier to review.

Server-level assembly freezes as startup begins. Prefer the checked
`UseChecked`, `MountChecked`, `MountRunnerChecked`, and `ManageChecked` forms
when assembly can race or be driven by extensions. All return
`server.ErrConfigurationLocked` after the lifecycle is claimed. Middleware and
mount operations can additionally return `server.ErrConfigurationBusy` for a
concurrent or reentrant mutation whose ordering would otherwise be ambiguous.
The fluent forms panic on their checked counterpart's error and are intended
for single-threaded startup composition.

Endpoint route and endpoint-local middleware APIs have their own concurrency
semantics and are not frozen by the Server lifecycle. Register them during
single-threaded assembly unless intentional runtime reconfiguration has been
designed and tested.

Expose `app.Name()` and `app.Descriptors()` in diagnostics when operators need
to verify the assembled process. Descriptor data reports protocol mode and
capabilities; it is not authorization or live protocol negotiation.

HTTP and HTTP/3 routes accept Go `ServeMux` path patterns. Use groups to keep
module assembly compact, `Context.Param` to read wildcards, and variadic route
middleware for policy that should not wrap the whole endpoint:

```go
api := endpoint.Group("/api", tenantMiddleware)
api.Get("/users/{id}", getUser, auditMiddleware)
```

Endpoint middleware runs first, then group middleware, then route middleware.
Use `HandleChecked` and `GroupChecked` when patterns or prefixes come from
configuration or plugins; fluent registration panics on invalid or conflicting
patterns so startup cannot silently publish an incomplete route table.

For a minimal application that does not need Prometheus, `middleware.Metrics`
remains an in-process JSON snapshot. The optional
[observability example](../examples/observability/main.go) shows Prometheus,
health, Zap, OTel wiring, and a separate admin listener.

## 4. Keep `main` as composition code

A conventional layout is:

```text
cmd/orders/main.go          signal handling and process assembly
internal/orders/service.go  business rules
internal/orders/http.go     HTTP handler registration
internal/orders/store.go    application-owned storage port
internal/platform/          concrete adapters and process utilities
```

Handlers should call module services rather than reach directly into unrelated transports or global database clients. This keeps protocol decisions at the edge and makes unit tests independent of a listener.

Message handlers should pass `core.ContextOf(ctx)` into module and dependency
calls. It reads middleware-provided tracing, deadlines, values, and session
cancellation through the optional `core.ContextProvider` capability.
`core.BaseContext` implements that capability; legacy custom `core.Context`
implementations fall back to `context.Background()`. A custom context that
needs request propagation should implement `core.ContextProvider`.

For relational source-of-truth data, prefer a module-owned storage interface backed by the GORM adapter. Put Redis, search, vector, document, or analytical stores behind module ports or projector/read-model boundaries. See [Database Architecture](database-architecture.md).

## 5. Make lifecycle and health ownership explicit

Open and validate required resources before accepting traffic. Run schema migrations as a separate deployment step when possible; if startup migrations are intentional, finish them before `Run`.

Resources that implement `Shutdown(context.Context)` can be registered with
`app.Manage(...)`. They are stopped in reverse registration order only after
the mounted runners complete their bounded stop sequence. If an adapter exposes
only `Close()`, use a small application-owned wrapper that adds a context-aware
shutdown contract.

For message transports, shutdown is a bounded stop, not a reply-delivery
barrier. Sessions and writer paths are closed, and outbound replies are
at-most-once; shutdown does not confirm that queued or in-flight replies reached
the peer.

Expose liveness and readiness separately. `healthx` executes named checks
concurrently under a bounded request context and returns HTTP 503 when a check
fails or exceeds the deadline. Every `CheckFunc` must pass that context to its
dependency and return when it is canceled; otherwise the response is bounded
but the check's goroutine may continue. Keep `ExposeErrors` disabled unless the
admin endpoint's access model makes diagnostic disclosure acceptable.

Pass a `signal.NotifyContext` to `Server.Start`, then call
`RunHandle.WaitReady` with a startup deadline before publishing application
readiness. Canceling the run context starts the same bounded stop sequence used
after a runner failure. `RunHandle.Ready()` can be selected on directly, while
`WaitReady` also returns a startup failure and the handle remains pinned to one
start generation. Custom transports should implement
`transport.ReadinessProvider` so readiness means the listener was bound;
legacy runners otherwise become ready when their serving method is entered.

Serving and shutdown errors retain their original causes and are wrapped in
`*server.ComponentError` with the component name and lifecycle phase (`serve`
or `shutdown`). Use `errors.As` for structured logging and `errors.Is` for the
underlying cause:

```go
var componentErr *server.ComponentError
if errors.As(err, &componentErr) {
	logger.Error("component failed",
		zap.String("component", componentErr.Component),
		zap.String("phase", componentErr.Phase),
		zap.Error(componentErr.Err),
	)
}
```

Call `Server.Shutdown(ctx)` explicitly only when an event other than run-context
cancellation initiates shutdown. The complete lifecycle pattern is in
[the quickstart](quickstart.md).

## 6. Choose message dispatch deliberately

For TCP, KCP, QUIC, UDP, and WebSocket routes, start with `HandleSync`. It preserves per-session execution order and is the safer default when operations mutate session or aggregate state.

For a contract-first message API, KPro can generate checked message
registration, JSON request/response types, and a create-once service
implementation from a `kpro.dev/message/v1alpha1` document. The generated
registration accepts a
small interface containing `HandleSyncChecked` and `HandleAsyncChecked`;
`*core.Router` and `*server.Endpoint` both implement it.

Prefer registering the service on the endpoint returned by `Mount`:

```go
endpoint := app.Mount(tcp.New(tcp.Config{
	Addr:  cfg.Addr,
	Codec: codec.KProV1(),
}))

if err := kpromessage.RegisterOrders(endpoint, service); err != nil {
	return err
}
```

Passing a directly owned `*core.Router` remains supported. The same generated
adapter type can be used with TCP, KCP, QUIC, UDP, and WebSocket message
endpoints, but each endpoint must be registered separately. It does not
generate HTTP, SSE, or gRPC bindings. See
[Code Generation](code-generation.md#kpro-message-backend).

Use `HandleAsync` only when all of the following are true:

- concurrent execution is safe;
- responses may complete out of order;
- shared state is synchronized;
- queue and in-flight limits have been sized and overload behavior has been tested.

Configure `core.RouterConfig` explicitly for sustained message workloads. The router has bounded queues and in-flight controls, but defaults are starting points rather than a capacity claim. See [Operations and Capacity Planning](operations.md).

## 7. Test from the inside out

Use this progression:

1. Unit-test module services with fake ports.
2. Test handlers and middleware without a real listener where the endpoint API permits it.
3. Run protocol integration tests on loopback with real framing and cancellation.
4. Run database integration tests against the exact engine versions used in deployment.
5. Load, soak, drain, and failure-test the built artifact in the target environment.

Repository tests demonstrate framework behavior. They do not establish your latency objective, storage durability, certificate rotation, deployment rollback, or maximum safe concurrency.

Repository CI also guards selected Router and HTTP hot-path benchmarks with
`cmd/benchguard` and [repository-owned budgets](../benchmark/budgets.json).
Those thresholds detect regressions on the CI workload; they are not service
capacity or latency objectives. Reproduce the CI command from
[Operations and Capacity Planning](operations.md), then add application-level
benchmarks and load tests.

CI additionally verifies module checksums, runs `govulncheck` on reachable
code, enforces a 55% aggregate coverage floor over framework packages, tests
the supported Go-version matrix, and compiles the generated scaffold on Linux
and Windows. These repository gates are a baseline; the application must scan
its complete dependency graph and built image and retain its own coverage and
test evidence.

Applications using code generation should commit the contract, manifest,
generated files, create-once scaffolds, and manifest-scoped lock together.
Run `kpro generate --check` in CI with the same pinned KPro CLI, protobuf tools,
and any explicitly authorized plugin executables used by developers.

## 8. Add production integrations at application boundaries

Authentication and authorization are intentionally outside this golden path.
Integrate them as middleware or in an upstream gateway and test fail-closed
behavior. OpenAPI generation rejects security requirements rather than
pretending to implement them; generated route registration is not an access
control layer. Prometheus exposition is opt-in, OTel export remains
application-configured, and every telemetry backend, retention policy, alert,
and access policy remains an operational integration.

Before deployment, complete the [Production Readiness Checklist](production-readiness.md), document the assumptions used for sizing, and record the load-test result that supports each operational limit.
