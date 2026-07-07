package redisstore

import (
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

const defaultPingTimeout = 5 * time.Second

// Config controls Redis adapter setup.
type Config struct {
	Name        string
	Addr        string
	Username    string
	Password    string
	DB          int
	Client      *redis.Client
	PingTimeout time.Duration
	KeyPrefix   string
}

func normalizeConfig(config Config) (Config, error) {
	if config.Name == "" {
		config.Name = "redis"
	}
	config.KeyPrefix = strings.TrimSpace(config.KeyPrefix)
	if config.PingTimeout <= 0 {
		config.PingTimeout = defaultPingTimeout
	}
	if config.Client == nil && strings.TrimSpace(config.Addr) == "" {
		return config, fmt.Errorf("%w: addr or client is required", ErrInvalidConfig)
	}
	return config, nil
}

func redisOptions(config Config) *redis.Options {
	return &redis.Options{
		Addr:     config.Addr,
		Username: config.Username,
		Password: config.Password,
		DB:       config.DB,
	}
}
