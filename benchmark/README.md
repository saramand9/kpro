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

Latest saved benchmark evidence: [RESULTS.md](RESULTS.md).

## CI hot-path budgets

CI runs deterministic in-process Router, HTTP handler, outbound-queue,
transport dispatch-gate, and codec benchmarks separately from localhost
protocol comparisons. The codec cases call
`Encode` and `DecodePacket` on in-memory 1 KiB frames; they do not use network
or disk I/O. Limits live in [budgets.json](budgets.json) and cover `ns/op`,
`B/op`, and `allocs/op`:

```sh
go test -p 1 ./core ./transport/httpx ./transport/internal/outbound ./transport/internal/dispatchgate ./benchmark -run '^$' \
  -bench 'Benchmark(RouterDispatchSync|EndpointServeHTTP|OutboundQueueRoundTrip|GateBeginEnd|KProCodec)$' \
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

