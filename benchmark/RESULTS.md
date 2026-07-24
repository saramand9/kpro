# KPro benchmark results

## 2026-07-24 hot-path regression baseline

Environment:

```text
goos: windows
goarch: amd64
go: go1.26.5
cpu: Intel(R) Core(TM) i5-14400
source state: v0.1.0 release-candidate tree
```

This measurement was captured from the `v0.1.0` release candidate before
tagging. The module baseline in `go.mod` is Go 1.25.12, while this local run
used Go 1.26.5; CI verifies both Go 1.25.12 and Go 1.26.5. Compare latency
numbers only under the same Go toolchain and benchmark conditions; the
checked-in budgets intentionally span normal CI toolchain and host variation.

Command (the same benchmark selection enforced by CI):

```powershell
go test ./core ./transport/httpx -run '^$' -bench 'Benchmark(RouterDispatchSync|EndpointServeHTTP)$' -benchmem -benchtime=250ms -count=1 | Tee-Object -FilePath benchmark-current.txt
go run ./cmd/benchguard -budget benchmark/budgets.json -input benchmark-current.txt
```

Observed result:

```text
BenchmarkRouterDispatchSync/no_middleware-16                 2101 ns/op    200 B/op     3 allocs/op
BenchmarkRouterDispatchSync/one_middleware-16                2002 ns/op    200 B/op     3 allocs/op
BenchmarkRouterDispatchSync/four_middlewares-16              2008 ns/op    200 B/op     3 allocs/op
BenchmarkEndpointServeHTTP/direct-16                        129.7 ns/op    136 B/op     3 allocs/op
BenchmarkEndpointServeHTTP/middleware-16                    194.2 ns/op    328 B/op     4 allocs/op
BenchmarkEndpointServeHTTP/middleware_headers_meta-16       507.8 ns/op   1048 B/op    11 allocs/op
```

These short microbenchmarks are allocation-regression evidence, not capacity
claims. `benchmark/budgets.json` is the pass/fail contract; latency ceilings
include CI noise headroom while allocation ceilings intentionally remain tight.
The older end-to-end and third-party comparisons below describe historical
implementations and should not be used as a current framework ranking.

## 2026-05-26 smoke

Environment:

```text
goos: windows
goarch: amd64
pkg: github.com/saramand9/kpro/benchmark
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
pkg: github.com/saramand9/kpro/benchmark
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
pkg: github.com/saramand9/kpro/benchmark
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
pkg: github.com/saramand9/kpro/benchmark
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

## 2026-05-26 HTTP concurrent comparison

Environment:

```text
goos: windows
goarch: amd64
pkg: github.com/saramand9/kpro/benchmark
cpu: Intel(R) Core(TM) i5-14400
```

Command:

```powershell
go test .\benchmark -bench "Benchmark(HTTP_Echo_Concurrent|FiberHTTP_Echo_Concurrent)$" -benchmem -benchtime=5s -count=1
```

Result:

```text
BenchmarkFiberHTTP_Echo_Concurrent/64B/16c-16         	  423022	     12476 ns/op	   5.13 MB/s	     80155 qps	    5399 B/op	      59 allocs/op
BenchmarkFiberHTTP_Echo_Concurrent/64B/64c-16         	  633745	     10106 ns/op	   6.33 MB/s	     98953 qps	    5379 B/op	      59 allocs/op
BenchmarkFiberHTTP_Echo_Concurrent/1024B/16c-16       	  398228	     16517 ns/op	  62.00 MB/s	     60543 qps	    7714 B/op	      62 allocs/op
BenchmarkFiberHTTP_Echo_Concurrent/1024B/64c-16       	  381090	     17056 ns/op	  60.04 MB/s	     58629 qps	    7772 B/op	      62 allocs/op
BenchmarkHTTP_Echo_Concurrent/64B/16c-16              	  409732	     15873 ns/op	   4.03 MB/s	     62998 qps	    5832 B/op	      64 allocs/op
BenchmarkHTTP_Echo_Concurrent/64B/64c-16              	  436011	     13999 ns/op	   4.57 MB/s	     71435 qps	    5927 B/op	      64 allocs/op
BenchmarkHTTP_Echo_Concurrent/1024B/16c-16            	  349261	     16210 ns/op	  63.17 MB/s	     61690 qps	    8199 B/op	      67 allocs/op
BenchmarkHTTP_Echo_Concurrent/1024B/64c-16            	  468049	     15365 ns/op	  66.64 MB/s	     65082 qps	    8309 B/op	      67 allocs/op
```

Notes:

- Concurrent benchmarks use the same `POST /echo` payload validation as the sequential HTTP benchmarks.
- This historical run used the old HTTP compatibility-skipping context option.
- At 64 workers, KPro reached about 71k qps for 64B and 65k qps for 1024B on this Windows run.

## 2026-05-26 HTTP content-type fast path check

Environment:

```text
goos: windows
goarch: amd64
pkg: github.com/saramand9/kpro/benchmark
cpu: Intel(R) Core(TM) i5-14400
```

Command:

```powershell
go test .\benchmark -run ^$ -bench "Benchmark(FiberHTTP_Echo_Concurrent|HTTP_Echo_Concurrent)/(64B|1024B)/64c$" -benchmem -benchtime=5s -count=3
```

Result:

```text
BenchmarkFiberHTTP_Echo_Concurrent/64B/64c-16         	  214981	     25284 ns/op	   2.53 MB/s	     39551 qps	    5482 B/op	      59 allocs/op
BenchmarkFiberHTTP_Echo_Concurrent/64B/64c-16         	  440880	     13285 ns/op	   4.82 MB/s	     75275 qps	    5396 B/op	      59 allocs/op
BenchmarkFiberHTTP_Echo_Concurrent/64B/64c-16         	  462312	     13435 ns/op	   4.76 MB/s	     74435 qps	    5394 B/op	      59 allocs/op
BenchmarkFiberHTTP_Echo_Concurrent/1024B/64c-16       	  375852	     16141 ns/op	  63.44 MB/s	     61954 qps	    7770 B/op	      62 allocs/op
BenchmarkFiberHTTP_Echo_Concurrent/1024B/64c-16       	  367632	     16582 ns/op	  61.75 MB/s	     60307 qps	    7764 B/op	      62 allocs/op
BenchmarkFiberHTTP_Echo_Concurrent/1024B/64c-16       	  377881	     17596 ns/op	  58.20 MB/s	     56832 qps	    7807 B/op	      62 allocs/op
BenchmarkHTTP_Echo_Concurrent/64B/64c-16              	  366794	     13707 ns/op	   4.67 MB/s	     72956 qps	    5450 B/op	      61 allocs/op
BenchmarkHTTP_Echo_Concurrent/64B/64c-16              	  529560	     11190 ns/op	   5.72 MB/s	     89365 qps	    5402 B/op	      61 allocs/op
BenchmarkHTTP_Echo_Concurrent/64B/64c-16              	  531381	     12647 ns/op	   5.06 MB/s	     79073 qps	    5423 B/op	      61 allocs/op
BenchmarkHTTP_Echo_Concurrent/1024B/64c-16            	  369147	     16177 ns/op	  63.30 MB/s	     61817 qps	    7872 B/op	      64 allocs/op
BenchmarkHTTP_Echo_Concurrent/1024B/64c-16            	  379186	     13544 ns/op	  75.60 MB/s	     73831 qps	    7857 B/op	      64 allocs/op
BenchmarkHTTP_Echo_Concurrent/1024B/64c-16            	  441746	     14566 ns/op	  70.30 MB/s	     68654 qps	    7863 B/op	      64 allocs/op
```

Notes:

- KPro `httpx` avoids allocating the compatibility `http.Header` map when only response `Content-Type` is set.
- Compared with the previous concurrent run, KPro dropped from 64 to 61 allocs/op for 64B, and from 67 to 64 allocs/op for 1024B.

## 2026-05-26 HTTP vs Fiber extended comparison

Environment:

```text
goos: windows
goarch: amd64
pkg: github.com/saramand9/kpro/benchmark
cpu: Intel(R) Core(TM) i5-14400
```

Commands:

```powershell
go test .\benchmark -run ^$ -bench "Benchmark(FiberHTTP_Echo_Concurrent|HTTP_Echo_Concurrent)$" -benchmem -benchtime=10s -count=3
go test .\benchmark -run ^$ -bench "Benchmark(FiberHTTP_Echo|HTTP_Echo)$" -benchmem -benchtime=10s -count=3
```

Median comparison:

| Case | Fiber qps | KPro qps | KPro vs Fiber | Fiber ns/op | KPro ns/op | KPro allocs/op |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| 64B sequential | 12646 | 9332 | -26.2% | 79079 | 107162 | 62 |
| 1024B sequential | 10897 | 10567 | -3.0% | 91765 | 94632 | 65 |
| 64B/16c | 79624 | 63936 | -19.7% | 12559 | 15641 | 61 |
| 64B/64c | 89408 | 81856 | -8.4% | 11185 | 12217 | 61 |
| 1024B/16c | 62748 | 59800 | -4.7% | 15937 | 16722 | 64 |
| 1024B/64c | 71896 | 70855 | -1.4% | 13909 | 14113 | 64 |

Concurrent result:

```text
BenchmarkFiberHTTP_Echo_Concurrent/64B/16c-16         	  858934	     12296 ns/op	   5.21 MB/s	     81329 qps	    5383 B/op	      59 allocs/op
BenchmarkFiberHTTP_Echo_Concurrent/64B/16c-16         	  875904	     12559 ns/op	   5.10 MB/s	     79624 qps	    5388 B/op	      59 allocs/op
BenchmarkFiberHTTP_Echo_Concurrent/64B/16c-16         	  914450	     13967 ns/op	   4.58 MB/s	     71599 qps	    5388 B/op	      59 allocs/op
BenchmarkFiberHTTP_Echo_Concurrent/64B/64c-16         	 1000000	     11193 ns/op	   5.72 MB/s	     89343 qps	    5376 B/op	      59 allocs/op
BenchmarkFiberHTTP_Echo_Concurrent/64B/64c-16         	 1000000	     10903 ns/op	   5.87 MB/s	     91718 qps	    5370 B/op	      59 allocs/op
BenchmarkFiberHTTP_Echo_Concurrent/64B/64c-16         	 1000000	     11185 ns/op	   5.72 MB/s	     89408 qps	    5378 B/op	      59 allocs/op
BenchmarkFiberHTTP_Echo_Concurrent/1024B/16c-16       	  799972	     15937 ns/op	  64.25 MB/s	     62748 qps	    7710 B/op	      62 allocs/op
BenchmarkFiberHTTP_Echo_Concurrent/1024B/16c-16       	  745342	     16016 ns/op	  63.94 MB/s	     62438 qps	    7722 B/op	      62 allocs/op
BenchmarkFiberHTTP_Echo_Concurrent/1024B/16c-16       	  732238	     15634 ns/op	  65.50 MB/s	     63965 qps	    7725 B/op	      62 allocs/op
BenchmarkFiberHTTP_Echo_Concurrent/1024B/64c-16       	  903222	     12835 ns/op	  79.78 MB/s	     77911 qps	    7748 B/op	      62 allocs/op
BenchmarkFiberHTTP_Echo_Concurrent/1024B/64c-16       	  883636	     14132 ns/op	  72.46 MB/s	     70761 qps	    7746 B/op	      62 allocs/op
BenchmarkFiberHTTP_Echo_Concurrent/1024B/64c-16       	  915402	     13909 ns/op	  73.62 MB/s	     71896 qps	    7754 B/op	      62 allocs/op
BenchmarkHTTP_Echo_Concurrent/64B/16c-16              	  829401	     14764 ns/op	   4.33 MB/s	     67732 qps	    5406 B/op	      61 allocs/op
BenchmarkHTTP_Echo_Concurrent/64B/16c-16              	  748225	     15715 ns/op	   4.07 MB/s	     63635 qps	    5417 B/op	      61 allocs/op
BenchmarkHTTP_Echo_Concurrent/64B/16c-16              	  851049	     15641 ns/op	   4.09 MB/s	     63936 qps	    5405 B/op	      61 allocs/op
BenchmarkHTTP_Echo_Concurrent/64B/64c-16              	  958494	     11201 ns/op	   5.71 MB/s	     89276 qps	    5411 B/op	      61 allocs/op
BenchmarkHTTP_Echo_Concurrent/64B/64c-16              	 1000000	     12217 ns/op	   5.24 MB/s	     81856 qps	    5434 B/op	      61 allocs/op
BenchmarkHTTP_Echo_Concurrent/64B/64c-16              	 1000000	     12345 ns/op	   5.18 MB/s	     81007 qps	    5433 B/op	      61 allocs/op
BenchmarkHTTP_Echo_Concurrent/1024B/16c-16            	  656070	     17219 ns/op	  59.47 MB/s	     58075 qps	    7762 B/op	      64 allocs/op
BenchmarkHTTP_Echo_Concurrent/1024B/16c-16            	  727084	     16638 ns/op	  61.54 MB/s	     60102 qps	    7760 B/op	      64 allocs/op
BenchmarkHTTP_Echo_Concurrent/1024B/16c-16            	  669454	     16722 ns/op	  61.24 MB/s	     59800 qps	    7762 B/op	      64 allocs/op
BenchmarkHTTP_Echo_Concurrent/1024B/64c-16            	  804040	     14113 ns/op	  72.56 MB/s	     70855 qps	    7874 B/op	      64 allocs/op
BenchmarkHTTP_Echo_Concurrent/1024B/64c-16            	  868177	     13920 ns/op	  73.56 MB/s	     71837 qps	    7837 B/op	      64 allocs/op
BenchmarkHTTP_Echo_Concurrent/1024B/64c-16            	  891565	     15784 ns/op	  64.88 MB/s	     63357 qps	    7900 B/op	      64 allocs/op
```

Sequential result:

```text
BenchmarkFiberHTTP_Echo/64B-16         	  135944	     76973 ns/op	   0.83 MB/s	     12992 qps	    5699 B/op	      60 allocs/op
BenchmarkFiberHTTP_Echo/64B-16         	  157995	     79079 ns/op	   0.81 MB/s	     12646 qps	    5698 B/op	      60 allocs/op
BenchmarkFiberHTTP_Echo/64B-16         	  153530	     98482 ns/op	   0.65 MB/s	     10154 qps	    5697 B/op	      60 allocs/op
BenchmarkFiberHTTP_Echo/1024B-16       	  121844	     88286 ns/op	  11.60 MB/s	     11327 qps	    8161 B/op	      63 allocs/op
BenchmarkFiberHTTP_Echo/1024B-16       	  133480	     91765 ns/op	  11.16 MB/s	     10897 qps	    8188 B/op	      63 allocs/op
BenchmarkFiberHTTP_Echo/1024B-16       	  135092	    107037 ns/op	   9.57 MB/s	      9343 qps	    8192 B/op	      63 allocs/op
BenchmarkHTTP_Echo/64B-16              	  145506	    107162 ns/op	   0.60 MB/s	      9332 qps	    5732 B/op	      62 allocs/op
BenchmarkHTTP_Echo/64B-16              	  101668	    108948 ns/op	   0.59 MB/s	      9179 qps	    5761 B/op	      62 allocs/op
BenchmarkHTTP_Echo/64B-16              	  155828	     86102 ns/op	   0.74 MB/s	     11614 qps	    5724 B/op	      62 allocs/op
BenchmarkHTTP_Echo/1024B-16            	  127436	     96566 ns/op	  10.60 MB/s	     10356 qps	    8172 B/op	      65 allocs/op
BenchmarkHTTP_Echo/1024B-16            	  139670	     94632 ns/op	  10.82 MB/s	     10567 qps	    8194 B/op	      65 allocs/op
BenchmarkHTTP_Echo/1024B-16            	  143475	     89339 ns/op	  11.46 MB/s	     11193 qps	    8204 B/op	      65 allocs/op
```

Notes:

- The comparison uses median values because Windows localhost HTTP benchmarks have visible run-to-run variance.
- KPro is still behind Fiber for 64B small payloads, especially sequential and 16-worker concurrency.
- At 1024B/64c, KPro is nearly equal to Fiber on median qps, but still keeps 2 more allocs/op.

## 2026-05-26 fasthttp-backed KPro HTTP comparison

Environment:

```text
goos: windows
goarch: amd64
pkg: github.com/saramand9/kpro/benchmark
cpu: Intel(R) Core(TM) i5-14400
```

Commands:

```powershell
go test .\benchmark -run ^$ -bench "Benchmark(FiberHTTP_Echo_Concurrent|HTTP_Echo_Concurrent)$" -benchmem -benchtime=5s -count=3
go test .\benchmark -run ^$ -bench "Benchmark(FiberHTTP_Echo|HTTP_Echo)$" -benchmem -benchtime=5s -count=3
```

Median comparison:

| Case | Fiber qps | KPro qps | KPro vs Fiber | Fiber ns/op | KPro ns/op | Fiber allocs/op | KPro allocs/op |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| 64B sequential | 12702 | 12305 | -3.1% | 78730 | 81267 | 60 | 60 |
| 1024B sequential | 11407 | 11341 | -0.6% | 87665 | 88176 | 63 | 63 |
| 64B/16c | 47236 | 53019 | +12.2% | 21170 | 18861 | 59 | 59 |
| 64B/64c | 78278 | 79949 | +2.1% | 12775 | 12508 | 59 | 59 |
| 1024B/16c | 40330 | 42919 | +6.4% | 24795 | 23299 | 63 | 62 |
| 1024B/64c | 66370 | 63837 | -3.8% | 15067 | 15665 | 62 | 62 |

Concurrent result:

```text
BenchmarkFiberHTTP_Echo_Concurrent/64B/16c-16         	  316107	     21170 ns/op	   3.02 MB/s	     47236 qps	    6803 B/op	      59 allocs/op
BenchmarkFiberHTTP_Echo_Concurrent/64B/16c-16         	  257266	     22703 ns/op	   2.82 MB/s	     44046 qps	    6896 B/op	      60 allocs/op
BenchmarkFiberHTTP_Echo_Concurrent/64B/16c-16         	  301742	     20740 ns/op	   3.09 MB/s	     48216 qps	    6265 B/op	      59 allocs/op
BenchmarkFiberHTTP_Echo_Concurrent/64B/64c-16         	  441253	     12775 ns/op	   5.01 MB/s	     78278 qps	    5478 B/op	      59 allocs/op
BenchmarkFiberHTTP_Echo_Concurrent/64B/64c-16         	  456334	     13060 ns/op	   4.90 MB/s	     76570 qps	    5497 B/op	      59 allocs/op
BenchmarkFiberHTTP_Echo_Concurrent/64B/64c-16         	  510994	     12761 ns/op	   5.02 MB/s	     78361 qps	    5457 B/op	      59 allocs/op
BenchmarkFiberHTTP_Echo_Concurrent/1024B/16c-16       	  281232	     24795 ns/op	  41.30 MB/s	     40330 qps	    9710 B/op	      63 allocs/op
BenchmarkFiberHTTP_Echo_Concurrent/1024B/16c-16       	  258324	     24517 ns/op	  41.77 MB/s	     40788 qps	    9469 B/op	      63 allocs/op
BenchmarkFiberHTTP_Echo_Concurrent/1024B/16c-16       	  233206	     24868 ns/op	  41.18 MB/s	     40212 qps	    9619 B/op	      63 allocs/op
BenchmarkFiberHTTP_Echo_Concurrent/1024B/64c-16       	  361146	     15067 ns/op	  67.96 MB/s	     66370 qps	    7993 B/op	      62 allocs/op
BenchmarkFiberHTTP_Echo_Concurrent/1024B/64c-16       	  412015	     15206 ns/op	  67.34 MB/s	     65766 qps	    7916 B/op	      62 allocs/op
BenchmarkFiberHTTP_Echo_Concurrent/1024B/64c-16       	  451365	     14476 ns/op	  70.74 MB/s	     69081 qps	    7893 B/op	      62 allocs/op
BenchmarkHTTP_Echo_Concurrent/64B/16c-16              	  352936	     18861 ns/op	   3.39 MB/s	     53019 qps	    5729 B/op	      59 allocs/op
BenchmarkHTTP_Echo_Concurrent/64B/16c-16              	  306985	     18293 ns/op	   3.50 MB/s	     54665 qps	    5595 B/op	      59 allocs/op
BenchmarkHTTP_Echo_Concurrent/64B/16c-16              	  303028	     20009 ns/op	   3.20 MB/s	     49978 qps	    5744 B/op	      59 allocs/op
BenchmarkHTTP_Echo_Concurrent/64B/64c-16              	  336016	     17307 ns/op	   3.70 MB/s	     57779 qps	    5549 B/op	      59 allocs/op
BenchmarkHTTP_Echo_Concurrent/64B/64c-16              	  470613	     12508 ns/op	   5.12 MB/s	     79949 qps	    5446 B/op	      59 allocs/op
BenchmarkHTTP_Echo_Concurrent/64B/64c-16              	  482574	     12234 ns/op	   5.23 MB/s	     81737 qps	    5438 B/op	      59 allocs/op
BenchmarkHTTP_Echo_Concurrent/1024B/16c-16            	  258991	     23299 ns/op	  43.95 MB/s	     42919 qps	    8814 B/op	      62 allocs/op
BenchmarkHTTP_Echo_Concurrent/1024B/16c-16            	  259248	     23600 ns/op	  43.39 MB/s	     42373 qps	    8861 B/op	      62 allocs/op
BenchmarkHTTP_Echo_Concurrent/1024B/16c-16            	  242917	     23125 ns/op	  44.28 MB/s	     43243 qps	    8874 B/op	      62 allocs/op
BenchmarkHTTP_Echo_Concurrent/1024B/64c-16            	  378820	     15027 ns/op	  68.14 MB/s	     66545 qps	    7927 B/op	      62 allocs/op
BenchmarkHTTP_Echo_Concurrent/1024B/64c-16            	  414067	     15850 ns/op	  64.61 MB/s	     63093 qps	    7889 B/op	      62 allocs/op
BenchmarkHTTP_Echo_Concurrent/1024B/64c-16            	  399249	     15665 ns/op	  65.37 MB/s	     63837 qps	    7953 B/op	      62 allocs/op
```

Sequential result:

```text
BenchmarkFiberHTTP_Echo/64B-16         	   77779	     78919 ns/op	   0.81 MB/s	     12671 qps	    5535 B/op	      60 allocs/op
BenchmarkFiberHTTP_Echo/64B-16         	   77749	     78730 ns/op	   0.81 MB/s	     12702 qps	    5579 B/op	      60 allocs/op
BenchmarkFiberHTTP_Echo/64B-16         	   76161	     77523 ns/op	   0.83 MB/s	     12899 qps	    5579 B/op	      60 allocs/op
BenchmarkFiberHTTP_Echo/1024B-16       	   69111	     86659 ns/op	  11.82 MB/s	     11540 qps	    8007 B/op	      63 allocs/op
BenchmarkFiberHTTP_Echo/1024B-16       	   70128	     87665 ns/op	  11.68 MB/s	     11407 qps	    7992 B/op	      63 allocs/op
BenchmarkFiberHTTP_Echo/1024B-16       	   70737	     95629 ns/op	  10.71 MB/s	     10457 qps	    8010 B/op	      63 allocs/op
BenchmarkHTTP_Echo/64B-16              	   60148	     83433 ns/op	   0.77 MB/s	     11986 qps	    5566 B/op	      60 allocs/op
BenchmarkHTTP_Echo/64B-16              	   70819	     80267 ns/op	   0.80 MB/s	     12458 qps	    5605 B/op	      60 allocs/op
BenchmarkHTTP_Echo/64B-16              	   77592	     81267 ns/op	   0.79 MB/s	     12305 qps	    5590 B/op	      60 allocs/op
BenchmarkHTTP_Echo/1024B-16            	   62768	     96889 ns/op	  10.57 MB/s	     10321 qps	    8019 B/op	      63 allocs/op
BenchmarkHTTP_Echo/1024B-16            	   69616	     86450 ns/op	  11.84 MB/s	     11567 qps	    7997 B/op	      63 allocs/op
BenchmarkHTTP_Echo/1024B-16            	   68546	     88176 ns/op	  11.61 MB/s	     11341 qps	    8000 B/op	      63 allocs/op
```

Notes:

- KPro `httpx` is backed by raw `fasthttp` in this run.
- KPro now matches Fiber allocation counts in the common 64B/1024B sequential and 64-worker cases, and is one allocation lower at 1024B/16c.
- On this Windows localhost run, KPro is ahead at 16 workers and near parity at sequential/64 workers, with visible run-to-run variance.

