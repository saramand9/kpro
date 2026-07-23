package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseBenchmarksNormalizesCPUSuffix(t *testing.T) {
	input := `BenchmarkRouterDispatchSync/no_middleware-16 1000 1400 ns/op 200 B/op 3 allocs/op
BenchmarkEndpointServeHTTP/direct-16 2000 120 ns/op 120 B/op 3 allocs/op
`
	measurements, err := parseBenchmarks(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	observed := measurements["BenchmarkRouterDispatchSync/no_middleware"]
	if observed.NSPerOp != 1400 || observed.BytesPerOp != 200 || observed.AllocsPerOp != 3 {
		t.Fatalf("measurement = %#v", observed)
	}
}

func TestRunRejectsMissingBenchmemMetrics(t *testing.T) {
	budgetPath := filepath.Join(t.TempDir(), "budgets.json")
	budget := `{"version":1,"benchmarks":{"BenchmarkHotPath":{"max_ns_per_op":100,"max_bytes_per_op":10,"max_allocs_per_op":1}}}`
	if err := os.WriteFile(budgetPath, []byte(budget), 0o600); err != nil {
		t.Fatal(err)
	}
	err := run(
		budgetPath,
		"",
		strings.NewReader("BenchmarkHotPath-16 1000 10 ns/op\n"),
		io.Discard,
	)
	if err == nil || !strings.Contains(err.Error(), "-benchmem") {
		t.Fatalf("run() error = %v, want missing -benchmem error", err)
	}
}

func TestParseBenchmarksRejectsEmptyInput(t *testing.T) {
	if _, err := parseBenchmarks(strings.NewReader("ok package\n")); err == nil {
		t.Fatal("empty benchmark input was accepted")
	}
}
