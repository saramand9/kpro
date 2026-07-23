//go:build dbintegration

package gormstore

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

const integrationMigrationVersion int64 = 900001

type integrationProbe struct {
	ID    string `gorm:"column:id;primaryKey;size:64"`
	Value string `gorm:"column:probe_value;size:64;not null"`
}

func (integrationProbe) TableName() string {
	return "kpro_database_integration_probes"
}

func TestExternalDatabaseMigrations(t *testing.T) {
	tests := []struct {
		name      string
		dsnEnv    string
		dialector func(string) gorm.Dialector
	}{
		{name: "mysql", dsnEnv: "KPRO_TEST_MYSQL_DSN", dialector: mysql.Open},
		{name: "postgres", dsnEnv: "KPRO_TEST_POSTGRES_DSN", dialector: postgres.Open},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dsn := os.Getenv(test.dsnEnv)
			if dsn == "" {
				if os.Getenv("KPRO_REQUIRE_EXTERNAL_DB") == "1" {
					t.Fatalf("required environment variable %s is empty", test.dsnEnv)
				}
				t.Skipf("%s is not configured", test.dsnEnv)
			}

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			database, err := Open(ctx, Config{
				Name:         "integration-" + test.name,
				Dialector:    test.dialector(dsn),
				MaxOpenConns: 4,
				MaxIdleConns: 4,
			})
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := database.Close(); err != nil {
					t.Errorf("close database: %v", err)
				}
			}()

			migrations := []Migration{{
				Version: integrationMigrationVersion,
				Name:    "create database integration probe",
				Statements: []Statement{SQL(`CREATE TABLE IF NOT EXISTS kpro_database_integration_probes (
id VARCHAR(64) PRIMARY KEY,
probe_value VARCHAR(64) NOT NULL
)`)},
			}}
			if err := ApplyMigrations(ctx, database, migrations); err != nil {
				t.Fatal(err)
			}
			if err := ApplyMigrations(ctx, database, migrations); err != nil {
				t.Fatalf("idempotent migration: %v", err)
			}

			rollback := errors.New("rollback integration probe")
			err = database.GORM().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
				if err := tx.Create(&integrationProbe{ID: "rolled-back", Value: test.name}).Error; err != nil {
					return err
				}
				return rollback
			})
			if !errors.Is(err, rollback) {
				t.Fatalf("transaction error = %v, want %v", err, rollback)
			}
			var count int64
			if err := database.GORM().WithContext(ctx).
				Model(&integrationProbe{}).
				Where("id = ?", "rolled-back").
				Count(&count).Error; err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Fatalf("rolled-back row count = %d, want 0", count)
			}
		})
	}
}
