package qdrantstore

import (
	"fmt"
	"net/http"
	"strings"
	"time"
)

const defaultHealthTimeout = 5 * time.Second

// Config controls Qdrant HTTP adapter setup.
type Config struct {
	Name            string
	URL             string
	APIKey          string
	Header          http.Header
	HTTPClient      *http.Client
	SkipHealthCheck bool
	HealthTimeout   time.Duration
}

func normalizeConfig(config Config) (Config, error) {
	if config.Name == "" {
		config.Name = "qdrant"
	}
	config.URL = strings.TrimRight(strings.TrimSpace(config.URL), "/")
	if config.HealthTimeout <= 0 {
		config.HealthTimeout = defaultHealthTimeout
	}
	if config.HTTPClient == nil {
		config.HTTPClient = http.DefaultClient
	}
	if config.URL == "" {
		return config, fmt.Errorf("%w: url is required", ErrInvalidConfig)
	}
	return config, nil
}
