// File: cmd/app/database.go
// Purpose: Wires application database resources for the KPro app server.

package main

import (
	"context"

	"github.com/saramand9/bignet/kpro/internal/app/persistence"
	commerceservice "github.com/saramand9/bignet/kpro/internal/modules/commerce/service"
	commercestore "github.com/saramand9/bignet/kpro/internal/modules/commerce/store"
	projectservice "github.com/saramand9/bignet/kpro/internal/modules/projects/service"
	projectstore "github.com/saramand9/bignet/kpro/internal/modules/projects/store"
	userservice "github.com/saramand9/bignet/kpro/internal/modules/users/service"
	userstore "github.com/saramand9/bignet/kpro/internal/modules/users/store"
)

type appServices struct {
	Users    *userservice.Service
	Projects *projectservice.Service
	Commerce *commerceservice.Service
}

func initAppPersistence(ctx context.Context, config persistence.Config) (*persistence.Resources, appServices, error) {
	resources, err := persistence.Open(ctx, config)
	if err != nil {
		return nil, appServices{}, err
	}
	if resources.SQL == nil {
		return resources, appServices{}, nil
	}
	if err := userstore.InitGORM(ctx, resources.SQL); err != nil {
		_ = resources.Close()
		return nil, appServices{}, err
	}
	if err := projectstore.InitGORM(ctx, resources.SQL); err != nil {
		_ = resources.Close()
		return nil, appServices{}, err
	}
	if err := commercestore.InitGORM(ctx, resources.SQL); err != nil {
		_ = resources.Close()
		return nil, appServices{}, err
	}
	return resources, appServices{
		Users:    userservice.NewWithStore(userstore.NewGORM(resources.SQL)),
		Projects: projectservice.NewWithStore(projectstore.NewGORM(resources.SQL)),
		Commerce: commerceservice.NewWithStore(commercestore.NewGORM(resources.SQL)),
	}, nil
}
