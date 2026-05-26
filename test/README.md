# KPro test directory

This directory contains cross-package smoke tests for the standalone module.

Current tests:

```text
examples_compile_test.go   compiles every package under examples/
protocol_smoke_test.go     validates KProV1 encode/read round trip
```

Run all tests:

```powershell
go test ./...
```
