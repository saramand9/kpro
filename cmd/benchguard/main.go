// Command benchguard validates Go benchmark output against repository-owned
// performance budgets. It is intentionally small so CI does not require an
// additional binary or hosted benchmark service.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

type budgetFile struct {
	Version        int               `json:"version"`
	MinimumSamples int               `json:"minimum_samples,omitempty"`
	Benchmarks     map[string]budget `json:"benchmarks"`
}

type budget struct {
	MaxNSPerOp     *float64 `json:"max_ns_per_op"`
	MaxBytesPerOp  *float64 `json:"max_bytes_per_op"`
	MaxAllocsPerOp *float64 `json:"max_allocs_per_op"`
}

type measurement struct {
	NSPerOp        float64
	BytesPerOp     float64
	AllocsPerOp    float64
	HasNSPerOp     bool
	HasBytesPerOp  bool
	HasAllocsPerOp bool
}

var cpuSuffix = regexp.MustCompile(`-\d+$`)

func main() {
	budgetPath := flag.String("budget", "benchmark/budgets.json", "performance budget JSON file")
	inputPath := flag.String("input", "", "go test benchmark output; stdin when empty")
	flag.Parse()
	if err := run(*budgetPath, *inputPath, os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "benchguard:", err)
		os.Exit(1)
	}
}

func run(budgetPath, inputPath string, stdin io.Reader, output io.Writer) error {
	budgets, err := loadBudgets(budgetPath)
	if err != nil {
		return err
	}
	reader := stdin
	var input *os.File
	if inputPath != "" {
		input, err = os.Open(inputPath)
		if err != nil {
			return fmt.Errorf("open input: %w", err)
		}
		defer input.Close()
		reader = input
	}
	measurements, err := parseBenchmarks(reader)
	if err != nil {
		return err
	}
	benchmarkNames := make([]string, 0, len(budgets.Benchmarks))
	for name := range budgets.Benchmarks {
		benchmarkNames = append(benchmarkNames, name)
	}
	sort.Strings(benchmarkNames)

	var failures []error
	for _, name := range benchmarkNames {
		limits := budgets.Benchmarks[name]
		samples, ok := measurements[name]
		if !ok {
			failures = append(failures, fmt.Errorf("required benchmark %q was not reported", name))
			continue
		}
		if len(samples) < budgets.MinimumSamples {
			failures = append(failures, fmt.Errorf(
				"%s reported %d samples; require at least %d",
				name,
				len(samples),
				budgets.MinimumSamples,
			))
		}
		summary := summarize(samples)
		if limits.MaxNSPerOp != nil {
			maximum := *limits.MaxNSPerOp
			for index, observed := range samples {
				if !observed.HasNSPerOp {
					failures = append(failures, fmt.Errorf("%s sample %d did not report ns/op", name, index+1))
				}
			}
			if summary.HasNSPerOp && summary.NSPerOp > maximum {
				failures = append(failures, fmt.Errorf("%s median ns/op %.2f exceeds %.2f across %d samples", name, summary.NSPerOp, maximum, len(samples)))
			}
		}
		if limits.MaxBytesPerOp != nil {
			maximum := *limits.MaxBytesPerOp
			for index, observed := range samples {
				if !observed.HasBytesPerOp {
					failures = append(failures, fmt.Errorf("%s sample %d did not report B/op; run benchmarks with -benchmem", name, index+1))
				} else if observed.BytesPerOp > maximum {
					failures = append(failures, fmt.Errorf("%s sample %d B/op %.2f exceeds %.2f", name, index+1, observed.BytesPerOp, maximum))
				}
			}
		}
		if limits.MaxAllocsPerOp != nil {
			maximum := *limits.MaxAllocsPerOp
			for index, observed := range samples {
				if !observed.HasAllocsPerOp {
					failures = append(failures, fmt.Errorf("%s sample %d did not report allocs/op; run benchmarks with -benchmem", name, index+1))
				} else if observed.AllocsPerOp > maximum {
					failures = append(failures, fmt.Errorf("%s sample %d allocs/op %.2f exceeds %.2f", name, index+1, observed.AllocsPerOp, maximum))
				}
			}
		}
		fmt.Fprintf(
			output,
			"%s: median %.2f ns/op, max %.0f B/op, max %.0f allocs/op (%d samples)\n",
			name,
			summary.NSPerOp,
			summary.BytesPerOp,
			summary.AllocsPerOp,
			len(samples),
		)
	}
	return errors.Join(failures...)
}

func loadBudgets(path string) (budgetFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return budgetFile{}, fmt.Errorf("read budget: %w", err)
	}
	var budgets budgetFile
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&budgets); err != nil {
		return budgetFile{}, fmt.Errorf("decode budget: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return budgetFile{}, errors.New("decode budget: trailing JSON value")
	}
	if budgets.Version != 1 {
		return budgetFile{}, fmt.Errorf("unsupported budget version %d", budgets.Version)
	}
	if len(budgets.Benchmarks) == 0 {
		return budgetFile{}, errors.New("budget contains no benchmarks")
	}
	if budgets.MinimumSamples < 0 {
		return budgetFile{}, errors.New("minimum_samples cannot be negative")
	}
	if budgets.MinimumSamples == 0 {
		budgets.MinimumSamples = 1
	}
	names := make([]string, 0, len(budgets.Benchmarks))
	for name := range budgets.Benchmarks {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		limits := budgets.Benchmarks[name]
		if name == "" {
			return budgetFile{}, errors.New("budget benchmark name cannot be empty")
		}
		if limits.MaxNSPerOp == nil && limits.MaxBytesPerOp == nil && limits.MaxAllocsPerOp == nil {
			return budgetFile{}, fmt.Errorf("benchmark %q contains no limits", name)
		}
		if err := validateBudgetLimit(name, "max_ns_per_op", limits.MaxNSPerOp); err != nil {
			return budgetFile{}, err
		}
		if err := validateBudgetLimit(name, "max_bytes_per_op", limits.MaxBytesPerOp); err != nil {
			return budgetFile{}, err
		}
		if err := validateBudgetLimit(name, "max_allocs_per_op", limits.MaxAllocsPerOp); err != nil {
			return budgetFile{}, err
		}
	}
	return budgets, nil
}

func validateBudgetLimit(benchmark, metric string, value *float64) error {
	if value != nil && (*value < 0 || math.IsNaN(*value) || math.IsInf(*value, 0)) {
		return fmt.Errorf("benchmark %q %s must be finite and non-negative", benchmark, metric)
	}
	return nil
}

func parseBenchmarks(reader io.Reader) (map[string][]measurement, error) {
	measurements := make(map[string][]measurement)
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 4 || !strings.HasPrefix(fields[0], "Benchmark") {
			continue
		}
		name := cpuSuffix.ReplaceAllString(fields[0], "")
		observed := measurement{}
		for index := 2; index+1 < len(fields); index += 2 {
			unit := fields[index+1]
			if unit != "ns/op" && unit != "B/op" && unit != "allocs/op" {
				continue
			}
			value, err := strconv.ParseFloat(fields[index], 64)
			if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
				return nil, fmt.Errorf("parse %s %s value %q", name, unit, fields[index])
			}
			switch unit {
			case "ns/op":
				observed.NSPerOp = value
				observed.HasNSPerOp = true
			case "B/op":
				observed.BytesPerOp = value
				observed.HasBytesPerOp = true
			case "allocs/op":
				observed.AllocsPerOp = value
				observed.HasAllocsPerOp = true
			}
		}
		measurements[name] = append(measurements[name], observed)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan benchmark output: %w", err)
	}
	if len(measurements) == 0 {
		return nil, errors.New("input contains no Go benchmark results")
	}
	return measurements, nil
}

func summarize(samples []measurement) measurement {
	summary := measurement{
		HasNSPerOp:     len(samples) > 0,
		HasBytesPerOp:  len(samples) > 0,
		HasAllocsPerOp: len(samples) > 0,
	}
	nsPerOp := make([]float64, 0, len(samples))
	for _, observed := range samples {
		if observed.HasNSPerOp {
			nsPerOp = append(nsPerOp, observed.NSPerOp)
		} else {
			summary.HasNSPerOp = false
		}
		if observed.HasBytesPerOp {
			summary.BytesPerOp = max(summary.BytesPerOp, observed.BytesPerOp)
		} else {
			summary.HasBytesPerOp = false
		}
		if observed.HasAllocsPerOp {
			summary.AllocsPerOp = max(summary.AllocsPerOp, observed.AllocsPerOp)
		} else {
			summary.HasAllocsPerOp = false
		}
	}
	if summary.HasNSPerOp {
		sort.Float64s(nsPerOp)
		middle := len(nsPerOp) / 2
		if len(nsPerOp)%2 == 0 {
			summary.NSPerOp = (nsPerOp[middle-1] + nsPerOp[middle]) / 2
		} else {
			summary.NSPerOp = nsPerOp[middle]
		}
	}
	return summary
}
