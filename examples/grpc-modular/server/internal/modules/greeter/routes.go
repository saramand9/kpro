// File: examples/grpc-modular/server/internal/modules/greeter/routes.go
// Purpose: Provides runnable KPro example code.

package greeter

import (
	"github.com/saramand9/bignet/kpro/kprogrpc"
	"google.golang.org/grpc"
)

type Module struct{}

func (Module) RegisterGRPC(registrar grpc.ServiceRegistrar) {
	kprogrpc.RegisterGreeter(registrar, service{})
}
