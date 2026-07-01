package kprogrpc

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

const (
	GreeterSayHelloFullMethod      = "/helloworld.v1.Greeter/SayHello"
	GreeterSayGoodbyeFullMethod    = "/helloworld.v1.Greeter/SayGoodbye"
	GreeterGetNameLengthFullMethod = "/helloworld.v1.Greeter/GetNameLength"
	GreeterEchoBytesFullMethod     = "/helloworld.v1.Greeter/EchoBytes"
)

// This file is the first generated-style service binding. A future
// protoc-gen-kpro should emit the same shape for user-defined services.
type GreeterClient interface {
	SayHello(ctx context.Context, in *wrapperspb.StringValue, opts ...grpc.CallOption) (*wrapperspb.StringValue, error)
	SayGoodbye(ctx context.Context, in *wrapperspb.StringValue, opts ...grpc.CallOption) (*wrapperspb.StringValue, error)
	GetNameLength(ctx context.Context, in *wrapperspb.StringValue, opts ...grpc.CallOption) (*wrapperspb.Int32Value, error)
	EchoBytes(ctx context.Context, in *wrapperspb.BytesValue, opts ...grpc.CallOption) (*wrapperspb.BytesValue, error)
}

type greeterClient struct {
	cc grpc.ClientConnInterface
}

func NewGreeterClient(cc grpc.ClientConnInterface) GreeterClient {
	return &greeterClient{cc: cc}
}

func (c *greeterClient) SayHello(ctx context.Context, in *wrapperspb.StringValue, opts ...grpc.CallOption) (*wrapperspb.StringValue, error) {
	out := new(wrapperspb.StringValue)
	if err := c.cc.Invoke(ctx, GreeterSayHelloFullMethod, in, out, opts...); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *greeterClient) SayGoodbye(ctx context.Context, in *wrapperspb.StringValue, opts ...grpc.CallOption) (*wrapperspb.StringValue, error) {
	out := new(wrapperspb.StringValue)
	if err := c.cc.Invoke(ctx, GreeterSayGoodbyeFullMethod, in, out, opts...); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *greeterClient) GetNameLength(ctx context.Context, in *wrapperspb.StringValue, opts ...grpc.CallOption) (*wrapperspb.Int32Value, error) {
	out := new(wrapperspb.Int32Value)
	if err := c.cc.Invoke(ctx, GreeterGetNameLengthFullMethod, in, out, opts...); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *greeterClient) EchoBytes(ctx context.Context, in *wrapperspb.BytesValue, opts ...grpc.CallOption) (*wrapperspb.BytesValue, error) {
	out := new(wrapperspb.BytesValue)
	if err := c.cc.Invoke(ctx, GreeterEchoBytesFullMethod, in, out, opts...); err != nil {
		return nil, err
	}
	return out, nil
}

type GreeterServer interface {
	SayHello(context.Context, *wrapperspb.StringValue) (*wrapperspb.StringValue, error)
	SayGoodbye(context.Context, *wrapperspb.StringValue) (*wrapperspb.StringValue, error)
	GetNameLength(context.Context, *wrapperspb.StringValue) (*wrapperspb.Int32Value, error)
	EchoBytes(context.Context, *wrapperspb.BytesValue) (*wrapperspb.BytesValue, error)
}

type UnimplementedGreeterServer struct{}

func (UnimplementedGreeterServer) SayHello(context.Context, *wrapperspb.StringValue) (*wrapperspb.StringValue, error) {
	return nil, status.Errorf(codes.Unimplemented, "method SayHello not implemented")
}

func (UnimplementedGreeterServer) SayGoodbye(context.Context, *wrapperspb.StringValue) (*wrapperspb.StringValue, error) {
	return nil, status.Errorf(codes.Unimplemented, "method SayGoodbye not implemented")
}

func (UnimplementedGreeterServer) GetNameLength(context.Context, *wrapperspb.StringValue) (*wrapperspb.Int32Value, error) {
	return nil, status.Errorf(codes.Unimplemented, "method GetNameLength not implemented")
}

func (UnimplementedGreeterServer) EchoBytes(context.Context, *wrapperspb.BytesValue) (*wrapperspb.BytesValue, error) {
	return nil, status.Errorf(codes.Unimplemented, "method EchoBytes not implemented")
}

func RegisterGreeter(s grpc.ServiceRegistrar, srv GreeterServer) {
	s.RegisterService(&GreeterServiceDesc, srv)
}

var GreeterServiceDesc = grpc.ServiceDesc{
	ServiceName: "helloworld.v1.Greeter",
	HandlerType: (*GreeterServer)(nil),
	Methods: []grpc.MethodDesc{
		{
			MethodName: "SayHello",
			Handler:    greeterSayHelloHandler,
		},
		{
			MethodName: "SayGoodbye",
			Handler:    greeterSayGoodbyeHandler,
		},
		{
			MethodName: "GetNameLength",
			Handler:    greeterGetNameLengthHandler,
		},
		{
			MethodName: "EchoBytes",
			Handler:    greeterEchoBytesHandler,
		},
	},
	Streams:  []grpc.StreamDesc{},
	Metadata: "helloworld.proto",
}

func greeterSayHelloHandler(srv any, ctx context.Context, dec func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
	in := new(wrapperspb.StringValue)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(GreeterServer).SayHello(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: GreeterSayHelloFullMethod,
	}
	handler := func(ctx context.Context, req any) (any, error) {
		return srv.(GreeterServer).SayHello(ctx, req.(*wrapperspb.StringValue))
	}
	return interceptor(ctx, in, info, handler)
}

func greeterSayGoodbyeHandler(srv any, ctx context.Context, dec func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
	in := new(wrapperspb.StringValue)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(GreeterServer).SayGoodbye(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: GreeterSayGoodbyeFullMethod,
	}
	handler := func(ctx context.Context, req any) (any, error) {
		return srv.(GreeterServer).SayGoodbye(ctx, req.(*wrapperspb.StringValue))
	}
	return interceptor(ctx, in, info, handler)
}

func greeterGetNameLengthHandler(srv any, ctx context.Context, dec func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
	in := new(wrapperspb.StringValue)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(GreeterServer).GetNameLength(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: GreeterGetNameLengthFullMethod,
	}
	handler := func(ctx context.Context, req any) (any, error) {
		return srv.(GreeterServer).GetNameLength(ctx, req.(*wrapperspb.StringValue))
	}
	return interceptor(ctx, in, info, handler)
}

func greeterEchoBytesHandler(srv any, ctx context.Context, dec func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
	in := new(wrapperspb.BytesValue)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(GreeterServer).EchoBytes(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: GreeterEchoBytesFullMethod,
	}
	handler := func(ctx context.Context, req any) (any, error) {
		return srv.(GreeterServer).EchoBytes(ctx, req.(*wrapperspb.BytesValue))
	}
	return interceptor(ctx, in, info, handler)
}
