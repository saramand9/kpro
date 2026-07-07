package influxstore

import (
	"fmt"
	"time"

	influxdb2 "github.com/influxdata/influxdb-client-go/v2"
)

const defaultHealthTimeout = 5 * time.Second

// Config controls InfluxDB client adapter setup.
type Config struct {
	Name string

	URL    string
	Token  string
	Org    string
	Bucket string

	Options *influxdb2.Options
	Client  influxdb2.Client

	HealthTimeout   time.Duration
	SkipHealthCheck bool
}

func normalizeConfig(config Config) (Config, error) {
	if config.Name == "" {
		config.Name = "influx"
	}
	if config.Client != nil && (config.URL != "" || config.Token != "" || config.Options != nil) {
		return config, fmt.Errorf("%w: use Client or connection options, not both", ErrInvalidConfig)
	}
	if config.Client == nil && config.URL == "" {
		return config, fmt.Errorf("%w: url or Client is required", ErrInvalidConfig)
	}
	if config.HealthTimeout <= 0 {
		config.HealthTimeout = defaultHealthTimeout
	}
	return config, nil
}
