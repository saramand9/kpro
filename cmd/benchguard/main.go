// Command benchguard validates Go benchmark output against repository-owned
// performance budgets. It is intentionally small so CI does not require an
// additional binary or hosted benchmark service.
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
)

type budgetFile struct {
	Version    int               `json:"version"`
	Benchmarks map[string]budget `json:"benchmarks"`
}

type budget struct {
	MaxNSPerOp     float64 `json:"max_ns_per_op"`
	MaxBytesPerOp  float64 `json:"max_bytes_per_op"`
	MaxAllocsPerOp float64 `json:"max_allocs_per_op"`
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
	var failures []error
	for name, limits := range budgets.Benchmarks {
		observed, ok := measurements[name]
		if !ok {
			failures = append(failures, fmt.Errorf("required benchmark %q was not reported", name))
			continue
		}
		if limits.MaxNSPerOp > 0 {
			if !observed.HasNSPerOp {
				failures = append(failures, fmt.Errorf("%s did not report ns/op", name))
			} else if observed.NSPerOp > limits.MaxNSPerOp {
				failures = append(failures, fmt.Errorf("%s ns/op %.2f exceeds %.2f", name, observed.NSPerOp, limits.MaxNSPerOp))
			}
		}
		if limits.MaxBytesPerOp > 0 {
			if !observed.HasBytesPerOp {
				failures = append(failures, fmt.Errorf("%s did not report B/op; run benchmarks with -benchmem", name))
			} else if observed.BytesPerOp > limits.MaxBytesPerOp {
				failures = append(failures, fmt.Errorf("%s B/op %.2f exceeds %.2f", name, observed.BytesPerOp, limits.MaxBytesPerOp))
			}
		}
		if limits.MaxAllocsPerOp > 0 {
			if !observed.HasAllocsPerOp {
				failures = append(failures, fmt.Errorf("%s did not report allocs/op; run benchmarks with -benchmem", name))
			} else if observed.AllocsPerOp > limits.MaxAllocsPerOp {
				failures = append(failures, fmt.Errorf("%s allocs/op %.2f exceeds %.2f", name, observed.AllocsPerOp, limits.MaxAllocsPerOp))
			}
		}
		fmt.Fprintf(output, "%s: %.2f ns/op, %.0f B/op, %.0f allocs/op\n", name, observed.NSPerOp, observed.BytesPerOp, observed.AllocsPerOp)
	}
	return errors.Join(failures...)
}

func loadBudgets(path string) (budgetFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return budgetFile{}, fmt.Errorf("read budget: %w", err)
	}
	var budgets budgetFile
	if err := json.Unmarshal(data, &budgets); err != nil {
		return budgetFile{}, fmt.Errorf("decode budget: %w", err)
	}
	if budgets.Version != 1 {
		return budgetFile{}, fmt.Errorf("unsupported budget version %d", budgets.Version)
	}
	if len(budgets.Benchmarks) == 0 {
		return budgetFile{}, errors.New("budget contains no benchmarks")
	}
	return budgets, nil
}

func parseBenchmarks(reader io.Reader) (map[string]measurement, error) {
	measurements := make(map[string]measurement)
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 4 || !strings.HasPrefix(fields[0], "Benchmark") {
			continue
		}
		name := cpuSuffix.ReplaceAllString(fields[0], "")
		observed := measurement{}
		for index := 2; index+1 < len(fields); index += 2 {
			value, err := strconv.ParseFloat(fields[index], 64)
			if err != nil {
				continue
			}
			switch fields[index+1] {
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
		measurements[name] = observed
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan benchmark output: %w", err)
	}
	if len(measurements) == 0 {
		return nil, errors.New("input contains no Go benchmark results")
	}
	return measurements, nil
}
