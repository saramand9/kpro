package neo4jstore

import (
	"fmt"
	"time"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

const defaultVerifyTimeout = 5 * time.Second

// Config controls Neo4j driver setup.
type Config struct {
	Name string

	URI      string
	Username string
	Password string
	Realm    string
	Database string

	Driver neo4j.DriverWithContext
	Config []func(*neo4j.Config)

	VerifyTimeout        time.Duration
	SkipVerifyConnection bool
}

func normalizeConfig(config Config) (Config, error) {
	if config.Name == "" {
		config.Name = "neo4j"
	}
	if config.VerifyTimeout <= 0 {
		config.VerifyTimeout = defaultVerifyTimeout
	}
	if config.Driver != nil && (config.URI != "" || config.Username != "" || config.Password != "" || config.Realm != "" || len(config.Config) > 0) {
		return config, fmt.Errorf("%w: use Driver or connection options, not both", ErrInvalidConfig)
	}
	if config.Driver == nil && config.URI == "" {
		return config, fmt.Errorf("%w: uri or Driver is required", ErrInvalidConfig)
	}
	return config, nil
}

func authToken(config Config) neo4j.AuthToken {
	if config.Username == "" && config.Password == "" {
		return neo4j.NoAuth()
	}
	return neo4j.BasicAuth(config.Username, config.Password, config.Realm)
}
