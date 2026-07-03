// File: internal/modules/reports/handler/grpc.go
// Purpose: Provides gRPC DTO handlers for the reports module.

package handler

import (
	"context"
	"errors"

	reportservice "github.com/saramand9/bignet/kpro/internal/modules/reports/service"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"
)

const (
	// ReportBuildSummaryFullMethod is the full unary gRPC method name for reports.
	ReportBuildSummaryFullMethod = "/kpro.reports.v1.ReportService/BuildSummary"
)

// ReportGRPCClient is the client contract for the reports gRPC service.
type ReportGRPCClient interface {
	BuildSummary(ctx context.Context, in *structpb.Struct, opts ...grpc.CallOption) (*structpb.Struct, error)
}

type reportGRPCClient struct {
	cc grpc.ClientConnInterface
}

// NewReportGRPCClient creates a client for the reports gRPC service.
func NewReportGRPCClient(cc grpc.ClientConnInterface) ReportGRPCClient {
	return &reportGRPCClient{cc: cc}
}

func (c *reportGRPCClient) BuildSummary(ctx context.Context, in *structpb.Struct, opts ...grpc.CallOption) (*structpb.Struct, error) {
	out := new(structpb.Struct)
	if err := c.cc.Invoke(ctx, ReportBuildSummaryFullMethod, in, out, opts...); err != nil {
		return nil, err
	}
	return out, nil
}

// ReportGRPCServer is the server contract for the reports gRPC service.
type ReportGRPCServer interface {
	BuildSummary(context.Context, *structpb.Struct) (*structpb.Struct, error)
}

// UnimplementedReportGRPCServer can be embedded for forward-compatible services.
type UnimplementedReportGRPCServer struct{}

// BuildSummary returns an unimplemented status.
func (UnimplementedReportGRPCServer) BuildSummary(context.Context, *structpb.Struct) (*structpb.Struct, error) {
	return nil, status.Errorf(codes.Unimplemented, "method BuildSummary not implemented")
}

type grpcReportService struct {
	UnimplementedReportGRPCServer
	service *reportservice.Service
}

// RegisterGRPC exposes report routes on a gRPC registrar.
func RegisterGRPC(registrar grpc.ServiceRegistrar, service *reportservice.Service) {
	registrar.RegisterService(&ReportGRPCServiceDesc, grpcReportService{service: service})
}

func (s grpcReportService) BuildSummary(ctx context.Context, req *structpb.Struct) (*structpb.Struct, error) {
	if s.service == nil {
		return nil, status.Error(codes.FailedPrecondition, "report service is nil")
	}
	summary, err := s.service.BuildSummary(ctx, reportservice.BuildSummaryInput{
		Project: grpcStringField(req, "project"),
		Owner:   grpcStringField(req, "owner"),
	})
	if err != nil {
		return nil, grpcReportError(err)
	}
	resp, err := grpcSummaryToStruct(summary)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return resp, nil
}

func grpcStringField(req *structpb.Struct, name string) string {
	if req == nil {
		return ""
	}
	value := req.GetFields()[name]
	if value == nil {
		return ""
	}
	return value.GetStringValue()
}

func grpcSummaryToStruct(summary reportservice.Summary) (*structpb.Struct, error) {
	return structpb.NewStruct(map[string]any{
		"project": summary.Project,
		"owner":   summary.Owner,
		"title":   summary.Title,
		"status":  summary.Status,
		"items":   summary.Items,
	})
}

func grpcReportError(err error) error {
	switch {
	case errors.Is(err, reportservice.ErrProjectRequired):
		return status.Error(codes.InvalidArgument, "project is required")
	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, err.Error())
	case errors.Is(err, context.DeadlineExceeded):
		return status.Error(codes.DeadlineExceeded, err.Error())
	default:
		return status.Error(codes.Internal, err.Error())
	}
}

// ReportGRPCServiceDesc describes the reports gRPC service.
var ReportGRPCServiceDesc = grpc.ServiceDesc{
	ServiceName: "kpro.reports.v1.ReportService",
	HandlerType: (*ReportGRPCServer)(nil),
	Methods: []grpc.MethodDesc{
		{
			MethodName: "BuildSummary",
			Handler:    reportGRPCBuildSummaryHandler,
		},
	},
	Streams:  []grpc.StreamDesc{},
	Metadata: "reports.proto",
}

func reportGRPCBuildSummaryHandler(srv any, ctx context.Context, dec func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
	in := new(structpb.Struct)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(ReportGRPCServer).BuildSummary(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: ReportBuildSummaryFullMethod,
	}
	handler := func(ctx context.Context, req any) (any, error) {
		return srv.(ReportGRPCServer).BuildSummary(ctx, req.(*structpb.Struct))
	}
	return interceptor(ctx, in, info, handler)
}
