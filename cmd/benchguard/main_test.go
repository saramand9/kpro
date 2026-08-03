package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseBenchmarksNormalizesCPUSuffix(t *testing.T) {
	input := `BenchmarkRouterDispatchSync/no_middleware-16 1000 1400 ns/op 200 B/op 3 allocs/op
BenchmarkRouterDispatchSync/no_middleware-16 1000 1300 ns/op 200 B/op 3 allocs/op
BenchmarkEndpointServeHTTP/direct-16 2000 120 ns/op 120 B/op 3 allocs/op
`
	measurements, err := parseBenchmarks(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	samples := measurements["BenchmarkRouterDispatchSync/no_middleware"]
	if len(samples) != 2 {
		t.Fatalf("sample count = %d, want 2", len(samples))
	}
	if samples[0].NSPerOp != 1400 || samples[1].NSPerOp != 1300 {
		t.Fatalf("samples lost input order: %#v", samples)
	}
	if samples[0].BytesPerOp != 200 || samples[0].AllocsPerOp != 3 {
		t.Fatalf("measurement = %#v", samples[0])
	}
}

func TestRunRejectsMissingBenchmemMetrics(t *testing.T) {
	budgetPath := writeTestBudget(t, `{"BenchmarkHotPath":{"max_ns_per_op":100,"max_bytes_per_op":10,"max_allocs_per_op":1}}`)
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

func TestRunUsesMedianNSAcrossSamples(t *testing.T) {
	budgetPath := writeTestBudget(t, `{"BenchmarkHotPath":{"max_ns_per_op":100,"max_bytes_per_op":10,"max_allocs_per_op":1}}`)
	input := `BenchmarkHotPath-16 1000 1000 ns/op 10 B/op 1 allocs/op
BenchmarkHotPath-16 1000 90 ns/op 10 B/op 1 allocs/op
BenchmarkHotPath-16 1000 95 ns/op 10 B/op 1 allocs/op
`
	var output bytes.Buffer
	if err := run(budgetPath, "", strings.NewReader(input), &output); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if !strings.Contains(output.String(), "median 95.00 ns/op") ||
		!strings.Contains(output.String(), "(3 samples)") {
		t.Fatalf("run() output = %q", output.String())
	}
}

func TestRunRejectsEveryMemorySampleOverBudget(t *testing.T) {
	budgetPath := writeTestBudget(t, `{"BenchmarkHotPath":{"max_ns_per_op":100,"max_bytes_per_op":10,"max_allocs_per_op":1}}`)
	input := `BenchmarkHotPath-16 1000 10 ns/op 10 B/op 1 allocs/op
BenchmarkHotPath-16 1000 10 ns/op 11 B/op 1 allocs/op
BenchmarkHotPath-16 1000 10 ns/op 10 B/op 2 allocs/op
`
	err := run(budgetPath, "", strings.NewReader(input), io.Discard)
	if err == nil {
		t.Fatal("run() accepted an over-budget memory sample")
	}
	if !strings.Contains(err.Error(), "sample 2 B/op") {
		t.Fatalf("run() error = %v, want sample 2 B/op failure", err)
	}
	if !strings.Contains(err.Error(), "sample 3 allocs/op") {
		t.Fatalf("run() error = %v, want sample 3 allocs/op failure", err)
	}
}

func TestRunRejectsMissingMetricInAnySample(t *testing.T) {
	budgetPath := writeTestBudget(t, `{"BenchmarkHotPath":{"max_ns_per_op":100,"max_bytes_per_op":10,"max_allocs_per_op":1}}`)
	input := `BenchmarkHotPath-16 1000 10 ns/op 10 B/op 1 allocs/op
BenchmarkHotPath-16 1000 11 ns/op
BenchmarkHotPath-16 1000 10 B/op 1 allocs/op
`
	err := run(budgetPath, "", strings.NewReader(input), io.Discard)
	if err == nil {
		t.Fatal("run() accepted an incomplete sample")
	}
	if !strings.Contains(err.Error(), "sample 2 did not report B/op") ||
		!strings.Contains(err.Error(), "sample 2 did not report allocs/op") ||
		!strings.Contains(err.Error(), "sample 3 did not report ns/op") {
		t.Fatalf("run() error = %v, want every missing metric", err)
	}
}

func TestRunReportsBenchmarksInNameOrder(t *testing.T) {
	budgetPath := writeTestBudget(t, `{
		"BenchmarkZulu":{"max_ns_per_op":100},
		"BenchmarkAlpha":{"max_ns_per_op":100}
	}`)
	input := `BenchmarkZulu-16 1000 10 ns/op
BenchmarkAlpha-16 1000 10 ns/op
`
	var output bytes.Buffer
	if err := run(budgetPath, "", strings.NewReader(input), &output); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	alpha := strings.Index(output.String(), "BenchmarkAlpha:")
	zulu := strings.Index(output.String(), "BenchmarkZulu:")
	if alpha < 0 || zulu < 0 || alpha > zulu {
		t.Fatalf("run() output is not sorted: %q", output.String())
	}
}

func TestRunRejectsMissingRequiredBenchmark(t *testing.T) {
	budgetPath := writeTestBudget(t, `{"BenchmarkRequired":{"max_ns_per_op":100}}`)
	err := run(
		budgetPath,
		"",
		strings.NewReader("BenchmarkOther-16 1000 10 ns/op\n"),
		io.Discard,
	)
	if err == nil || !strings.Contains(err.Error(), `required benchmark "BenchmarkRequired" was not reported`) {
		t.Fatalf("run() error = %v, want missing benchmark error", err)
	}
}

func TestRunEnforcesMinimumSampleCount(t *testing.T) {
	path := filepath.Join(t.TempDir(), "budgets.json")
	data := `{"version":1,"minimum_samples":3,"benchmarks":{"BenchmarkHot":{"max_ns_per_op":100}}}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	err := run(
		path,
		"",
		strings.NewReader("BenchmarkHot-16 1000 10 ns/op\nBenchmarkHot-16 1000 11 ns/op\n"),
		io.Discard,
	)
	if err == nil || !strings.Contains(err.Error(), "reported 2 samples; require at least 3") {
		t.Fatalf("run() error = %v, want minimum-sample failure", err)
	}
}

func TestRunEnforcesExplicitZeroAllocationBudget(t *testing.T) {
	budgetPath := writeTestBudget(t, `{"BenchmarkZero":{"max_bytes_per_op":0,"max_allocs_per_op":0}}`)
	if err := run(
		budgetPath,
		"",
		strings.NewReader("BenchmarkZero-16 1000 10 ns/op 0 B/op 0 allocs/op\n"),
		io.Discard,
	); err != nil {
		t.Fatalf("zero-allocation sample rejected: %v", err)
	}
	err := run(
		budgetPath,
		"",
		strings.NewReader("BenchmarkZero-16 1000 10 ns/op 1 B/op 1 allocs/op\n"),
		io.Discard,
	)
	if err == nil || !strings.Contains(err.Error(), "B/op 1.00 exceeds 0.00") || !strings.Contains(err.Error(), "allocs/op 1.00 exceeds 0.00") {
		t.Fatalf("positive allocation under zero budget error = %v", err)
	}
}

func TestLoadBudgetsRejectsUnknownFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "budgets.json")
	data := `{"version":1,"benchmarks":{"BenchmarkHot":{"max_ns_per_op":10,"max_ns_typo":20}}}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadBudgets(path); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("loadBudgets() error = %v, want unknown-field rejection", err)
	}
}

func TestParseBenchmarksRejectsNonFiniteMetrics(t *testing.T) {
	if _, err := parseBenchmarks(strings.NewReader("BenchmarkHot-16 1000 NaN ns/op 0 B/op 0 allocs/op\n")); err == nil {
		t.Fatal("non-finite benchmark metric was accepted")
	}
}

func TestParseBenchmarksRejectsEmptyInput(t *testing.T) {
	if _, err := parseBenchmarks(strings.NewReader("ok package\n")); err == nil {
		t.Fatal("empty benchmark input was accepted")
	}
}

func writeTestBudget(t *testing.T, benchmarks string) string {
	t.Helper()
	budgetPath := filepath.Join(t.TempDir(), "budgets.json")
	budget := `{"version":1,"benchmarks":` + benchmarks + `}`
	if err := os.WriteFile(budgetPath, []byte(budget), 0o600); err != nil {
		t.Fatal(err)
	}
	return budgetPath
}
