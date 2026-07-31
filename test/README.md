# KPro test directory

This directory contains cross-package smoke tests for the standalone module.

Current tests:

```text
examples_compile_test.go   compiles every package under examples/
protocol_smoke_test.go     validates KProV1 encode/read round trip
tcp_dispatch_test.go       validates TCP async can bypass a blocked sync handler
tcp_connection_load_test.go provides an opt-in TCP connection lifecycle load check
tcp_file_compare_test.go   records client/server TCP traces and compares files
udp_file_compare_test.go   records client/server UDP traces and compares files
websocket_file_compare_test.go records WebSocket traces and compares files
http_sse_file_compare_test.go  records HTTP, HTTPS and SSE traces and compares files
```

TCP file-compare coverage:

```text
basic echo
sync ordering
async out-of-order responses
sync + async mixed ordering
sync queue full error
no handler error
max in-flight per session error
async queue full error
multi-connection isolation
heartbeat pong bypasses router
```

UDP and WebSocket file-compare coverage:

```text
basic echo
async out-of-order responses
sync queue full error
no handler error
heartbeat pong bypasses router
```

HTTP, HTTPS and SSE file-compare coverage:

```text
HTTP request/response trace consistency
HTTPS request/response trace consistency
SSE server event and client event consistency
```

Run all tests:

```powershell
go test ./...
```

Run the opt-in TCP connection load check with its default 1000 concurrent
connections and 10 request/response messages per connection:

```powershell
$env:KPRO_TCP_CONNECTION_LOAD_TEST = "1"
go test ./test -run TestTCPConnectionLoad -count=1 -timeout=10m
```

Use smaller dimensions for a quick smoke run:

```powershell
$env:KPRO_TCP_CONNECTION_LOAD_TEST = "1"
$env:KPRO_TCP_LOAD_CONNECTIONS = "50"
$env:KPRO_TCP_LOAD_MESSAGES = "2"
$env:KPRO_TCP_LOAD_TIMEOUT = "30s"
go test ./test -run TestTCPConnectionLoad -count=1
```

The test holds one full connection wave open, verifies every configured
request/response round trip, closes it, and then establishes a second full wave
to verify that connection slots can be reused. Logged elapsed time, heap, and
goroutine counts are observations for diagnosing a run; they are not a
performance benchmark or SLA.
