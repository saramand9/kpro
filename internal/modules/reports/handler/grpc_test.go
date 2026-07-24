// File: internal/modules/reports/handler/grpc_test.go
// Purpose: Tests the reports gRPC handler.

package handler

import (
	"context"
	"net"
	"testing"
	"time"

	reportservice "github.com/saramand9/kpro/internal/modules/reports/service"
	"github.com/saramand9/kpro/transport/grpcx"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestGRPCReportBuildSummarySuccessAndFailure(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	srv := grpcx.NewServer(ln.Addr().String())
	RegisterGRPC(srv, reportservice.New(reportservice.Config{DefaultOwner: "platform"}))
	go func() {
		_ = srv.ServeListener(ln)
	}()
	defer srv.Stop()

	conn := dialReportGRPC(t, ln.Addr().String())
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	client := NewReportGRPCClient(conn)

	req := mustStruct(t, map[string]any{
		"project": "apollo",
		"owner":   "Ada",
	})
	resp, err := client.BuildSummary(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	fields := resp.GetFields()
	if got := fields["project"].GetStringValue(); got != "apollo" {
		t.Fatalf("project = %q, want apollo", got)
	}
	if got := fields["owner"].GetStringValue(); got != "Ada" {
		t.Fatalf("owner = %q, want Ada", got)
	}
	if got := fields["status"].GetStringValue(); got != "ready" {
		t.Fatalf("status = %q, want ready", got)
	}
	if got := fields["items"].GetNumberValue(); got != 2 {
		t.Fatalf("items = %v, want 2", got)
	}

	badReq := mustStruct(t, map[string]any{"owner": "Ada"})
	_, err = client.BuildSummary(ctx, badReq)
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("error code = %s, want %s: %v", status.Code(err), codes.InvalidArgument, err)
	}
}

func dialReportGRPC(t *testing.T, addr string) *grpc.ClientConn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, err := grpc.DialContext(ctx, addr, grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithBlock())
	if err != nil {
		t.Fatal(err)
	}
	return conn
}

func mustStruct(t *testing.T, values map[string]any) *structpb.Struct {
	t.Helper()
	msg, err := structpb.NewStruct(values)
	if err != nil {
		t.Fatal(err)
	}
	return msg
}
