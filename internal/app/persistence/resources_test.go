package persistence

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/saramand9/kpro/database/common/lifecycle"
	gormstore "github.com/saramand9/kpro/database/gorm"
)

func TestSQLConfigWithDefaults(t *testing.T) {
	tests := []struct {
		name string
		in   SQLConfig
		want SQLConfig
	}{
		{
			name: "sqlite",
			in:   SQLConfig{Driver: "sqlite", DSN: ":memory:"},
			want: SQLConfig{
				Driver:       "sqlite",
				DSN:          ":memory:",
				MaxOpenConns: defaultSQLiteMaxOpenConns,
				MaxIdleConns: defaultSQLiteMaxIdleConns,
			},
		},
		{
			name: "mysql",
			in:   SQLConfig{Driver: "mysql", DSN: "dsn"},
			want: SQLConfig{
				Driver:          "mysql",
				DSN:             "dsn",
				MaxOpenConns:    defaultNetworkMaxOpenConns,
				MaxIdleConns:    defaultNetworkMaxIdleConns,
				ConnMaxLifetime: defaultNetworkConnMaxLifetime,
				ConnMaxIdleTime: defaultNetworkConnMaxIdleTime,
			},
		},
		{
			name: "network override and idle clamp",
			in: SQLConfig{
				Driver:          "postgres",
				DSN:             "dsn",
				MaxOpenConns:    4,
				ConnMaxLifetime: time.Hour,
				ConnMaxIdleTime: 10 * time.Minute,
			},
			want: SQLConfig{
				Driver:          "postgres",
				DSN:             "dsn",
				MaxOpenConns:    4,
				MaxIdleConns:    4,
				ConnMaxLifetime: time.Hour,
				ConnMaxIdleTime: 10 * time.Minute,
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.in.WithDefaults(); got != test.want {
				t.Fatalf("WithDefaults() = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestOpenAppliesSQLitePoolDefaultsAndOverrides(t *testing.T) {
	for _, test := range []struct {
		name        string
		maxOpen     int
		maxIdle     int
		wantMaxOpen int
	}{
		{name: "default", wantMaxOpen: defaultSQLiteMaxOpenConns},
		{name: "override", maxOpen: 3, maxIdle: 2, wantMaxOpen: 3},
	} {
		t.Run(test.name, func(t *testing.T) {
			resources, err := Open(context.Background(), Config{SQL: SQLConfig{
				Driver:       "sqlite",
				DSN:          ":memory:",
				MaxOpenConns: test.maxOpen,
				MaxIdleConns: test.maxIdle,
			}})
			if err != nil {
				t.Fatal(err)
			}
			defer resources.Close()
			sqlDB, err := resources.SQL.SQLDB()
			if err != nil {
				t.Fatal(err)
			}
			if got := sqlDB.Stats().MaxOpenConnections; got != test.wantMaxOpen {
				t.Fatalf("MaxOpenConnections = %d, want %d", got, test.wantMaxOpen)
			}
			health := resources.Health(context.Background())
			if len(health) != 1 || health[0].Status != lifecycle.StatusOK {
				t.Fatalf("health = %#v, want one healthy SQL resource", health)
			}
		})
	}
}

func TestOpenRejectsInvalidPoolConfiguration(t *testing.T) {
	for _, test := range []struct {
		name   string
		config SQLConfig
	}{
		{name: "negative max open", config: SQLConfig{MaxOpenConns: -1}},
		{name: "negative max idle", config: SQLConfig{MaxIdleConns: -1}},
		{name: "negative max lifetime", config: SQLConfig{ConnMaxLifetime: -1}},
		{name: "negative max idle time", config: SQLConfig{ConnMaxIdleTime: -1}},
		{name: "idle exceeds open", config: SQLConfig{MaxOpenConns: 2, MaxIdleConns: 3}},
	} {
		t.Run(test.name, func(t *testing.T) {
			test.config.Driver = "sqlite"
			test.config.DSN = ":memory:"
			_, err := Open(context.Background(), Config{SQL: test.config})
			if !errors.Is(err, gormstore.ErrInvalidConfig) {
				t.Fatalf("Open() error = %v, want %v", err, gormstore.ErrInvalidConfig)
			}
		})
	}
}

func TestResourcesHealthHonorsCanceledContext(t *testing.T) {
	resources, err := Open(context.Background(), Config{SQL: SQLConfig{
		Driver: "sqlite",
		DSN:    ":memory:",
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer resources.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	health := resources.Health(ctx)
	if len(health) != 1 || health[0].Status != lifecycle.StatusDown {
		t.Fatalf("health with canceled context = %#v, want one down SQL resource", health)
	}
}
