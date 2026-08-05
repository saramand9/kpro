# KPro and Netty Performance Baseline

This document records a source-based performance comparison between KPro and
Netty. It is an engineering hypothesis and optimization guide, not a benchmark
result, capacity guarantee, or claim of Netty parity.

## Baseline and evidence boundary

| Field | Baseline |
| --- | --- |
| Assessment date | 2026-08-05 |
| KPro source | The repository revision containing this document and the segmented TCP/WebSocket FastPath work |
| Netty source | Local Netty 4.2 checkout at `b022b4786f2b8258ea75d5b613957a01f2246c5b` |
| KPro measured evidence | [FastPath regression baseline](../benchmark/RESULTS.md#2026-08-03-fastpath-regression-baseline), plus the 2026-08-04 loopback matrix below |
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
| Outbound framing | Inline fixed header, pooled payload, optional transport-owned buffer, and gathered regions | Direct/reference-counted buffers, outbound buffer, gathered writes, and mature allocator integration | KPro has closed much of the narrow framing/admission gap |
| TCP receive | Adaptive socket aggregation plus Context-owned reusable codec scratch; KProV1 reuses the same storage for header and payload, but emits one frame per codec call | Adaptive socket reads, cumulative decoding, and multiple frames from one read buffer | Netty retains a decode/scheduling advantage for small coalesced frames |
| Execution topology | Typical synchronous message path crosses reader, per-session Router executor, outbound queue, and writer goroutines | Decode, handler, and write can remain on one EventLoop when no executor boundary is crossed | Netty has fewer hops for non-blocking I/O work |
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

Against the KPro binary at the start of this optimization iteration, the same
paired method observed median throughput changes of +25.8% (T04), +34.1%
(T07), and +22.4% (T08). Median CPU-seconds per completed message fell by about
41%, 36%, and 43%, respectively. T07/T08 tail latency improved substantially;
T08 p50 increased from roughly 2.5-3.0 ms to 4.7-5.2 ms, so the result includes
a visible median-latency/throughput trade-off.

## Largest current gap: scheduling and cumulative decode

The normal KProV1 TCP receive path currently performs:

```text
adaptive socket aggregation
-> ReadInto(reusable owned scratch)
-> validate header and payload length
-> transfer scratch ownership to Context
-> Router dispatch
```

The receive payload and header no longer require steady per-message backing
allocations after the pool is warm. The remaining decoder gap is structural:
the codec interface still returns one frame per call instead of walking a
cumulative buffer and emitting every complete frame already available.

Netty reads available bytes into an adaptively sized buffer and its cumulative
decoder can preserve partial frames and emit several messages from the same
input buffer. The final T07 CPU profile attributed about 9% cumulative CPU to
`readFrame`/KProV1 decoding, while Windows `WSASend`/writev and Go scheduler
work were materially larger. A multi-frame decoder is therefore no longer an
automatic first change: it needs an isolated prototype and measured benefit
large enough to justify widening the codec/transport contract.

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

The segmented admission result was about 39% lower than KPro's previous
contiguous in-memory baseline and removed its warmed per-operation allocation.
The benchmark does not contain a real socket, kernel scheduling, peer, Router
round trip, or Netty process. It proves a KPro regression improvement only; it
does not prove a 39% service-throughput improvement or a lead over Netty.

## Expected result by workload

| Workload | Source-based expectation before measurement |
| --- | --- |
| Up to roughly 1,000 users, with database/RPC-dominated handlers | Framework I/O difference is unlikely to be the primary bottleneck; KPro is technically suitable when limits are configured and validated |
| 64 B to 1 KiB pure echo at high QPS | The local diagnostic shows Netty leading throughput; cumulative decode and fewer scheduling hops remain the likely reasons |
| Several frames available in each socket read | Netty retains an advantage because KPro aggregates socket reads but still invokes the codec once per frame |
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

1. Prototype cumulative multi-frame decoding behind a codec-neutral ownership
   contract, and retain it only if CPU profiles and full-payload A/B tests show
   a material win.
2. Investigate scheduler topology and runnable-goroutine stealing without
   weakening per-session ordering or blocking-handler isolation.
3. Measure write syscall density, then refine flush consolidation, adaptive
   write quanta, and partial-write progress only where justified.
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

Every step must retain current correctness properties: bounded memory,
per-session ordering, caller payload ownership, deterministic close behavior,
and complete cleanup on errors. The real-machine matrix and acceptance gates
in [Performance Fast Paths](performance-fast-path.md#kpro-versus-netty-baseline-contract)
remain the authority for any future performance ratio or parity claim.
