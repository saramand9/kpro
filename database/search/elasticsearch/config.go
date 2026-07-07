package elasticstore

import (
	"fmt"
	"net/http"
	"time"

	"github.com/elastic/go-elasticsearch/v8"
)

const defaultHealthTimeout = 5 * time.Second

// Config controls Elasticsearch adapter setup.
type Config struct {
	Name            string
	Addresses       []string
	Username        string
	Password        string
	APIKey          string
	ServiceToken    string
	Client          *elasticsearch.Client
	ClientConfig    *elasticsearch.Config
	Transport       http.RoundTripper
	SkipHealthCheck bool
	HealthTimeout   time.Duration
}

func normalizeConfig(config Config) (Config, error) {
	if config.Name == "" {
		config.Name = "elastic"
	}
	if config.HealthTimeout <= 0 {
		config.HealthTimeout = defaultHealthTimeout
	}
	if config.Client != nil {
		return config, nil
	}
	if config.ClientConfig == nil && len(config.Addresses) == 0 {
		return config, fmt.Errorf("%w: addresses or client is required", ErrInvalidConfig)
	}
	return config, nil
}

func elasticConfig(config Config) elasticsearch.Config {
	if config.ClientConfig != nil {
		return *config.ClientConfig
	}
	return elasticsearch.Config{
		Addresses:    config.Addresses,
		Username:     config.Username,
		Password:     config.Password,
		APIKey:       config.APIKey,
		ServiceToken: config.ServiceToken,
		Transport:    config.Transport,
	}
}
