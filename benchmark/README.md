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

Latest saved smoke result: [RESULTS.md](RESULTS.md).

Current coverage:

```text
BenchmarkTCP_Echo
BenchmarkUDP_Echo
BenchmarkWebSocket_Echo
BenchmarkHTTP_Echo
BenchmarkHTTP_Echo_Concurrent
BenchmarkHTTPS_Echo
BenchmarkSSE_Events
```

`BenchmarkHTTP_Echo` and `BenchmarkHTTPS_Echo` use KPro `httpx`, which is backed by fasthttp.
Concurrent HTTP benchmarks run the same `POST /echo` workload with 16 and 64 workers.
Fiber and gnet comparison benchmarks live in `benchmark/compare` so their dependencies do not enter the main kpro module.
That module includes KPro-vs-Fiber HTTP echo and KPro-vs-gnet TCP echo comparisons.

KCP is not benchmarked because `transport/kcp` is not implemented.
