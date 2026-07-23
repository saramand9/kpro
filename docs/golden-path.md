# Recommended Golden Path

This is the default application shape for teams adopting KPro. It favors the smallest public API surface, explicit limits, and an easy path from a local HTTP service to a multi-transport application.

KPro provides the assembly and transport primitives described here. It does not certify an application for production; deployment-specific security, sizing, observability, and failure testing remain the application's responsibility.

## 1. Create a reviewable starting point

Run the checked-in [quickstart](quickstart.md) when evaluating KPro. To start a
separate application from this checkout:

```sh
go run ./cmd/kpro new ../orders --module example.com/orders --name orders
```

The target must be missing or empty. The generator auto-detects this checkout
and records it as a quoted local replacement; pass `--kpro-replace <checkout>`
when running the tool elsewhere. The generated service already includes
checked construction and explicit example HTTP limits. Review those values for
the workload, then add real health checks and follow the production checklist.
Until KPro has release tags, use the local replacement while developing and a
reviewed commit for shared builds.

## 2. Start with the protocol your contract requires

| Need | Recommended starting point |
| --- | --- |
| Browser, REST-style, webhook, or ordinary request/response API | HTTP/HTTPS with `server.HTTP` or `server.HTTPS` |
| Protobuf service contract and gRPC ecosystem interoperability | `server.GRPC` |
| One-way server event stream to an HTTP client | `server.SSE` |
| Persistent binary sessions, application message IDs, or custom framing | TCP, KCP, QUIC, UDP, or WebSocket message endpoints |
| HTTP semantics over QUIC | `server.HTTP3` |

Do not add a second protocol only to make the deployment look more general. Add it when a client contract or measured workload requires it.

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

httpAPI := app.HTTP(":8080", httpx.Config{
	ReadTimeout:     5 * time.Second,
	WriteTimeout:    10 * time.Second,
	IdleTimeout:     60 * time.Second,
	ShutdownTimeout: 10 * time.Second,
	MaxBodyBytes:    1 << 20,
})
registerOrderRoutes(httpAPI, orderService)
```

`NewChecked` rejects an invalid server config. On `ListenAndServe`, KPro also
collects errors from every mounted component implementing
`transport.Validator` before it starts serving. Keep custom validation free of
side effects and return all independently useful problems with `errors.Join`.
`server.New` remains the compatibility constructor and applies the same runtime
defaults without the checked-construction error return.

Here `logger`, the Prometheus `registry`, and `tracerProvider` are constructed
and owned by the application. Recovery is outermost; OTel installs the span
context before Zap reads trace IDs; Prometheus records the completed invocation;
payload limiting remains close to the handler. Choose the exact order from the
failure semantics you want and test panic/error paths.

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

The HTTP router currently matches an exact method and path. It does not provide path parameters, route groups, or route-local middleware. Keep module registration functions small, or place a third-party HTTP router behind a custom native runner when those features are required.

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

For relational source-of-truth data, prefer a module-owned storage interface backed by the GORM adapter. Put Redis, search, vector, document, or analytical stores behind module ports or projector/read-model boundaries. See [Database Architecture](database-architecture.md).

## 5. Make lifecycle and health ownership explicit

Open and validate required resources before accepting traffic. Run schema migrations as a separate deployment step when possible; if startup migrations are intentional, finish them before `ListenAndServe`.

Resources that implement `Shutdown(context.Context)` can be registered with `app.Manage(...)`. They are stopped after protocol runners drain, in reverse registration order. If an adapter exposes only `Close()`, use a small application-owned wrapper that adds a context-aware shutdown contract.

Expose liveness and readiness separately. `healthx` executes named checks
concurrently under a bounded request context and returns HTTP 503 when a check
fails or exceeds the deadline. Every `CheckFunc` must pass that context to its
dependency and return when it is canceled; otherwise the response is bounded
but the check's goroutine may continue. Keep `ExposeErrors` disabled unless the
admin endpoint's access model makes diagnostic disclosure acceptable.

Handle `SIGINT` and `SIGTERM`, give `Server.Shutdown` a deadline, and report both serving and shutdown errors. The complete pattern is in [the quickstart](quickstart.md).

## 6. Choose message dispatch deliberately

For TCP, KCP, QUIC, UDP, and WebSocket routes, start with `HandleSync`. It preserves per-session execution order and is the safer default when operations mutate session or aggregate state.

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

## 8. Add production integrations at application boundaries

Authentication and authorization are intentionally outside this golden path. Integrate them as middleware or in an upstream gateway and test fail-closed behavior. Prometheus exposition is opt-in, OTel export remains application-configured, and every telemetry backend, retention policy, alert, and access policy remains an operational integration.

Before deployment, complete the [Production Readiness Checklist](production-readiness.md), document the assumptions used for sizing, and record the load-test result that supports each operational limit.
