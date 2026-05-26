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

## 2026-05-26 Fiber HTTP comparison

Environment:

```text
goos: windows
goarch: amd64
pkg: github.com/saramand9/bignet/kpro/benchmark
cpu: Intel(R) Core(TM) i5-14400
```

Command:

```powershell
go test .\benchmark -bench "Benchmark(HTTP_Echo|FiberHTTP_Echo)$" -benchmem -benchtime=5s -count=3
```

Result:

```text
BenchmarkFiberHTTP_Echo/64B-16         	   64996	     81572 ns/op	   0.78 MB/s	     12259 qps	    5568 B/op	      60 allocs/op
BenchmarkFiberHTTP_Echo/64B-16         	   76520	     79443 ns/op	   0.81 MB/s	     12588 qps	    5547 B/op	      60 allocs/op
BenchmarkFiberHTTP_Echo/64B-16         	   80940	     78308 ns/op	   0.82 MB/s	     12770 qps	    5568 B/op	      60 allocs/op
BenchmarkFiberHTTP_Echo/1024B-16       	   54823	     97745 ns/op	  10.48 MB/s	     10231 qps	    8042 B/op	      63 allocs/op
BenchmarkFiberHTTP_Echo/1024B-16       	   69025	     86482 ns/op	  11.84 MB/s	     11563 qps	    7991 B/op	      63 allocs/op
BenchmarkFiberHTTP_Echo/1024B-16       	   72302	    103628 ns/op	   9.88 MB/s	      9650 qps	    8030 B/op	      63 allocs/op
BenchmarkHTTP_Echo/64B-16              	   72721	     85529 ns/op	   0.75 MB/s	     11692 qps	    9001 B/op	      91 allocs/op
BenchmarkHTTP_Echo/64B-16              	   70225	     87530 ns/op	   0.73 MB/s	     11425 qps	    9038 B/op	      91 allocs/op
BenchmarkHTTP_Echo/64B-16              	   69849	     98676 ns/op	   0.65 MB/s	     10134 qps	    9035 B/op	      91 allocs/op
BenchmarkHTTP_Echo/1024B-16            	   39477	    133729 ns/op	   7.66 MB/s	      7478 qps	   15156 B/op	      96 allocs/op
BenchmarkHTTP_Echo/1024B-16            	   58236	    123752 ns/op	   8.27 MB/s	      8081 qps	   15115 B/op	      96 allocs/op
BenchmarkHTTP_Echo/1024B-16            	   60601	     99901 ns/op	  10.25 MB/s	     10010 qps	   15233 B/op	      96 allocs/op
```

Average summary:

```text
Fiber HTTP 64B     79,774 ns/op    12,539 qps    5,561 B/op    60 allocs/op
KPro HTTP 64B      90,578 ns/op    11,084 qps    9,025 B/op    91 allocs/op
Fiber HTTP 1024B   95,952 ns/op    10,481 qps    8,021 B/op    63 allocs/op
KPro HTTP 1024B   119,127 ns/op     8,523 qps   15,168 B/op    96 allocs/op
```

Notes:

- Both benchmarks use the same sequential `POST /echo` client loop and payload sizes.
- Fiber is about 13% higher QPS for 64B and about 23% higher QPS for 1024B in this run.
- Fiber also allocates less memory per operation in this comparison.

## 2026-05-26 optimized KPro HTTP comparison

Environment:

```text
goos: windows
goarch: amd64
pkg: github.com/saramand9/bignet/kpro/benchmark
cpu: Intel(R) Core(TM) i5-14400
```

Command:

```powershell
go test .\benchmark -bench "Benchmark(HTTP_Echo|FiberHTTP_Echo)$" -benchmem -benchtime=5s -count=3
```

Result:

```text
BenchmarkFiberHTTP_Echo/64B-16         	   71425	     83406 ns/op	   0.77 MB/s	     11990 qps	    5564 B/op	      60 allocs/op
BenchmarkFiberHTTP_Echo/64B-16         	   79087	     77537 ns/op	   0.83 MB/s	     12897 qps	    5552 B/op	      60 allocs/op
BenchmarkFiberHTTP_Echo/64B-16         	   76024	     82196 ns/op	   0.78 MB/s	     12166 qps	    5553 B/op	      60 allocs/op
BenchmarkFiberHTTP_Echo/1024B-16       	   66928	     94594 ns/op	  10.83 MB/s	     10572 qps	    8026 B/op	      63 allocs/op
BenchmarkFiberHTTP_Echo/1024B-16       	   66170	     97047 ns/op	  10.55 MB/s	     10304 qps	    8012 B/op	      63 allocs/op
BenchmarkFiberHTTP_Echo/1024B-16       	   63202	     93836 ns/op	  10.91 MB/s	     10657 qps	    8000 B/op	      63 allocs/op
BenchmarkHTTP_Echo/64B-16              	   64267	     84415 ns/op	   0.76 MB/s	     11846 qps	    8798 B/op	      88 allocs/op
BenchmarkHTTP_Echo/64B-16              	   63806	     85040 ns/op	   0.75 MB/s	     11759 qps	    8908 B/op	      88 allocs/op
BenchmarkHTTP_Echo/64B-16              	   73354	     85978 ns/op	   0.74 MB/s	     11631 qps	    8868 B/op	      88 allocs/op
BenchmarkHTTP_Echo/1024B-16            	   63908	     98742 ns/op	  10.37 MB/s	     10127 qps	   11651 B/op	      91 allocs/op
BenchmarkHTTP_Echo/1024B-16            	   62985	     97113 ns/op	  10.54 MB/s	     10297 qps	   11777 B/op	      91 allocs/op
BenchmarkHTTP_Echo/1024B-16            	   61112	     94571 ns/op	  10.83 MB/s	     10574 qps	   11719 B/op	      91 allocs/op
```

Average summary:

```text
Fiber HTTP 64B     81,046 ns/op    12,351 qps    5,556 B/op    60 allocs/op
KPro HTTP 64B      85,144 ns/op    11,745 qps    8,858 B/op    88 allocs/op
Fiber HTTP 1024B   95,159 ns/op    10,511 qps    8,013 B/op    63 allocs/op
KPro HTTP 1024B    96,809 ns/op    10,333 qps   11,716 B/op    91 allocs/op
```

Notes:

- KPro HTTP now uses `CopyBody` for this echo benchmark.
- Compared with the previous KPro run, 64B QPS improved by about 6% and 1024B QPS improved by about 21%.
- In this run Fiber is still about 5% higher QPS for 64B, while 1024B is within about 2%.

## 2026-05-26 Hertz-backed KPro HTTP comparison

Environment:

```text
goos: windows
goarch: amd64
pkg: github.com/saramand9/bignet/kpro/benchmark
cpu: Intel(R) Core(TM) i5-14400
```

Command:

```powershell
go test .\benchmark -bench "Benchmark(HTTP_Echo|FiberHTTP_Echo)$" -benchmem -benchtime=5s -count=3
```

Result:

```text
BenchmarkFiberHTTP_Echo/64B-16         	   70846	     76518 ns/op	   0.84 MB/s	     13069 qps	    5708 B/op	      60 allocs/op
BenchmarkFiberHTTP_Echo/64B-16         	   79186	     81215 ns/op	   0.79 MB/s	     12313 qps	    5692 B/op	      60 allocs/op
BenchmarkFiberHTTP_Echo/64B-16         	   76922	     79944 ns/op	   0.80 MB/s	     12509 qps	    5714 B/op	      60 allocs/op
BenchmarkFiberHTTP_Echo/1024B-16       	   67494	     98233 ns/op	  10.42 MB/s	     10180 qps	    8178 B/op	      63 allocs/op
BenchmarkFiberHTTP_Echo/1024B-16       	   70203	     91602 ns/op	  11.18 MB/s	     10917 qps	    8154 B/op	      63 allocs/op
BenchmarkFiberHTTP_Echo/1024B-16       	   61372	    143981 ns/op	   7.11 MB/s	      6945 qps	    8126 B/op	      63 allocs/op
BenchmarkHTTP_Echo/64B-16              	   65098	     88542 ns/op	   0.72 MB/s	     11294 qps	    6191 B/op	      68 allocs/op
BenchmarkHTTP_Echo/64B-16              	   79614	     77504 ns/op	   0.83 MB/s	     12903 qps	    6162 B/op	      68 allocs/op
BenchmarkHTTP_Echo/64B-16              	   72255	     80747 ns/op	   0.79 MB/s	     12384 qps	    6167 B/op	      68 allocs/op
BenchmarkHTTP_Echo/1024B-16            	   69980	     85853 ns/op	  11.93 MB/s	     11648 qps	    8668 B/op	      71 allocs/op
BenchmarkHTTP_Echo/1024B-16            	   70398	     85295 ns/op	  12.01 MB/s	     11724 qps	    8632 B/op	      71 allocs/op
BenchmarkHTTP_Echo/1024B-16            	   68784	     85065 ns/op	  12.04 MB/s	     11756 qps	    8638 B/op	      71 allocs/op
```

Average summary:

```text
Fiber HTTP 64B     79,226 ns/op    12,630 qps    5,705 B/op    60 allocs/op
KPro HTTP 64B      82,264 ns/op    12,194 qps    6,173 B/op    68 allocs/op
Fiber HTTP 1024B  111,272 ns/op     9,347 qps    8,153 B/op    63 allocs/op
KPro HTTP 1024B    85,404 ns/op    11,709 qps    8,646 B/op    71 allocs/op
```

Notes:

- KPro `httpx` is backed by Hertz in this run.
- On Windows, Hertz reports the standard network library; netpoll is not active here.
- Compared with the previous optimized KPro run, 64B QPS is about 4% higher and 1024B QPS is about 13% higher.
