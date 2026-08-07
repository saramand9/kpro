# KPro benchmarks

Run all protocol benchmarks:

```powershell
go test .\benchmark -bench . -benchmem
```

Run a quick smoke benchmark:

```powershell
go test .\benchmark -bench . -benchmem -benchtime=100x
```

Run external comparison benchmarks:

```powershell
cd .\benchmark\compare
go test . -bench . -benchmem
```

The benchmark output includes:

```text
ns/op
MB/s
B/op
allocs/op
qps
```

Latest saved benchmark evidence: [RESULTS.md](RESULTS.md). The expanded
2026-08-06 KPro/Netty TCP diagnostic preserves
[180 raw process samples](results/2026-08-06-kpro-netty-tcp-diagnostic-raw.csv),
[60 same-cycle comparisons](results/2026-08-06-kpro-netty-tcp-diagnostic-pairs.csv),
and a [10-case summary](results/2026-08-06-kpro-netty-tcp-diagnostic-summary.csv).
The earlier
[24-process diagnostic](results/2026-08-05-kpro-netty-loopback.csv) remains as
historical evidence. Both use the checked-in
[Netty NIO echo fixture](fixtures/netty/NettyKProEchoServer.java).

## TCP comparison load generator

`benchmark/cmd/tcp-loadgen` is an independent, raw TCP client for repeatable
KProV1 echo comparisons. Every request payload varies with its sequence. A
response is counted only after its magic, version, flags, message ID, sequence,
payload length, and every payload byte match. It is a local measurement tool,
not a production capacity test or load-testing service.

Build it or run it directly from the repository root:

```sh
go build -o tcp-loadgen ./benchmark/cmd/tcp-loadgen
go run ./benchmark/cmd/tcp-loadgen \
  -addr 127.0.0.1:18088 \
  -connections 64 \
  -payload 1024 \
  -inflight 16 \
  -coalesce 4 \
  -duration 30s \
  -max-samples 200000
```

`-inflight` is the number of requests each connection sends before it starts
reading that group of responses. `-coalesce` is the maximum number of
consecutive frames combined into one client write and is capped at
`-inflight`. For example, `-inflight 16 -coalesce 4` performs four writes of
four frames and then reads sixteen responses in order. `-slow-percent` marks a
deterministic percentage of connections as write-only slow consumers, rounded
up to a whole connection.

Connections are established and worker buffers are prepared before one shared
absolute measurement window starts. Every socket uses that same deadline.
Throughput is divided by the configured window rather than by per-worker
startup time or a slow connection's final timeout.

The output fields are:

| Field | Meaning |
| --- | --- |
| `connections`, `healthy_connections`, `slow_connections` | Established client connections by role. |
| `payload` | Payload bytes per KProV1 frame. |
| `duration_s`, `wall_s` | Fixed measurement window and observed process-side wall time. |
| `healthy_sent`, `slow_sent` | Complete request frames written by each connection role. |
| `completed` | Healthy responses that passed full-frame and full-payload validation. |
| `incomplete` | Fully written healthy requests without a validated response inside the window. |
| `validation_errors` | Header, sequence, length, flags, or payload mismatches. |
| `io_errors`, `healthy_io_errors`, `slow_io_errors` | Non-deadline read/write failures. |
| `timeouts`, `healthy_timeouts`, `slow_timeouts` | Workers that reached the common window boundary in an operation or loop. |
| `bytes` | All request bytes accepted by client writes, including a partial final write. |
| `throughput_msg_s` | Fully validated completions divided by the fixed measurement duration. |
| `p50_us`, `p99_us`, `p999_us` | Percentiles from the bounded successful-request latency reservoir. |
| `samples` | Actual reservoir size. `-max-samples` is divided across healthy connections. |

The command exits non-zero when a healthy workload completes no responses or
observes validation or non-deadline healthy I/O errors. Window-boundary
timeouts and an incomplete final in-flight group remain explicit counters
rather than being silently suppressed.

Same-machine loopback results include the client process, OS scheduler, local
TCP stack, runtime, and server on the same CPUs. They do not establish remote
network throughput, maximum connection capacity, or production tail latency.
The generator is closed-loop: a connection waits for its current in-flight
group before sending the next group. Latencies use an Algorithm R reservoir
over successful responses across the whole window, with a bounded share for
each healthy connection. Failed and incomplete requests are necessarily
censored from those percentiles, and closed-loop traffic still has coordinated
omission. Interpret the percentile fields together with `incomplete` and the
error counters as comparison diagnostics, not SLA evidence.

For implementation comparisons, warm up both servers and run multiple rounds
in `A-B-B-A` order with the same load-generator binary, flags, ports, machine,
and background-load conditions. Compare medians and spread together with error
counts and tail latency; do not use a single best run as the result.

## CI hot-path budgets

CI runs deterministic in-process Router, HTTP handler, outbound-queue,
transport dispatch-gate, codec, TCP send-admission, and TCP/WebSocket
batch-loop benchmarks separately from localhost protocol comparisons. The
codec and transport fast-path cases use in-memory 1 KiB frames, drained queues,
and discard connections; they do not use network or disk I/O. Limits live in
[budgets.json](budgets.json) and cover `ns/op`, `B/op`, and `allocs/op`:

```sh
go test -p 1 ./core ./transport/httpx ./transport/internal/outbound ./transport/internal/dispatchgate ./transport/tcp ./transport/websocket ./benchmark -run '^$' \
  -bench 'Benchmark(RouterDispatchSync|EndpointServeHTTP|OutboundQueueRoundTrip|OutboundQueueDrainAvailable|GateBeginEnd|KProCodec|SessionSendContiguous|SessionSendSegmented|ReserveOutboundBufferCommit|TCPSessionBatchWrite16x1KiB|WebSocketSessionBatchWrite16x1KiB)$' \
  -benchmem -benchtime=250ms -count=3 > benchmark-current.txt
go run ./cmd/benchguard \
  -budget benchmark/budgets.json \
  -input benchmark-current.txt
```

Latency ceilings are deliberately wider than local results because shared CI
hardware varies. Allocation and byte budgets are tighter and should only be
raised with a documented reason. The workflow uploads the raw benchmark output
so a failed or changed run remains inspectable.

`benchguard` keeps every repeated sample. It compares the median `ns/op` with
the latency ceiling, while every `B/op` and `allocs/op` sample must remain
within budget. Required benchmarks and required metrics must be present. This
keeps timing outliers from dominating CI without allowing a memory regression
to be hidden by aggregation. Explicit zero limits enforce zero allocation;
omitting a metric leaves it unguarded. The repository budget also requires at
least three samples per benchmark, so a weakened `-count` cannot silently pass.
Version 1 budget files without that field remain valid and require one sample.

After the Router invocation allocation optimization, all three guarded Router
cases measure `24 B/op` and `1 alloc/op` on the saved local baseline. Their
budgets are capped at `64 B/op` and `1 alloc/op`; the wider byte ceiling allows
normal toolchain accounting variation without permitting another allocation.

Use repeated runs and `benchstat` when evaluating an optimization; passing the
budget is a regression guard, not evidence that a change is faster.

`BenchmarkSessionSendContiguous` preserves an in-process baseline for the
legacy contiguous codec/queue shape. `BenchmarkSessionSendSegmented` locks the
default KProV1 TCP compatibility path to zero heap allocations after pool
warm-up. The normal `Session.Send` contract still copies caller-owned payload
bytes before returning.
`BenchmarkReserveOutboundBufferCommit` covers the explicit ownership-transfer
path. In the warmed 1 KiB case, its one allocation is the public lease handle;
the transport does not allocate a new payload backing slice per operation.
The TCP and WebSocket 16-frame batch benchmarks guard the writer construction
and completion loops at zero allocations using an in-memory discard
connection; they do not measure syscalls or network throughput.

Current coverage:

```text
BenchmarkTCP_Echo
BenchmarkKCP_Echo
BenchmarkQUIC_Echo
BenchmarkUDP_Echo
BenchmarkWebSocket_Echo
BenchmarkHTTP_Echo
BenchmarkHTTP_Echo_Concurrent
BenchmarkHTTPS_Echo
BenchmarkSSE_Events
```

`BenchmarkHTTP_Echo` and `BenchmarkHTTPS_Echo` use KPro `httpx`, which is backed by net/http.
Concurrent HTTP benchmarks run the same `POST /echo` workload with 16 and 64 workers.
Fiber and gnet comparison benchmarks live in `benchmark/compare` so their dependencies do not enter the main kpro module.
That module includes KPro-vs-Fiber HTTP echo and KPro-vs-gnet TCP echo comparisons.

## External loopback diagnostics

The commands below are correctness-validating local load generators. They are
useful for repeatable implementation diagnostics but do not replace a separate
load-generator host or a production workload test:

```sh
go run ./benchmark/cmd/websocket-echo-server -addr 127.0.0.1:18401
go run ./benchmark/cmd/websocket-loadgen -addr 127.0.0.1:18401 -connections 64 -payload 1024 -inflight 16 -coalesce 16 -duration 8s

go run ./benchmark/cmd/sse-push-server -addr 127.0.0.1:18501 -yield-every 0
go run ./benchmark/cmd/sse-loadgen -addr 127.0.0.1:18501 -streams 64 -payload 1024 -duration 8s
```

The SSE producer accepts `-yield-every 32` for the cooperative fairness shape
used by the 2026-08-07 comparison. Zero retains the maximum-throughput loop.
SSE has no request inflight or request/response latency metric; the load
generator reports aggregate events, payload throughput, inter-event gaps, and
the slowest stream's event count relative to the per-stream mean.

The matching Java fixtures are
`benchmark/fixtures/netty/NettyWebSocketKProEchoServer.java` and
`benchmark/fixtures/netty/NettySSEServer.java`. They compile against a reviewed
Netty source build and intentionally keep Netty dependencies out of the Go
module. Saved raw and summarized evidence lives under `benchmark/results/`;
the interpretation and limitations are recorded in
`docs/netty-performance-baseline.md`.
