# 10-minute Quick Start

This guide starts a small HTTP service using the recommended KPro application
lifecycle. It includes global middleware, bounded request bodies, explicit HTTP
timeouts, health and metrics routes, and signal-driven graceful shutdown.

The example intentionally does not include authentication or authorization.
Those concerns can be integrated at an application or infrastructure boundary
without changing this startup shape.

## 1. Prerequisites

- Go 1.21 or newer.
- A checkout of this repository.
- Port `8080` available, or another port supplied with `-addr`.

The repository currently has no release tags. Run this example from the
checkout; external consumers should pin a reviewed commit until a versioned
release exists. See [API stability](api-stability.md).

## 2. Start the service

From the repository root:

```sh
go run ./examples/quickstart -addr :8080
```

The process logs:

```text
quickstart starting on :8080
```

The complete source is [examples/quickstart/main.go](../examples/quickstart/main.go).

To scaffold a separate service instead, run this from the KPro checkout. The
target must be missing or empty; the command refuses to overwrite a non-empty
directory:

```sh
go run ./cmd/kpro new ../hello-kpro --module example.com/hello-kpro --name hello-kpro
cd ../hello-kpro
go mod tidy
go run .
```

The tool detects the current KPro checkout and writes a quoted local `replace`
into the generated `go.mod`. When invoking an installed tool from elsewhere,
pass `--kpro-replace <checkout>`. The current Git remote, module import path,
and lack of release tags do not form an external `go get` installation
contract. For a shared build, use an organization-approved checkout, vendoring
workflow, or module proxy and pin the reviewed commit. The generated service
includes checked construction, explicit HTTP timeouts/body limits, Zap logging,
recovery middleware, two starter routes, process signals, and graceful
shutdown. Review those example limits against the workload, then add real
readiness checks, metrics/tracing, and dependency ownership before deployment.

## 3. Call the routes

With `curl`:

```sh
curl http://127.0.0.1:8080/health
curl -X POST http://127.0.0.1:8080/echo \
  -H "Content-Type: text/plain" \
  --data "hello kpro"
curl http://127.0.0.1:8080/metrics
```

With PowerShell:

```powershell
Invoke-RestMethod http://127.0.0.1:8080/health
Invoke-RestMethod http://127.0.0.1:8080/echo `
  -Method Post -ContentType text/plain -Body 'hello kpro'
Invoke-RestMethod http://127.0.0.1:8080/metrics
```

`POST /echo` returns:

```json
{"echo":"hello kpro"}
```

The metrics endpoint exposes the built-in in-process `middleware.Metrics`
snapshot. It is not a Prometheus exporter or a replacement for an external
observability backend.

## 4. Stop gracefully

Press `Ctrl+C`. The example catches the process signal, gives the server five
seconds to drain, stops mounted runners, and then lets managed resources close.

The relevant lifecycle is:

```go
serveErr := make(chan error, 1)
go func() {
	serveErr <- app.ListenAndServe()
}()

<-signalCtx.Done()
shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
defer cancel()
_ = app.Shutdown(shutdownCtx)
```

Keep the shutdown deadline longer than the longest request you intend to drain,
but bounded so a deployment cannot wait forever.

## 5. Understand the setup

The example follows four steps that should remain visible in a larger service:

1. Create one checked `server.Server` and set its automatic shutdown timeout.
2. Register global middleware before mounting endpoints.
3. Mount an HTTP endpoint with explicit limits and timeouts.
4. Start once and stop through `Server.Shutdown` with a deadline.

New applications should surface configuration errors before serving:

```go
app, err := server.NewChecked(server.Config{
	Name:            "quickstart",
	ShutdownTimeout: 5 * time.Second,
})
if err != nil {
	return err
}
```

`server.New` remains available and applies compatibility defaults for existing
callers. `NewChecked` validates the server config; when `ListenAndServe` starts,
the server also aggregates `Validate` failures from mounted components that
implement `transport.Validator` before launching any serving goroutine.

Middleware registration order is outermost to innermost:

```go
app.Use(
	middleware.Recover(),
	metrics.Middleware(),
	middleware.MaxPayload(1<<20),
)
```

Here recovery wraps metrics, and metrics records payload-limit failures. HTTP
handlers continue to use the native `*httpx.Context`; KPro does not force HTTP
requests into `core.Message`.

The HTTP configuration is explicit because the built-in read, write, and idle
timeouts default to zero:

```go
app.HTTP(":8080", httpx.Config{
	ReadTimeout:     5 * time.Second,
	WriteTimeout:    10 * time.Second,
	IdleTimeout:     60 * time.Second,
	ShutdownTimeout: 5 * time.Second,
	MaxBodyBytes:    1 << 20,
})
```

These values are examples, not universal production recommendations. Select
them from measured request sizes, handler latency, client behavior, and the
deployment shutdown budget.

## 6. Add operational integrations

The minimal example deliberately keeps its JSON `middleware.Metrics` endpoint.
For the optional production-facing integrations, run:

```sh
go run ./examples/observability -api-addr :8080 -admin-addr :9090
curl http://127.0.0.1:8080/hello
curl http://127.0.0.1:9090/livez
curl http://127.0.0.1:9090/readyz
curl http://127.0.0.1:9090/metrics
```

[The observability example](../examples/observability/main.go) demonstrates:

- `healthx` liveness and readiness handlers on a separate admin listener;
- `prometheusx` invocation collectors with only `transport`, stable
  `operation`, and `result` labels, plus a bounded lifecycle collector;
- `zapx` completion, failure, panic, and slow-invocation logging;
- `otelx` middleware placed before logging so valid trace/span IDs can flow to
  logs;
- `transport.Describe`, a capability descriptor, and automatic startup
  validation for a custom native runner.

The example intentionally does not install an OpenTelemetry SDK exporter.
`otelx` uses the supplied or global `TracerProvider` and propagator; the
application must select, configure, flush, and shut down its SDK, sampler, and
exporter. A health check receives a deadline-bearing `context.Context` and must
stop its own I/O or waiting when that context is canceled—`healthx` cannot force
a check that ignores cancellation to terminate. The admin listener is for local
demonstration; its network exposure, TLS, and access policy remain deployment
decisions.

## 7. Move to an application structure

After the first route works, move route assembly out of `main`:

```text
cmd/service/main.go             flags, process signals, startup
internal/app/routes.go          endpoint assembly
internal/modules/<name>/        handler, service, ports, stores
```

Use [the golden path](golden-path.md) for protocol and persistence choices.
The modular examples under [examples/](../examples/) show larger route layouts.

## What this proves—and what it does not

This quick start exercises code that exists in this repository:

- native HTTP method/path routing;
- unified middleware;
- request body limits;
- in-process metrics;
- checked configuration and component startup validation;
- server shutdown with a deadline.

It does not establish production capacity, availability, TLS posture, external
database behavior, multi-host failover, or a security model. Complete the
[production readiness checklist](production-readiness.md) and run
workload-specific tests before deployment.
