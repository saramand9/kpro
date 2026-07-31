# 10-minute Quick Start

This guide starts a small HTTP service using the recommended KPro application
lifecycle. It includes global middleware, bounded request bodies, explicit HTTP
timeouts, health and metrics routes, and signal-driven graceful shutdown.

The example intentionally does not include authentication or authorization.
Those concerns can be integrated at an application or infrastructure boundary
without changing this startup shape.

## 1. Prerequisites

- Go 1.25 or newer. Repository CI verifies Go 1.25.12 and 1.26.5.
- A checkout of this repository when running the example directly or testing
  unreleased source.
- Port `8080` available, or another port supplied with `-addr`.

`v0.1.0` is the initial pre-1.0 developer release. This guide tracks current
`main`, whose dependency-neutral mounting API is planned for the next minor
release. Pin one revision and use the documentation from the same revision;
review the [API stability policy](api-stability.md) before upgrading.

## 2. Start the service

From the repository root:

```sh
go run ./examples/quickstart -addr :8080
```

The process logs:

```text
quickstart starting on :8080
quickstart ready on :8080
```

The complete source is [examples/quickstart/main.go](../examples/quickstart/main.go).

To scaffold a separate contract-first HTTP service, run the tool from the same
KPro checkout:

```sh
go run ./cmd/kpro doctor
go run ./cmd/kpro version
go run ./cmd/kpro new ../hello-kpro \
  --module example.com/hello-kpro \
  --name hello-kpro \
  --profile service \
  --api http
cd ../hello-kpro
go mod tidy
go run ./cmd/hello-kpro
```

Every CLI subcommand accepts `--help`; for example,
`go run ./cmd/kpro new --help` prints usage and exits successfully.

The unified `service` profile accepts repeatable `--api http|grpc|message`
selections. A Message API also requires at least one explicit, repeatable
`--transport tcp|kcp|quic|udp|websocket`. For example:

```sh
# Message API over TCP.
go run ./cmd/kpro new ../session-service \
  --module example.com/session-service \
  --name session-service \
  --profile service \
  --api message \
  --transport tcp

# HTTP, gRPC, and Message APIs assembled into one process.
go run ./cmd/kpro new ../multi-service \
  --module example.com/multi-service \
  --name multi-service \
  --profile service \
  --api http \
  --api grpc \
  --api message \
  --transport tcp \
  --transport websocket
```

An HTTP-only unified service can add gRPC later:

```sh
go run ./cmd/kpro add --root ../hello-kpro --api grpc --dry-run
go run ./cmd/kpro add --root ../hello-kpro --api grpc
cd ../hello-kpro
go mod tidy
go test ./...
kpro generate --check
```

This command currently adds only gRPC and accepts only projects created by the
current unified `service` profile. It does not migrate the removed
protocol-specific profile or an older project whose endpoint setup is embedded
directly in `cmd/<service>/main.go`. The stable main file is not rewritten, and
the command deliberately leaves dependency selection to `go mod tidy`.
Re-running the same add against a current project is a byte-preserving no-op.

Selecting gRPC requires `protoc`, `protoc-gen-go`, and
`protoc-gen-go-grpc` on `PATH`. Run `kpro doctor` before scaffolding and pin all
three versions in development and CI. Selecting QUIC produces configuration
that reads `KPRO_QUIC_CERT_FILE` and `KPRO_QUIC_KEY_FILE`; supply reviewed TLS
material before starting that project. The scaffold intentionally does not
create or commit a private key.

The old `http-api` profile has been removed. Use
`--profile service --api http`. The hand-written `minimal` profile remains
available when contract generation is deliberately not wanted:

```sh
go run ./cmd/kpro new ../hello-minimal \
  --module example.com/hello-minimal \
  --name hello-minimal \
  --profile minimal
```

See [Code Generation](code-generation.md) for the generated project layout,
manifest and ownership rules, supported contract subsets, plugin trust
boundary, and the `validate`/`generate --check` workflow.

The tagged `v0.1.0` CLI generates a service for the `v0.1.0` API and remains
usable with the documentation at that tag. It predates `server.MountNative`;
do not combine that binary with current `main` examples. Private-module use also
requires the organization's normal Git credentials and `GOPRIVATE`
configuration. The target must be missing or empty; the command refuses to
overwrite a non-empty directory. Service generation is staged and published
only after all selected bindings are generated successfully.

`doctor` prints the Go runtime, platform, main module, available VCS revision,
and the availability of the three protobuf generation tools. Missing protobuf
tools remain optional for the general diagnostic and for HTTP or Message
projects, but `--profile service --api grpc` requires them before creating the
project. `version` prints build metadata; a checkout run reports development
values, while release artifacts receive version, commit, and build time from
the release pipeline.

The source-mode tool detects the current KPro checkout and writes a quoted local
`replace` into the generated `go.mod`; `--kpro-replace <checkout>` selects one
explicitly. The generated HTTP service includes checked construction, explicit
request timeouts and body limits, recovery and payload-limit middleware,
liveness and readiness routes, process signals, and graceful shutdown. The
starter has no external dependency, so readiness succeeds while the process is
serving. Add dependency checks, metrics, tracing, authentication, and managed
resource ownership when the service begins using them.

The local replacement is a source-development convenience in the generated
application; the KPro root module itself does not use a repository-local
replacement. Before distributing the generated module, remove the local
`replace` and pin a KPro tag that is reachable through its declared module
path. A versioned CLI install uses:

```sh
go install github.com/saramand9/kpro/cmd/kpro@vX.Y.Z
```

That command is only expected to work after the selected tag and declared
module path are reachable. Private-source use requires both working VCS
credentials and appropriate `GOPRIVATE` configuration; `GOPRIVATE` is not an
authentication mechanism. Use a reviewed checkout or verified release archive
when the versioned module path is not available.

## 3. Call the routes

For `examples/quickstart`, which was started at the beginning of this guide,
use:

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

If you instead started the generated `hello-kpro` service, call `/livez`,
`/readyz`, and `/healthz`. The starter returns `{"status":"alive"}` from
`/livez` and a ready status with its placeholder dependency summary from the
other two routes.

## 4. Stop gracefully

Press `Ctrl+C`. Use the signal context as the lifetime passed to `Server.Start`.
Cancellation starts the configured bounded stop. In this HTTP-only example,
`net/http` attempts to drain accepted requests within the shutdown deadline;
managed resources close only if runner stop succeeds. Message transports have
at-most-once shutdown semantics and do not guarantee reply drain.

The relevant lifecycle is:

```go
baseCtx, cancelRun := context.WithCancel(context.Background())
defer cancelRun()
runCtx, stopSignals := signal.NotifyContext(
	baseCtx,
	os.Interrupt,
	syscall.SIGTERM,
)
defer stopSignals()

handle, err := app.Start(runCtx)
if err != nil {
	return err
}

readyCtx, cancelReady := context.WithTimeout(context.Background(), 15*time.Second)
if err := handle.WaitReady(readyCtx); err != nil {
	cancelReady()
	cancelRun()
	return errors.Join(err, handle.Wait())
}
cancelReady()

if err := handle.Wait(); err != nil &&
	!(server.IsContextTermination(err) && runCtx.Err() != nil) {
	return err
}
```

`RunHandle.WaitReady` returns only after every readiness-aware runner has published a
bound listener, or returns the startup error. Legacy custom runners without
`transport.ReadinessProvider` are considered ready when their
`ListenAndServe` method is entered. `RunHandle.Ready()` is available for select
loops and is pinned to one start generation; use `WaitReady` when startup
failure must be distinguished from readiness. Keep
`server.Config.ShutdownTimeout` longer than the longest request you intend to
drain, but bounded so a deployment cannot wait forever. An explicit
`Server.Shutdown(ctx)` remains available when shutdown is initiated by something
other than cancellation of the run context.

## 5. Understand the setup

The example follows four steps that should remain visible in a larger service:

1. Create one checked `server.Server` and set its automatic shutdown timeout.
2. Register global middleware before mounting endpoints.
3. Mount an HTTP endpoint with explicit limits and timeouts.
4. Start once with `Server.Start`, wait for `RunHandle.WaitReady`, and stop by
   canceling the run context or by calling `Server.Shutdown` with a deadline.

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
callers. `NewChecked` validates the server config; when `Run` starts, the
server also aggregates `Validate` failures from mounted components that
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

The HTTP configuration is explicit even though zero values select bounded
built-in defaults: 5 seconds for request headers, 30 seconds for reads and
writes, 60 seconds for idle connections, 5 seconds for shutdown, 1 MiB for
headers, and 4 MiB for request bodies. Explicit values make the service contract
reviewable:

```go
httpEndpoint := server.MountNative(app, httpx.New(httpx.Config{
	Addr:            ":8080",
	ReadTimeout:     5 * time.Second,
	WriteTimeout:    10 * time.Second,
	IdleTimeout:     60 * time.Second,
	ShutdownTimeout: 5 * time.Second,
	MaxBodyBytes:    1 << 20,
}))
```

These values are examples, not universal production recommendations. Select
them from measured request sizes, handler latency, client behavior, and the
deployment shutdown budget.

Handler and middleware errors are classified at the protocol boundary. HTTP
maps rate limiting to 429, payload limits to 413, bounded-capacity overload to
503, and unknown or recovered failures to 500. Response bodies use stable safe
messages rather than returning the original error text. Application
middleware can preserve its internal cause while selecting a public category:

```go
return core.WithErrorKind(core.ErrorKindOverloaded, err)
```

Log or trace the original error inside the application. Do not use the safe
wire message as the only diagnostic record.

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
- listener readiness through `RunHandle.WaitReady`;
- stable HTTP error classification without exposing internal error text;
- server shutdown with a deadline.

It does not establish production capacity, availability, TLS posture, external
database behavior, multi-host failover, or a security model. Complete the
[production readiness checklist](production-readiness.md) and run
workload-specific tests before deployment.
