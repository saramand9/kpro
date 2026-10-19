// File: examples/grpc-modular/server/internal/modules/greeter/handler.go
// Purpose: Provides runnable KPro example code.

package greeter

import (
	"context"
	"math"

	"github.com/saramand9/kpro/transport/grpcx"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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
	if len(req.Value) > math.MaxInt32 {
		return nil, status.Error(codes.ResourceExhausted, "name length exceeds int32")
	}
	return wrapperspb.Int32(int32(len(req.Value))), nil // #nosec G115 -- Nonnegative length is checked against MaxInt32 above.
}

func (service) EchoBytes(_ context.Context, req *wrapperspb.BytesValue) (*wrapperspb.BytesValue, error) {
	return wrapperspb.Bytes(append([]byte(nil), req.Value...)), nil
}
