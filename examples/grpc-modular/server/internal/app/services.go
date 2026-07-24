// File: examples/grpc-modular/server/internal/app/services.go
// Purpose: Provides runnable KPro example code.

package app

import (
	"github.com/saramand9/kpro/examples/grpc-modular/server/internal/modules/greeter"
	"google.golang.org/grpc"
)

type GRPCModule interface {
	RegisterGRPC(grpc.ServiceRegistrar)
}

func RegisterServices(registrar grpc.ServiceRegistrar) {
	registerGRPC(registrar, greeter.Module{})
}

func registerGRPC(registrar grpc.ServiceRegistrar, modules ...GRPCModule) {
	for _, module := range modules {
		module.RegisterGRPC(registrar)
	}
}
