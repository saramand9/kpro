// File: cmd/app/database.go
// Purpose: Wires application database resources for the KPro app server.

package main

import (
	"context"
	"strings"

	gormstore "github.com/saramand9/bignet/kpro/database/gorm"
	"github.com/saramand9/bignet/kpro/internal/modules/users"
	userservice "github.com/saramand9/bignet/kpro/internal/modules/users/service"
	userstore "github.com/saramand9/bignet/kpro/internal/modules/users/store"
	"gorm.io/driver/sqlite"

	_ "modernc.org/sqlite"
)

func initAppPersistence(ctx context.Context, dsn string) (*gormstore.Database, *userservice.Service, error) {
	if strings.TrimSpace(dsn) == "" {
		return nil, nil, nil
	}
	database, err := gormstore.Open(ctx, gormstore.Config{
		Name:         "app",
		Dialector:    sqlite.Dialector{DriverName: "sqlite", DSN: dsn},
		MaxOpenConns: 1,
	})
	if err != nil {
		return nil, nil, err
	}
	if err := users.Init(ctx, database); err != nil {
		_ = database.Shutdown(ctx)
		return nil, nil, err
	}
	return database, userservice.NewWithStore(userstore.NewGORM(database)), nil
}
