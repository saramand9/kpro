// File: examples/grpc-modular/server/internal/modules/greeter/handler.go
// Purpose: Provides runnable KPro example code.

package greeter

import (
	"context"

	"github.com/saramand9/kpro/transport/grpcx"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

type service struct {
	grpcx.UnimplementedGreeterServer
}

func (service) SayHello(_ context.Context, req *wrapperspb.StringValue) (*wrapperspb.StringValue, error) {
	return wrapperspb.String("hello " + req.Value), nil
}

func (service) SayGoodbye(_ context.Context, req *wrapperspb.StringValue) (*wrapperspb.StringValue, error) {
	return wrapperspb.String("goodbye " + req.Value), nil
}

func (service) GetNameLength(_ context.Context, req *wrapperspb.StringValue) (*wrapperspb.Int32Value, error) {
	return wrapperspb.Int32(int32(len(req.Value))), nil
}

func (service) EchoBytes(_ context.Context, req *wrapperspb.BytesValue) (*wrapperspb.BytesValue, error) {
	return wrapperspb.Bytes(append([]byte(nil), req.Value...)), nil
}
