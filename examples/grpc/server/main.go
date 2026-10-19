// File: examples/grpc/server/main.go
// Purpose: Provides runnable KPro example code.

package main

import (
	"context"
	"flag"
	"log"
	"math"

	"github.com/saramand9/kpro/server"
	"github.com/saramand9/kpro/transport/grpcx"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

type GreeterService struct {
	grpcx.UnimplementedGreeterServer
}

func (GreeterService) SayHello(_ context.Context, req *wrapperspb.StringValue) (*wrapperspb.StringValue, error) {
	return wrapperspb.String("hello " + req.Value), nil
}

func (GreeterService) SayGoodbye(_ context.Context, req *wrapperspb.StringValue) (*wrapperspb.StringValue, error) {
	return wrapperspb.String("goodbye " + req.Value), nil
}

func (GreeterService) GetNameLength(_ context.Context, req *wrapperspb.StringValue) (*wrapperspb.Int32Value, error) {
	if len(req.Value) > math.MaxInt32 {
		return nil, status.Error(codes.ResourceExhausted, "name length exceeds int32")
	}
	return wrapperspb.Int32(int32(len(req.Value))), nil // #nosec G115 -- Nonnegative length is checked against MaxInt32 above.
}

func (GreeterService) EchoBytes(_ context.Context, req *wrapperspb.BytesValue) (*wrapperspb.BytesValue, error) {
	return wrapperspb.Bytes(append([]byte(nil), req.Value...)), nil
}

func main() {
	addr := flag.String("addr", ":9090", "listen address")
	flag.Parse()

	app := server.New()
	grpcEndpoint := grpcx.NewServer(*addr)
	app.MountRunner(grpcEndpoint)
	grpcx.RegisterGreeter(grpcEndpoint, GreeterService{})

	log.Printf("grpc server listening on %s", *addr)
	if err := app.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}
