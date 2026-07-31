package protogen

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/saramand9/kpro/internal/codegen/manifest"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
)

func TestParseDescriptorSetExtractsOnlySelectedSource(t *testing.T) {
	target := &descriptorpb.FileDescriptorProto{
		Name:    proto.String("api/service.proto"),
		Package: proto.String("example.orders.v1"),
		Service: []*descriptorpb.ServiceDescriptorProto{
			{
				Name: proto.String("Orders"),
				Method: []*descriptorpb.MethodDescriptorProto{
					{
						Name:       proto.String("Get"),
						InputType:  proto.String(".example.orders.v1.GetRequest"),
						OutputType: proto.String(".example.orders.v1.GetResponse"),
					},
					{
						Name:            proto.String("Watch"),
						InputType:       proto.String(".example.orders.v1.WatchRequest"),
						OutputType:      proto.String(".example.orders.v1.WatchResponse"),
						ClientStreaming: proto.Bool(true),
						ServerStreaming: proto.Bool(true),
					},
				},
			},
			{Name: proto.String("Admin")},
		},
	}
	imported := &descriptorpb.FileDescriptorProto{
		Name:    proto.String("shared/types.proto"),
		Package: proto.String("example.shared.v1"),
		Service: []*descriptorpb.ServiceDescriptorProto{{Name: proto.String("Ignored")}},
	}
	data, err := proto.Marshal(&descriptorpb.FileDescriptorSet{
		File: []*descriptorpb.FileDescriptorProto{imported, target},
	})
	if err != nil {
		t.Fatal(err)
	}

	got, err := ParseDescriptorSet(data, "api/service.proto")
	if err != nil {
		t.Fatal(err)
	}
	want := Contract{
		Path:    "api/service.proto",
		Package: "example.orders.v1",
		Services: []Service{
			{
				Name:     "Orders",
				FullName: "example.orders.v1.Orders",
				Methods: []Method{
					{Name: "Get", Input: "example.orders.v1.GetRequest", Output: "example.orders.v1.GetResponse"},
					{
						Name:            "Watch",
						Input:           "example.orders.v1.WatchRequest",
						Output:          "example.orders.v1.WatchResponse",
						ClientStreaming: true,
						ServerStreaming: true,
					},
				},
			},
			{Name: "Admin", FullName: "example.orders.v1.Admin"},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseDescriptorSet() = %#v, want %#v", got, want)
	}
}

func TestParseDescriptorSetRejectsMissingAndMalformedSources(t *testing.T) {
	if _, err := ParseDescriptorSet([]byte("not protobuf"), "api/service.proto"); err == nil || !strings.Contains(err.Error(), "decode descriptor set") {
		t.Fatalf("malformed descriptor error = %v", err)
	}

	data, err := proto.Marshal(&descriptorpb.FileDescriptorSet{
		File: []*descriptorpb.FileDescriptorProto{{Name: proto.String("other.proto")}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseDescriptorSet(data, "api/service.proto"); err == nil || !strings.Contains(err.Error(), "does not contain source") {
		t.Fatalf("missing source error = %v", err)
	}
}

func TestInspectorUsesOnlyProtocAndLeavesProjectUntouched(t *testing.T) {
	root, value, source := descriptorTestProject(t)
	var lookedUp []string
	var temporary string
	var inputTemporary string
	inspector := Inspector{
		Root: root,
		LookupTool: func(name string) (string, error) {
			lookedUp = append(lookedUp, name)
			return filepath.Join(root, "tools", name+".exe"), nil
		},
		Runner: runnerFunc(func(_ context.Context, command Command) ([]byte, error) {
			temporary = command.Dir
			if len(command.Args) == 0 || !strings.HasPrefix(command.Args[0], "--proto_path=") {
				t.Fatalf("descriptor args = %v, want isolated proto path", command.Args)
			}
			inputTemporary = strings.TrimPrefix(command.Args[0], "--proto_path=")
			wantArgs := []string{
				"--proto_path=" + inputTemporary,
				"--descriptor_set_out=" + descriptorFilename,
				"--include_imports",
				source.Path,
			}
			if !reflect.DeepEqual(command.Args, wantArgs) {
				t.Fatalf("descriptor args = %v, want %v", command.Args, wantArgs)
			}
			if filepath.Clean(inputTemporary) == filepath.Clean(mustResolvedRoot(t, root)) ||
				filepath.Clean(inputTemporary) == filepath.Clean(command.Dir) {
				t.Fatalf("descriptor proto path = %q, want isolated input snapshot", inputTemporary)
			}
			writeDescriptorContract(t, command.Dir, source.Path)
			return nil, nil
		}),
	}

	contract, err := inspector.Inspect(value, source)
	if err != nil {
		t.Fatal(err)
	}
	if len(contract.Services) != 1 || len(contract.Services[0].Methods) != 1 {
		t.Fatalf("inspected contract = %#v", contract)
	}
	if !reflect.DeepEqual(lookedUp, []string{"protoc"}) {
		t.Fatalf("looked up tools = %v, want only protoc", lookedUp)
	}
	if _, err := os.Stat(temporary); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary descriptor directory remains: %v", err)
	}
	if _, err := os.Stat(inputTemporary); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary input directory remains: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, descriptorFilename)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("inspector wrote into project: %v", err)
	}
}

func TestInspectorPreservesProtocDiagnostics(t *testing.T) {
	root, value, source := descriptorTestProject(t)
	inspector := Inspector{
		Root: root,
		LookupTool: func(name string) (string, error) {
			if name != "protoc" {
				t.Fatalf("unexpected tool lookup %q", name)
			}
			return filepath.Join(root, "tools", "protoc.exe"), nil
		},
		Runner: runnerFunc(func(context.Context, Command) ([]byte, error) {
			return []byte("api/service.proto:7: unknown type"), errors.New("exit status 1")
		}),
	}

	_, err := inspector.Inspect(value, source)
	if err == nil || !strings.Contains(err.Error(), "protoc failed") || !strings.Contains(err.Error(), "unknown type") {
		t.Fatalf("Inspect() error = %v", err)
	}
}

func TestInspectorWithInstalledProtoc(t *testing.T) {
	if _, err := exec.LookPath("protoc"); err != nil {
		t.Skipf("optional real-protoc smoke test: protoc is unavailable: %v", err)
	}
	root, value, source := descriptorTestProject(t)
	before := descriptorProjectFiles(t, root)
	contract, err := (Inspector{Root: root}).Inspect(value, source)
	if err != nil {
		t.Fatalf("real protoc Inspect() error = %v", err)
	}
	if len(contract.Services) != 1 || contract.Services[0].FullName != "example.orders.v1.Orders" {
		t.Fatalf("real protoc contract = %#v", contract)
	}
	if after := descriptorProjectFiles(t, root); !reflect.DeepEqual(after, before) {
		t.Fatalf("real protoc changed project files: before=%v after=%v", before, after)
	}
}

func descriptorTestProject(t *testing.T) (string, manifest.Manifest, manifest.Source) {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "api"), 0o755); err != nil {
		t.Fatal(err)
	}
	source := manifest.Source{
		ID:      "grpc-api",
		Kind:    manifest.KindProto,
		Path:    "api/service.proto",
		Package: "internal/grpcapi",
	}
	value := manifest.Manifest{
		APIVersion: manifest.APIVersionV1Alpha1,
		Module:     "example.com/orders",
		Service:    "orders",
		Sources:    []manifest.Source{source},
	}
	if err := os.WriteFile(
		filepath.Join(root, "api", "service.proto"),
		[]byte("syntax = \"proto3\"; package example.orders.v1; service Orders {}"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	return root, value, source
}

func writeDescriptorContract(t *testing.T, root, sourcePath string) {
	t.Helper()
	data, err := proto.Marshal(&descriptorpb.FileDescriptorSet{
		File: []*descriptorpb.FileDescriptorProto{{
			Name:    proto.String(sourcePath),
			Package: proto.String("example.orders.v1"),
			Service: []*descriptorpb.ServiceDescriptorProto{{
				Name: proto.String("Orders"),
				Method: []*descriptorpb.MethodDescriptorProto{{
					Name:       proto.String("Get"),
					InputType:  proto.String(".example.orders.v1.GetRequest"),
					OutputType: proto.String(".example.orders.v1.GetResponse"),
				}},
			}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, descriptorFilename), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustResolvedRoot(t *testing.T, root string) string {
	t.Helper()
	resolved, err := resolveRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func descriptorProjectFiles(t *testing.T, root string) map[string]string {
	t.Helper()
	result := make(map[string]string)
	err := filepath.WalkDir(root, func(filename string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(root, filename)
		if err != nil {
			return err
		}
		content, err := os.ReadFile(filename)
		if err != nil {
			return err
		}
		result[filepath.ToSlash(relative)] = string(content)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}
