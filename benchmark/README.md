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
BenchmarkHTTPS_Echo
BenchmarkSSE_Events
```

KCP is not benchmarked because `transport/kcp` is not implemented.
