// File: cmd/app/database.go
// Purpose: Wires application database resources for the KPro app server.

package main

import (
	"context"

	"github.com/saramand9/bignet/kpro/internal/app/persistence"
	userservice "github.com/saramand9/bignet/kpro/internal/modules/users/service"
	userstore "github.com/saramand9/bignet/kpro/internal/modules/users/store"
)

func initAppPersistence(ctx context.Context, dsn string) (*persistence.Resources, *userservice.Service, error) {
	resources, err := persistence.Open(ctx, persistence.Config{GORMDSN: dsn})
	if err != nil {
		return nil, nil, err
	}
	if resources.SQL == nil {
		return resources, nil, nil
	}
	if err := userstore.InitGORM(ctx, resources.SQL); err != nil {
		_ = resources.Close()
		return nil, nil, err
	}
	return resources, userservice.NewWithStore(userstore.NewGORM(resources.SQL)), nil
}
