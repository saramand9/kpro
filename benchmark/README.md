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

CI runs deterministic in-process Router and HTTP handler benchmarks separately
from localhost protocol comparisons. Limits live in [budgets.json](budgets.json)
and cover `ns/op`, `B/op`, and `allocs/op`:

```sh
go test ./core ./transport/httpx -run '^$' \
  -bench 'Benchmark(RouterDispatchSync|EndpointServeHTTP)$' \
  -benchmem -benchtime=250ms -count=1 > benchmark-current.txt
go run ./cmd/benchguard \
  -budget benchmark/budgets.json \
  -input benchmark-current.txt
```

Latency ceilings are deliberately wider than local results because shared CI
hardware varies. Allocation and byte budgets are tighter and should only be
raised with a documented reason. The workflow uploads the raw benchmark output
so a failed or changed run remains inspectable.

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

