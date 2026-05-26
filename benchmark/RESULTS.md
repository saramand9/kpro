# KPro benchmark results

## 2026-05-26 smoke

Environment:

```text
goos: windows
goarch: amd64
pkg: github.com/saramand9/bignet/kpro/benchmark
cpu: Intel(R) Core(TM) i5-14400
```

Command:

```powershell
go test .\benchmark -bench . -benchmem -benchtime=100x
```

Result:

```text
BenchmarkTCP_Echo/64B-16      	     100	     52990 ns/op	   1.21 MB/s	     18871 qps	    2121 B/op	       8 allocs/op
BenchmarkTCP_Echo/1024B-16    	     100	     48006 ns/op	  21.33 MB/s	     20831 qps	    6139 B/op	       8 allocs/op
BenchmarkUDP_Echo/64B-16      	     100	     80822 ns/op	   0.79 MB/s	     12373 qps	    2071 B/op	      17 allocs/op
BenchmarkUDP_Echo/1024B-16    	     100	     80543 ns/op	  12.71 MB/s	     12416 qps	    7158 B/op	      17 allocs/op
BenchmarkWebSocket_Echo/64B-16         	     100	     96937 ns/op	   0.66 MB/s	     10316 qps	    2191 B/op	      22 allocs/op
BenchmarkWebSocket_Echo/1024B-16       	     100	    115988 ns/op	   8.83 MB/s	      8622 qps	    9399 B/op	      24 allocs/op
BenchmarkHTTP_Echo/64B-16              	     100	     90065 ns/op	   0.71 MB/s	     11103 qps	   10248 B/op	      92 allocs/op
BenchmarkHTTP_Echo/1024B-16            	     100	    121075 ns/op	   8.46 MB/s	      8259 qps	   16137 B/op	      97 allocs/op
BenchmarkHTTPS_Echo/64B-16             	     100	     91305 ns/op	   0.70 MB/s	     10952 qps	   11299 B/op	     105 allocs/op
BenchmarkHTTPS_Echo/1024B-16           	     100	    104710 ns/op	   9.78 MB/s	      9550 qps	   17086 B/op	     110 allocs/op
BenchmarkSSE_Events/64B-16             	     100	     27292 ns/op	   2.35 MB/s	     36641 qps	     237 B/op	       5 allocs/op
BenchmarkSSE_Events/1024B-16           	     100	     30062 ns/op	  34.06 MB/s	     33265 qps	    2297 B/op	       6 allocs/op
```

Notes:

- This is a short smoke benchmark for result visibility and regression checks.
- Use `-benchtime=5s -count=3` for a more stable production performance report.
