# KPro benchmarks

Run all protocol benchmarks:

```powershell
go test .\benchmark -bench . -benchmem
```

Run a quick smoke benchmark:

```powershell
go test .\benchmark -bench . -benchmem -benchtime=100x
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
BenchmarkFiberHTTP_Echo
BenchmarkFiberHTTP_Echo_Concurrent
BenchmarkSSE_Events
```

`BenchmarkHTTP_Echo` and `BenchmarkHTTPS_Echo` use KPro `httpx`, which is backed by fasthttp.
Concurrent HTTP benchmarks run the same `POST /echo` workload with 16 and 64 workers.

KCP is not benchmarked because `transport/kcp` is not implemented.
