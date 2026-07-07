package mongostore

import (
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

const defaultPingTimeout = 5 * time.Second

// Config controls MongoDB client adapter setup.
type Config struct {
	Name string

	URI      string
	Database string

	ClientOptions *options.ClientOptions
	Client        *mongo.Client

	PingTimeout   time.Duration
	SkipPingCheck bool
}

func normalizeConfig(config Config) (Config, error) {
	if config.Name == "" {
		config.Name = "mongo"
	}
	if config.Client != nil && (config.URI != "" || config.ClientOptions != nil) {
		return config, fmt.Errorf("%w: use Client or connection options, not both", ErrInvalidConfig)
	}
	if config.Client == nil && config.URI == "" && config.ClientOptions == nil {
		return config, fmt.Errorf("%w: missing URI, ClientOptions, or Client", ErrInvalidConfig)
	}
	if config.Database == "" {
		return config, fmt.Errorf("%w: missing Database", ErrInvalidConfig)
	}
	if config.PingTimeout <= 0 {
		config.PingTimeout = defaultPingTimeout
	}
	return config, nil
}

func mongoOptions(config Config) *options.ClientOptions {
	if config.ClientOptions != nil {
		return config.ClientOptions
	}
	return options.Client().ApplyURI(config.URI)
}
