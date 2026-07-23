// Package healthx provides bounded liveness and readiness HTTP probes.
package healthx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"time"
)

const DefaultTimeout = 2 * time.Second

// CheckFunc returns nil while a dependency or runtime condition is healthy.
type CheckFunc func(context.Context) error

// Check names one independently reported health condition.
type Check struct {
	Name string
	Run  CheckFunc
}

// Config controls response deadlines and diagnostic detail.
type Config struct {
	Timeout      time.Duration
	ExposeErrors bool
}

// Handler evaluates checks concurrently and returns a stable JSON response.
type Handler struct {
	config Config
	checks []registeredCheck
}

type registeredCheck struct {
	Check
	running chan struct{}
}

type response struct {
	Status string        `json:"status"`
	Checks []checkResult `json:"checks,omitempty"`
}

type checkResult struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

// New validates and snapshots a set of health checks.
func New(checks []Check, config ...Config) (*Handler, error) {
	cfg := Config{Timeout: DefaultTimeout}
	if len(config) > 0 {
		cfg = config[0]
		if cfg.Timeout <= 0 {
			cfg.Timeout = DefaultTimeout
		}
	}
	seen := make(map[string]struct{}, len(checks))
	snapshot := append([]Check(nil), checks...)
	for _, check := range snapshot {
		if check.Name == "" {
			return nil, errors.New("health check name is required")
		}
		if check.Run == nil {
			return nil, fmt.Errorf("health check %q function is required", check.Name)
		}
		if _, duplicate := seen[check.Name]; duplicate {
			return nil, fmt.Errorf("duplicate health check %q", check.Name)
		}
		seen[check.Name] = struct{}{}
	}
	sort.Slice(snapshot, func(i, j int) bool { return snapshot[i].Name < snapshot[j].Name })
	registered := make([]registeredCheck, 0, len(snapshot))
	for _, check := range snapshot {
		registered = append(registered, registeredCheck{Check: check, running: make(chan struct{}, 1)})
	}
	return &Handler{config: cfg, checks: registered}, nil
}

// MustNew is New with panic-on-invalid-check semantics.
func MustNew(checks []Check, config ...Config) *Handler {
	handler, err := New(checks, config...)
	if err != nil {
		panic(err)
	}
	return handler
}

// ServeHTTP returns 200 for a passing probe and 503 when any check fails or
// exceeds the configured deadline.
func (h *Handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	ctx, cancel := context.WithTimeout(request.Context(), h.config.Timeout)
	defer cancel()
	resultChannel := make(chan checkResult, len(h.checks))
	for _, check := range h.checks {
		check := check
		select {
		case check.running <- struct{}{}:
		default:
			result := checkResult{Name: check.Name, Status: "fail"}
			if h.config.ExposeErrors {
				result.Error = "previous health check is still running"
			}
			resultChannel <- result
			continue
		}
		go func() {
			defer func() { <-check.running }()
			result := checkResult{Name: check.Name, Status: "pass"}
			if err := safeRun(ctx, check.Run); err != nil {
				result.Status = "fail"
				if h.config.ExposeErrors {
					result.Error = err.Error()
				}
			}
			resultChannel <- result
		}()
	}
	resultsByName := make(map[string]checkResult, len(h.checks))
	statusCode := http.StatusOK
	for len(resultsByName) < len(h.checks) {
		select {
		case result := <-resultChannel:
			resultsByName[result.Name] = result
			if result.Status != "pass" {
				statusCode = http.StatusServiceUnavailable
			}
		case <-ctx.Done():
			statusCode = http.StatusServiceUnavailable
			for _, check := range h.checks {
				if _, ok := resultsByName[check.Name]; ok {
					continue
				}
				result := checkResult{Name: check.Name, Status: "fail"}
				if h.config.ExposeErrors {
					result.Error = ctx.Err().Error()
				}
				resultsByName[check.Name] = result
			}
		}
	}
	results := make([]checkResult, 0, len(h.checks))
	for _, check := range h.checks {
		results = append(results, resultsByName[check.Name])
	}
	status := "pass"
	if statusCode != http.StatusOK {
		status = "fail"
	}
	writer.Header().Set("Content-Type", "application/health+json; charset=utf-8")
	writer.WriteHeader(statusCode)
	_ = json.NewEncoder(writer).Encode(response{Status: status, Checks: results})
}

func safeRun(ctx context.Context, check CheckFunc) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("health check panic: %v", recovered)
		}
	}()
	return check(ctx)
}
