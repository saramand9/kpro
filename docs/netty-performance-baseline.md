# KPro and Netty Performance Baseline

This document records a source-based performance comparison between KPro and
Netty. It is an engineering hypothesis and optimization guide, not a benchmark
result, capacity guarantee, or claim of Netty parity.

## Baseline and evidence boundary

| Field | Baseline |
| --- | --- |
| Assessment date | 2026-08-06 |
| KPro source | `863001bbba3ddc3911a0501cd87310ebc086a510` for the latest measured diagnostic; the source comparison also follows the repository revision containing this document |
| Netty source | Local Netty 4.2 checkout at `b022b4786f2b8258ea75d5b613957a01f2246c5b` |
| KPro measured evidence | [Expanded named TCP diagnostic](../benchmark/RESULTS.md#2026-08-06-expanded-named-tcp-loopback-diagnostic), plus the historical matrices below |
| Comparison acceptance contract | [Performance Fast Paths](performance-fast-path.md#kpro-versus-netty-baseline-contract) |

The implementation comparison comes from the two source trees. The public
concurrency references below come from Netty's documentation site. The
loopback matrix below is a controlled engineering measurement, not a capacity
guarantee or a production certification.

## What Netty's concurrency reputation means

Netty does not publish a universal maximum connection count. Its official
[user guide](https://netty.io/wiki/user-guide-for-4.x.html) describes a
high-performance, high-scalability framework, not a `maxConnections` service
level guarantee.

Netty's official [related-articles
index](https://netty.io/wiki/related-articles.html) links historical third-party
reports of more than 100,000 concurrent connections and 512,000 concurrent
WebSockets. Its [adopters page](https://netty.io/wiki/adopters.html) records a
Livefyre deployment serving more than one million concurrent users. These are
useful evidence that the architecture has operated at large scale, but they do
not establish one repeatable single-machine limit: the machine count, traffic
rate, protocol behavior, business state, and success criteria differ.

The commonly repeated phrase "Netty supports a million concurrent
connections" should therefore be read as an architecture and deployment
capability, usually for carefully tuned, mostly idle or low-frequency long
connections. It must not be translated into one million simultaneously active
requests.

| Term | What must be measured |
| --- | --- |
| Established connections | Open sockets and protocol/session state, including idle peers |
| Active connections | Connections exchanging data during the measurement interval |
| Requests in flight | Application operations accepted but not completed |
| Throughput | Successfully peer-validated messages or bytes per second |
| Sustainable capacity | The highest offered load that also satisfies declared correctness, latency, error, and memory-growth limits |

For example, one million clients each sending one 1 KiB request and receiving
one 1 KiB response every second already implies roughly two million messages
and about 2 GiB/s of application payload traffic before protocol overhead.
Connection count alone says almost nothing about whether a server can sustain
that workload.

## Current implementation comparison

| Dimension | Current KPro implementation | Netty 4.2 implementation | Theoretical position |
| --- | --- | --- | --- |
| Outbound framing | Inline fixed header, pooled payload, optional transport-owned buffer or exact current-payload transfer, and gathered regions | Direct/reference-counted buffers, outbound buffer, gathered writes, and mature allocator integration | KPro has closed much of the narrow framing/admission gap |
| TCP receive | Adaptive socket aggregation, Context-owned reusable codec scratch, and optional bounded decoding of up to 16 complete frames already buffered | Adaptive socket reads, cumulative decoding, retained slices, and multiple frames from one read buffer | KPro now amortizes eligible buffered groups; Netty still has more mature cumulative ownership |
| Execution topology | Typical synchronous work crosses reader, per-session Router executor, outbound queue, and writer goroutines; eligible same-session groups share admission and enqueue synchronization | Decode, handler, and write can remain on one EventLoop when no executor boundary is crossed | KPro reduces repeated synchronization but Netty still has fewer hops for non-blocking I/O work |
| Write formation | Fixed, bounded, opportunistic FIFO drain using `net.Buffers` | Separate write/flush, flush consolidation, adaptive gather limits, write-spin, and partial-progress state | Netty is more adaptive under mixed and partial writes |
| Buffer allocation | Sharded size-class pools with one aggregate idle-retention hard limit and Context-reference-counted receive ownership | Adaptive/pooled direct-buffer allocators, caches, arenas, and reference-counted ownership | KPro has removed steady receive allocation while retaining a simpler hard budget |
| Backpressure | Hard frame/byte admission limits, watermarks, bounded waiters, and close-on-hard-full behavior | High/low writability watermarks, queued-byte accounting, and read-interest control | KPro is strongly bounded; Netty provides finer gradual flow control |
| Large immutable payloads | No file-region or kernel copy-avoidance API | `FileRegion`/transfer support and native transports | Netty has a large advantage for eligible file transfer |
| Platform backends | Go runtime network poller and standard `net` APIs | NIO plus Linux epoll and io_uring implementations | Netty exposes more specialized Linux tuning |
| Blocking handler tolerance | Goroutine isolation makes ordinary blocking application code easier to contain | Blocking an EventLoop delays every channel assigned to it unless work is offloaded | KPro is easier to use safely for conventional blocking business logic |

The relevant KPro paths are
[`codec/kpro.go`](../codec/kpro.go),
[`core/router.go`](../core/router.go), and
[`transport/tcp/server.go`](../transport/tcp/server.go). The pinned Netty
counterparts include
[`AbstractNioByteChannel`](https://github.com/netty/netty/blob/b022b4786f2b8258ea75d5b613957a01f2246c5b/transport/src/main/java/io/netty/channel/nio/AbstractNioByteChannel.java),
[`ByteToMessageDecoder`](https://github.com/netty/netty/blob/b022b4786f2b8258ea75d5b613957a01f2246c5b/codec-base/src/main/java/io/netty/handler/codec/ByteToMessageDecoder.java),
[`NioSocketChannel`](https://github.com/netty/netty/blob/b022b4786f2b8258ea75d5b613957a01f2246c5b/transport/src/main/java/io/netty/channel/socket/nio/NioSocketChannel.java), and
[`ChannelOutboundBuffer`](https://github.com/netty/netty/blob/b022b4786f2b8258ea75d5b613957a01f2246c5b/transport/src/main/java/io/netty/channel/ChannelOutboundBuffer.java).

## 2026-08-04 receive-path validation correction

The read-deadline optimization remains: TCP establishes an initial read
deadline and refreshes it by a bounded time window instead of calling
`SetReadDeadline` for every frame. Idle connections still expire normally,
while handler execution is not counted as read-idle time.

The same iteration also tried to reuse one receive scratch buffer per TCP
session. That optimization was unsafe and has been reverted. `Router.Dispatch`
confirms admission, not handler completion, so a queued or asynchronous handler
could still reference the scratch payload when the next socket read overwrote
it. A deterministic A/B/C queue test and a sequence-dependent full-payload load
test reproduce the corruption on `c1f5051` and pass after restoring independent
payload ownership.

The earlier T01-T09 KPro/Netty table is withdrawn. Its client did not validate
every sequence-dependent payload byte, so successful headers could hide body
corruption and the reported throughput ratios were not trustworthy. As a
negative control, the corrected client completed zero responses and reported
eight validation errors against the unsafe binary with 8 connections, 64-byte
payloads, inflight 16, and a 2-second window. The historical numbers remain in
Git history but must not be used as current performance evidence. A corrected
comparison is recorded below.

## 2026-08-05 safe receive ownership and loopback diagnostic

TCP now uses `ReusableStreamCodec` through an explicit ownership transfer. A
receive buffer is attached to the pooled message Context and is returned only
after the transport caller and any accepted Router task release their
references. KProV1 also reads the 20-byte header and then the payload through
the same scratch allocation. A race-enabled lifecycle test verifies that the
buffer is not returned while a Router handler is active, and the corrected
sequence-dependent load generator reported zero validation errors in every run
below.

The local Windows loopback diagnostic used an Intel Core i5-14400, Go's default
`GOMAXPROCS`, JDK 21, Netty 4.2 at the pinned checkout above, fresh processes,
two seconds of warm-up, four seconds of measurement, alternating order, and
three paired runs. T04 is 64 connections x 16 KiB x inflight 1; T07 is 64 x
64 B x inflight 16 with 16 coalesced requests; T08 is 256 x 1 KiB x inflight
16 with 16 coalesced requests. Every response payload was fully validated.

| Case | KPro throughput range | Netty throughput range | Median paired KPro/Netty | KPro p99 range | Netty p99 range |
| --- | ---: | ---: | ---: | ---: | ---: |
| T04 | 58,948-76,234 msg/s | 61,074-82,975 msg/s | 91.9% | 2.01-2.54 ms | 3.70-6.55 ms |
| T07 | 1,087,508-1,173,788 msg/s | 1,229,892-1,511,790 msg/s | 83.0% | 2.02-2.23 ms | 4.00-5.00 ms |
| T08 | 748,068-872,968 msg/s | 817,422-1,030,460 msg/s | 88.1% | 12.53-15.61 ms | 12.99-16.59 ms |

This is not the acceptance matrix defined later in this document. It is
loopback, short, closed-loop, and has only three process runs. The Netty lane
also echoes a retained wire buffer on its EventLoop, while the KPro lane uses
Router dispatch, recovery middleware, receive ownership, outbound ownership
copying, queue accounting, and a writer goroutine. The table is useful for
tracking this implementation, not for a public parity or production-capacity
claim.

The bounded receive-batch candidate below supersedes these values for the
current candidate source state. This earlier table remains as historical
evidence of the optimization trajectory.

Against the KPro binary at the start of this optimization iteration, the same
paired method observed median throughput changes of +25.8% (T04), +34.1%
(T07), and +22.4% (T08). Median CPU-seconds per completed message fell by about
41%, 36%, and 43%, respectively. T07/T08 tail latency improved substantially;
T08 p50 increased from roughly 2.5-3.0 ms to 4.7-5.2 ms, so the result includes
a visible median-latency/throughput trade-off.

## 2026-08-05 bounded receive batch candidate and loopback retest

The candidate adds three deliberately optional mechanisms:

- `codec.BufferedStreamCodec` lets TCP inspect complete frames already held by
  its adaptive reader;
- `transport.BatchMessageServer` lets a mounted Endpoint offer up to 16
  same-read contexts to `Router.TryDispatchSyncBatch`, with an exact
  no-ownership-transfer/no-accepted-task fallback to ordinary per-message
  dispatch; and
- `core.ReplyCurrentPayload` lets an application explicitly transfer the
  exact current inbound payload to a compatible TCP writer.

The Router batch accepts only same-session synchronous routes under
`BackpressureReject` and only when the whole group fits global, per-session,
and queue capacity. Direct `tcp.Server.ListenAndServe` retains its original
one-frame receive behavior. The comparison below uses ordinary `ctx.Reply`;
it does not exercise the optional current-payload transfer.

Measured source and binaries:

| Field | Value |
| --- | --- |
| KPro base | `d0f037f955d971bb580570ad3d2ba402bcabaded` plus the candidate changes documented here |
| KPro Go-source manifest SHA-256 | `81cded3eb3232cb7c6094c8967ecd9b592459ab8bf1caa750511589f597ab088` |
| KPro server executable SHA-256 | `264f44210640af492c585406f7bfe45a8382b6747a6ebb74755808d1a00a291c` |
| Load-generator executable SHA-256 | `c05619b257cfa26288e91e5092a2b6579e45d32e30d407f3f861a7f700c041be` |
| Netty | `b022b4786f2b8258ea75d5b613957a01f2246c5b`, NIO transport |
| Netty fixture | [`NettyKProEchoServer.java`](../benchmark/fixtures/netty/NettyKProEchoServer.java), SHA-256 `5a9fc10a2f4d52023dd88dea92e1be791396e4346b96a2f98c0533e344ace1c1` |

The Go-source manifest hashes the sorted path plus Git blob ID of every
`*.go`, `go.mod`, `go.sum`, and `benchmark/budgets.json` file in the
candidate worktree; it excludes documentation and saved output.

The Windows loopback run used the same Intel Core i5-14400, Go 1.26.4,
JDK 21.0.3, `GOMAXPROCS=8`, and
`-XX:ActiveProcessorCount=8`. Each server was restricted to logical CPUs 0-7
and the load generator to CPUs 8-15. Every cell used four fresh KPro and four
fresh Netty processes in `K-N-N-K-N-K-K-N` order, with two seconds of warm-up
and ten seconds of measurement. Values below are medians; throughput includes
the observed per-process range.

| Case | KPro msg/s (range) | Netty msg/s (range) | KPro/Netty | p50 K/N | p99 K/N | p99.9 K/N | CPU seconds/million K/N |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| T04: 64 conn, 16 KiB, inflight/coalesce 1 | 78,114 (77,420-81,072) | 81,519 (79,410-83,519) | 95.8% | 0.864/0.999 ms | 2.407/2.718 ms | 6.654/8.758 ms | 37.473/33.081 |
| T07: 64 conn, 64 B, inflight/coalesce 16 | 1,599,423 (1,558,584-1,652,509) | 1,769,514 (1,723,062-1,790,561) | 90.4% | 0.995/0.815 ms | 2.455/2.001 ms | 4.719/3.692 ms | 2.901/1.891 |
| T08: 256 conn, 1 KiB, inflight/coalesce 16 | 831,994 (819,580-874,615) | 982,788 (975,962-1,009,156) | 84.7% | 5.000/4.000 ms | 8.365/6.716 ms | 14.980/11.068 ms | 4.645/2.710 |

All 24 measured processes reported zero payload-validation errors and zero
non-deadline I/O errors. Window-boundary timeouts and the final incomplete
in-flight group remain explicit in the
[raw samples](../benchmark/results/2026-08-05-kpro-netty-loopback.csv).

The point estimates mean:

- T04 KPro throughput is 4.2% lower, while p99 is 11.4% lower and CPU per
  million messages is 13.3% higher;
- T07 throughput is 9.6% lower, p99 is 22.7% higher, and CPU per million is
  53.4% higher; and
- T08 throughput is 15.3% lower, p99 is 24.6% higher, and CPU per million is
  71.4% higher.

T04's throughput point estimate is inside the future 5% non-inferiority
threshold, but that is not a gate pass: this diagnostic lacks confidence
bounds, long steady-state runs, independent load generators, and equal
framework semantics. T07 and T08 remain clearly below the point thresholds.

The Netty fixture cumulatively decodes and echoes a retained wire `ByteBuf` on
its EventLoop, writing each decoded frame and flushing on
`channelReadComplete`. KPro performs Router admission, recovery middleware,
per-session execution, safe receive ownership, ordinary reply copying,
outbound accounting, and writer-goroutine submission. This is intentionally a
repeatable implementation diagnostic, not a claim that the two framework lanes
provide equal scheduling, overload, or blocking-handler semantics.

## 2026-08-06 expanded named TCP diagnostic and PGO check

The follow-up expands the corrected loopback diagnostic to T01-T09, including
T08b, and measures three builds from the same source: ordinary KPro, KPro
compiled with a T07-trained PGO profile, and the pinned Netty NIO fixture.
Every case ran all six implementation-order permutations with fresh processes.
The nine normal cases used three seconds of warm-up and eight seconds of
measurement. T09 used one second of warm-up and two seconds of measurement to
bound the Netty fixture's retained output for non-reading peers.

The run produced 180 process samples and 497,259,490 fully validated
responses, with zero validation errors and zero healthy-peer non-deadline I/O
errors. The durable evidence is split into
[raw samples](../benchmark/results/2026-08-06-kpro-netty-tcp-diagnostic-raw.csv),
[same-cycle ratios](../benchmark/results/2026-08-06-kpro-netty-tcp-diagnostic-pairs.csv),
and the
[case summary](../benchmark/results/2026-08-06-kpro-netty-tcp-diagnostic-summary.csv).

The QPS columns below are six-process medians. The relative columns are the
median of six same-cycle KPro/Netty ratios and are the primary comparison
because the host showed time-dependent drift.

| Case | Ordinary KPro msg/s | PGO KPro msg/s | Netty msg/s | Ordinary/Netty paired | PGO/Netty paired |
| --- | ---: | ---: | ---: | ---: | ---: |
| T01: 1 conn, 64 B, inflight/coalesce 1 | 15,964 | 17,154 | 19,209 | 82.9% | 85.9% |
| T02: 64 conn, 64 B, inflight/coalesce 1 | 93,874 | 95,851 | 97,892 | 95.9% | 96.9% |
| T03: 64 conn, 1 KiB, inflight/coalesce 1 | 93,507 | 96,845 | 97,546 | 98.4% | 97.6% |
| T04: 64 conn, 16 KiB, inflight/coalesce 1 | 59,477 | 60,562 | 63,648 | 97.7% | 96.7% |
| T05: 1,000 conn, 64 B, inflight/coalesce 1 | 80,569 | 83,596 | 87,376 | 92.6% | 94.3% |
| T06: 1,000 conn, 1 KiB, inflight/coalesce 1 | 81,294 | 79,970 | 85,600 | 94.0% | 92.1% |
| T07: 64 conn, 64 B, inflight/coalesce 16 | 1,439,724 | 1,271,079 | 1,499,586 | 89.1% | 88.4% |
| T08: 256 conn, 1 KiB, inflight/coalesce 16 | 809,777 | 829,656 | 891,678 | 90.1% | 92.2% |
| T08b: 1,000 conn, 1 KiB, inflight/coalesce 16 | 753,369 | 766,542 | 837,592 | 89.0% | 90.2% |

Across those nine normal cells, the unweighted geometric means are:

| Build | Paired throughput | Paired p99 | Paired CPU/message |
| --- | ---: | ---: | ---: |
| Ordinary KPro / Netty | 92.1% | 104.7% | 119.4% |
| PGO KPro / Netty | 92.6% | 105.6% | 115.3% |

This resolves two interpretation questions. First, T02-T04 are close in this
short diagnostic, while the batched T07-T08b cases retain a roughly 8-11%
paired throughput gap and a roughly 31-43% CPU/message gap. Second, the
T07-trained PGO profile reduces aggregate CPU consumption but is not a
universal improvement: T06 throughput and tail latency regress, and the
nine-case aggregate p99 is slightly worse.

T09 is an overload-policy diagnostic rather than a same-semantics throughput
comparison. In every run, each KPro build closed all ten non-reading clients
after its bounded backpressure limit was exhausted and kept healthy-peer I/O
errors at zero. The Netty fixture retained those peers until the deadline and
its healthy throughput fell to 12,794 msg/s. That evidence validates KPro's
configured isolation behavior; it does not establish a 6.76x framework
performance advantage.

Like the earlier runs, this remains a short Windows closed-loop diagnostic.
It lacks the independent generator host, long steady state, RSS/PSS evidence,
confidence intervals, and 24-shape/72-offered-load coverage required by the
formal acceptance contract.

## 2026-08-07 WebSocket and SSE diagnostics

The follow-up used validated external clients for RFC 6455 binary messages and
HTTP/1.1 SSE streams. Server and client processes were pinned to separate
eight-logical-CPU groups. Each measured process used a two-second warm-up and
an eight-second fixed window. WebSocket and the initial SSE maximum-throughput
shape ran in KPro-Netty-Netty-KPro order with fresh servers. Each saved cell
contains two process samples; repeat throughput spread was at most 1.95% for
WebSocket and 1.19% for SSE.

Every completed WebSocket message validated its RFC 6455 framing and complete
KProV1 sequence/payload. Every SSE event validated its event syntax, continuous
per-stream hexadecimal sequence, and complete payload. The saved samples have
zero validation errors and zero non-deadline client I/O errors. Deadline
timeouts are one expected stream/window termination per client.

The durable WebSocket evidence is the
[raw process data](../benchmark/results/2026-08-07-kpro-netty-websocket-raw.csv)
and [case summary](../benchmark/results/2026-08-07-kpro-netty-websocket-summary.csv).
The values below are two-sample medians; for two observations this equals their
arithmetic mean.

| Case | KPro msg/s | Netty msg/s | Netty lead | p99 KPro/Netty |
| --- | ---: | ---: | ---: | ---: |
| W01: 1 conn, 64 B, inflight 1 | 22,312 | 24,275 | 8.8% | 0.532 / 0.549 ms |
| W02: 64 conn, 64 B, inflight 1 | 285,283 | 290,364 | 1.8% | 1.504 / 1.503 ms |
| W03: 64 conn, 1 KiB, inflight 1 | 168,984 | 190,506 | 12.7% | 2.201 / 1.503 ms |
| W04: 64 conn, 16 KiB, inflight 1 | 22,846 | 24,506 | 7.3% | 27.637 / 3.902 ms |
| W05: 64 conn, 64 B, inflight/coalesce 16 | 1,998,868 | 2,211,052 | 10.6% | 3.136 / 1.505 ms |
| W06: 256 conn, 1 KiB, inflight/coalesce 16 | 340,876 | 346,997 | 1.8% | 124.068 / 14.764 ms |

The Netty fixture decodes a binary WebSocket frame, validates the KProV1
header, copies the payload into a pooled response buffer, and flushes on
`channelReadComplete`. KPro preserves its normal packet-copy, session queue,
backpressure accounting, and writer-goroutine path. W04 and W06 show a stable
tail-latency issue rather than a uniform slowdown: KPro's median was lower in
those cells, while p99 was materially higher.

SSE is one-way server push, so there is no request inflight or request latency.
The diagnostic instead records aggregate events/s, CPU seconds per million
events, inter-event gaps, and `min stream events / mean stream events`. Netty
has no dedicated SSE module; the fixture uses Netty's official HTTP codec with
a chunked `text/event-stream` handler and flushes every event, matching KPro
`Context.Send` semantics.

The initial maximum-throughput shape allowed the idiomatic KPro handler to loop
without voluntarily yielding. Netty retained a 32-event EventLoop quantum so
multiple channels could progress. Aggregate throughput was within 2.8% in all
six cells, but KPro's slowest stream received only 29-66% of mean stream
progress in multi-stream cells. Netty measured 93-100%.

| Case | KPro events/s | Netty events/s | KPro vs Netty | min/mean KPro/Netty |
| --- | ---: | ---: | ---: | ---: |
| S01: 1 stream, 64 B | 94,398 | 92,956 | +1.6% | 100.0 / 100.0% |
| S02: 64 streams, 64 B | 415,180 | 413,816 | +0.3% | 58.1 / 99.6% |
| S03: 64 streams, 1 KiB | 218,489 | 219,591 | -0.5% | 66.2 / 99.7% |
| S04: 64 streams, 16 KiB | 24,358 | 25,037 | -2.7% | 62.0 / 99.4% |
| S05: 256 streams, 64 B | 421,910 | 411,087 | +2.6% | 53.9 / 93.3% |
| S06: 256 streams, 1 KiB | 224,558 | 220,363 | +1.9% | 29.0 / 99.4% |

The cooperative comparison then made both producers yield after every 32
events. KPro used `runtime.Gosched`; Netty resubmitted a task to its local
EventLoop. Only KPro changed, so its two samples were rerun on the same pinned
CPU groups and replace the earlier KPro rows; the unchanged Netty rows remain
from the interleaved run. Throughput spread remained below 1.2%, but this is a
diagnostic comparison rather than paired statistical evidence.

| Case | KPro events/s | Netty events/s | Netty lead | p99.9 gap KPro/Netty | min/mean KPro/Netty |
| --- | ---: | ---: | ---: | ---: | ---: |
| S01: 1 stream, 64 B | 77,868 | 92,956 | 19.4% | 0.63 / 0.60 ms | 100.0 / 100.0% |
| S02: 64 streams, 64 B | 233,708 | 413,816 | 77.1% | 28.72 / 4.96 ms | 91.1 / 99.6% |
| S03: 64 streams, 1 KiB | 155,773 | 219,591 | 41.0% | 78.92 / 9.02 ms | 79.1 / 99.7% |
| S04: 64 streams, 16 KiB | 22,531 | 25,037 | 11.1% | 396.46 / 75.41 ms | 62.8 / 99.4% |
| S05: 256 streams, 64 B | 233,910 | 411,087 | 75.8% | 128.13 / 20.59 ms | 78.6 / 93.3% |
| S06: 256 streams, 1 KiB | 158,604 | 220,363 | 38.9% | 334.01 / 37.89 ms | 56.7 / 99.4% |

The SSE evidence is split into maximum-throughput
[raw data](../benchmark/results/2026-08-07-kpro-netty-sse-max-throughput-raw.csv)
and [summary](../benchmark/results/2026-08-07-kpro-netty-sse-max-throughput-summary.csv),
plus cooperative-quantum
[raw data](../benchmark/results/2026-08-07-kpro-netty-sse-fair-quantum-raw.csv)
and [summary](../benchmark/results/2026-08-07-kpro-netty-sse-fair-quantum-summary.csv).
The result shows a scheduling trade-off, not that `runtime.Gosched` should be
added to the transport hot path. A production fairness design needs a measured
sharded broadcaster or another bounded scheduler rather than a per-stream
unconditional yield.

Both protocol comparisons remain short Windows loopback diagnostics. They do
not provide confidence intervals, independent-host network evidence, RSS/PSS,
slow-reader coverage, reconnect-storm behavior, or production certification.

## Largest current gap: scheduling and write-submission efficiency

The normal KProV1 TCP receive path currently performs:

```text
adaptive socket aggregation
-> ReadInto(reusable owned scratch)
-> decode up to 15 additional complete frames already buffered
-> copy each additional payload into independently owned pool storage
-> validate header and payload length
-> transfer scratch ownership to Context
-> batch Router admission when the group is eligible
```

The isolated buffered decoder made the after-first-frame decode segment about
20% faster, but decoder-only T07 throughput changed by -1.2% and CPU per
message by -0.6%. It was therefore not retained as a universal direct-TCP path;
it remains bounded plumbing for the batch-enabled Endpoint path. Additional
buffered payloads are copied into independent transport ownership before the
shared adaptive buffer is reused.

Router batch admission reduced the 16-message in-memory admission segment by
about 21%. Against the clean end-to-end candidate baseline, it changed T07
throughput by +2.5% and CPU per message by -14.7%; T08 throughput was neutral
at -0.3% while CPU per message fell 13.3%. The retained benefit is therefore
primarily synchronization and CPU amortization, not a universal throughput
increase.

A later T07 CPU profile still attributed roughly 35% flat CPU to Go's Windows
`cgocall` boundary, about 33% cumulative to `net.Buffers.WriteTo`/`WSASend`,
and about 12% to `runtime.runqgrab`. Router execution remained material, but
buffer-pool get/put and the batch-admission function were each only a few
percent cumulative. These attributions are profile guides rather than additive
shares, but they agree with the latest CPU-per-message gap: the next large
category is execution topology and write submission, not another isolated
pool or decoder micro-optimization.

## Scheduling and batching trade-offs

A typical synchronous KPro message follows this shape:

```text
TCP reader goroutine
-> Router admission and per-session executor
-> handler
-> outbound queue
-> TCP writer goroutine
```

This design preserves per-session ordering and isolates blocking work, but its
channels, locks, queue accounting, and goroutine wakeups add CPU cost. Netty's
same-EventLoop fast path avoids most cross-thread scheduling for a non-blocking
handler. A fair comparison must either keep both handlers non-blocking or
offload blocking Netty work; deliberately blocking Netty's EventLoop is not an
equivalent configuration.

KPro's writer takes the first queued frame and immediately drains only the
frames already available, subject to fixed frame and byte limits. That is
bounded and low complexity, but it is opportunistic. Netty can intentionally
separate `write` from `flush`, consolidate flushes, retain partial write
progress, and adjust the write quantum. Those mechanisms should improve
syscall amortization and tail behavior when traffic is bursty or sockets accept
only part of a submission.

## 2026-08-04 corrected Router profile follow-up

The corrected KPro-only comparison used a safe baseline consisting of
`c1f5051` plus the receive-ownership fix. The treatment added only the
session-local Router close signal. Both sides used the same full-payload
validating client, five fresh-process samples, a 2-second warm-up, a 10-second
fixed window, and an interleaved B-L-L-B order on Windows with Go 1.26.4.
Every sample had zero validation and non-timeout I/O errors.

| Case | Baseline -> local mean | Baseline -> local median | Median paired change |
| --- | ---: | ---: | ---: |
| T07: 64 conn, 64 B, inflight/coalesce 16 | 939,684 -> 1,036,720 (+10.3%) | 1,001,876 -> 1,037,396 (+3.5%) | +3.5% |
| T08: 256 conn, 1 KiB, inflight/coalesce 16 | 667,373 -> 655,725 (-1.7%) | 656,931 -> 670,180 (+2.0%) | -0.5% |

The T07 baseline slowed in later rounds, so its mean overstates the typical
gain; the paired median of about 3.5% is the more robust summary. T08 shows no
stable throughput benefit. The raw T07 baseline/local rates were
`[1182332, 1001876, 1015292, 781029, 717892]` and
`[1107160, 1037396, 986107, 1003999, 1048939]`. T08 was
`[741857, 642442, 673291, 656931, 622344]` and
`[671516, 649110, 670180, 681043, 606776]`.

A matched 20-second T07 CPU profile produced 1,084,160 and 1,085,981 validated
messages/s. Normalized total CPU moved from 13.18 to 13.12 CPU seconds per
million messages (-0.4%). Within that total, `runtime.selectgo` moved from
4.03 to 0.33 (-91.7%) and `runtime.sellock` from 3.67 to 0.07 (-98.1%).
The buffer-pool-focused cumulative share moved from 17.6% to 35.7%, showing
hotspot migration rather than a large end-to-end CPU reduction.

The session-local close signal is retained because it removes measured shared
select-channel contention with a small implementation change and no observed
cross-workload regression. A channel-pool sharding candidate changed T07 mean
and median by only +0.60% and +0.64%; its paired-difference confidence interval
included zero, so it was rejected. Earlier batch-scheduler and atomic global
admission candidates likewise showed no stable benefit. This evidence is
same-machine closed-loop diagnostic data; it supports neither a tail-latency
claim nor a current Netty comparison.

## What the current FastPath evidence proves

The recorded Windows in-memory regression run observed:

| Case | Median | Memory |
| --- | ---: | ---: |
| Contiguous TCP send admission, 1 KiB | 343.2 ns/op | 1152 B/op, 1 allocation |
| Segmented TCP send admission, 1 KiB | 210.4 ns/op | 0 B/op, 0 allocations |
| Owned-buffer commit, 1 KiB | 236.3 ns/op | 64 B/op, 1 lease allocation |
| TCP batch loop, 16 x 1 KiB | 1692 ns/op | 0 B/op, 0 allocations |
| WebSocket batch loop, 16 x 1 KiB | 1724 ns/op | 0 B/op, 0 allocations |
| Stream decode versus buffered after-first-frame segment | 490.4 versus 407.5 ns/op | 0 allocations |
| Router sequential versus batch admission, 16 messages | 11.11 versus 8.75 us/op | 0 allocations |
| Ordinary copy versus `ReplyCurrentPayload`, 64 B | 255.0 versus 208.3 ns/op | 0 allocations |

The segmented admission result was about 39% lower than KPro's previous
contiguous in-memory baseline and removed its warmed per-operation allocation.
The benchmark does not contain a real socket, kernel scheduling, peer, Router
round trip, or Netty process. It proves a KPro regression improvement only; it
does not prove a 39% service-throughput improvement or a lead over Netty.

## Expected result by workload

| Workload | Source-based expectation before measurement |
| --- | --- |
| Up to roughly 1,000 users, with database/RPC-dominated handlers | Framework I/O difference is unlikely to be the primary bottleneck; KPro is technically suitable when limits are configured and validated |
| 64 B to 1 KiB pure echo at high QPS | The latest diagnostic shows Netty leading throughput and CPU efficiency; fewer execution hops and cheaper write submission now dominate the measured explanation |
| Several frames available in each socket read | KPro can decode and batch-admit up to 16 buffered frames, but copies additional payloads into independent ownership; Netty retains a mature retained-slice/EventLoop advantage |
| Bursty writers, partial writes, and slow consumers | Netty is likely to lead in efficiency and tail stability; KPro still provides explicit hard resource bounds |
| Blocking database or filesystem handlers | KPro is easier to use safely; a correctly configured Netty application must offload the work |
| Large file or immutable-region transfer | Netty has a large structural advantage until KPro adds a file-region/sendfile path |
| Idle process footprint and startup | KPro is structurally simpler and likely lighter, but this requires an equal-semantics RSS/startup measurement before making a numerical claim |

For the stated basic-application target of no more than about 1,000 users, this
comparison does not identify a framework-level capacity blocker. It also does
not turn user count into a capacity result: connection multiplicity, requests
per second, payload distribution, TLS, slow clients, and downstream latency
still belong in the application load model.

## Optimization order

The next work should target measured architectural gaps in this order:

1. Prototype scheduler-topology changes one at a time, without adding a second
   public server mode, and retain them only when T04, T07, and T08 all preserve
   correctness and avoid a cross-workload regression.
2. Measure write syscall density and partial-write behavior, then refine flush
   consolidation, adaptive
   write quanta, and partial-write progress only where justified.
3. Keep payload-ownership shortcuts explicit and workload-specific; do not
   change normal `Reply` or `Session.Send` caller ownership.
4. Refine read-side backpressure so sustained outbound pressure can pause and
   resume reads before the hard close boundary.
5. Add a separate file-region/sendfile capability for eligible large immutable
   payloads; consider Linux-specific copy avoidance only after that path has a
   reproducible CPU-per-GiB requirement.

The earlier standalone scheduler, channel-pool sharding, and atomic-admission
candidates were not retained in their original forms. The current mailbox,
atomic capacity, and payload-pool sharding implementations were accepted only
after the integrated ownership changes produced stable cumulative A/B gains.
Reopen a rejected variant only if a representative profile reproduces its
specific bottleneck and it passes correctness, tail-latency, and cross-workload
regression checks.

The completed one-variable campaign rejected contiguous small-message copying,
weighted batch credits, a direct-mapped session-executor cache, and a
session-local buffer magazine because at least one end-to-end workload lost
throughput or CPU efficiency. Those decisions and their measurements are
preserved in [Benchmark Results](../benchmark/RESULTS.md#2026-08-05-bounded-receive-batch-candidate)
so future work does not repeat them without new evidence.

Every step must retain current correctness properties: bounded memory,
per-session ordering, caller payload ownership, deterministic close behavior,
and complete cleanup on errors. The real-machine matrix and acceptance gates
in [Performance Fast Paths](performance-fast-path.md#kpro-versus-netty-baseline-contract)
remain the authority for any future performance ratio or parity claim.
