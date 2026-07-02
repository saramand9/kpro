// File: examples/grpc/server/main.go
// Purpose: Provides runnable KPro example code.

package main

import (
	"context"
	"flag"
	"log"

	"github.com/saramand9/bignet/kpro/kprogrpc"
	"github.com/saramand9/bignet/kpro/server"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

type GreeterService struct {
	kprogrpc.UnimplementedGreeterServer
}

func (GreeterService) SayHello(_ context.Context, req *wrapperspb.StringValue) (*wrapperspb.StringValue, error) {
	return wrapperspb.String("hello " + req.Value), nil
}

func (GreeterService) SayGoodbye(_ context.Context, req *wrapperspb.StringValue) (*wrapperspb.StringValue, error) {
	return wrapperspb.String("goodbye " + req.Value), nil
}

func (GreeterService) GetNameLength(_ context.Context, req *wrapperspb.StringValue) (*wrapperspb.Int32Value, error) {
	return wrapperspb.Int32(int32(len(req.Value))), nil
}

func (GreeterService) EchoBytes(_ context.Context, req *wrapperspb.BytesValue) (*wrapperspb.BytesValue, error) {
	return wrapperspb.Bytes(append([]byte(nil), req.Value...)), nil
}

func main() {
	addr := flag.String("addr", ":9090", "listen address")
	flag.Parse()

	app := server.New()
	grpcEndpoint := app.GRPC(*addr)
	kprogrpc.RegisterGreeter(grpcEndpoint, GreeterService{})

	log.Printf("grpc server listening on %s", *addr)
	if err := app.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}
