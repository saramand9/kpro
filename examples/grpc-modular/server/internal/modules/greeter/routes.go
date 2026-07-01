// File: examples/grpc-modular/server/internal/modules/greeter/routes.go
// Purpose: Provides runnable KPro example code.

package greeter

import (
	"github.com/saramand9/bignet/kpro/transport/grpcx"
	"google.golang.org/grpc"
)

type Module struct{}

func (Module) RegisterGRPC(registrar grpc.ServiceRegistrar) {
	grpcx.RegisterGreeter(registrar, service{})
}
