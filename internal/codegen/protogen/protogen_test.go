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

	"github.com/saramand9/kpro/internal/codegen"
	"github.com/saramand9/kpro/internal/codegen/manifest"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
)

func TestGeneratorReportsEachMissingTool(t *testing.T) {
	root, value, source := testProject(t)
	for _, missing := range []string{"protoc", "protoc-gen-go", "protoc-gen-go-grpc"} {
		t.Run(missing, func(t *testing.T) {
			called := false
			generator := Generator{
				Root: root,
				Runner: runnerFunc(func(context.Context, Command) ([]byte, error) {
					called = true
					return nil, nil
				}),
				LookupTool: func(name string) (string, error) {
					if name == missing {
						return "", errors.New("not in PATH")
					}
					return filepath.Join(root, "tools", name), nil
				},
			}
			_, err := generator.Plan(value, source)
			if err == nil || !strings.Contains(err.Error(), `required tool "`+missing+`" was not found`) {
				t.Fatalf("Plan() error = %v, want missing %s diagnostic", err, missing)
			}
			if called {
				t.Fatal("runner was called after tool preflight failed")
			}
		})
	}
}

func TestGeneratorBuildsDeterministicStandardAndKProPlan(t *testing.T) {
	root, value, source := testProject(t)
	var commands []Command
	runner := runnerFunc(func(_ context.Context, command Command) ([]byte, error) {
		commands = append(commands, command)
		writeFakeProtocOutput(t, command.Dir, source.Path, source.Package)
		return nil, nil
	})
	lookup := func(name string) (string, error) {
		return filepath.Join(root, "tools", name+".exe"), nil
	}
	generator := Generator{Root: root, Runner: runner, LookupTool: lookup}

	first, err := generator.Plan(value, source)
	if err != nil {
		t.Fatalf("first Plan() error = %v", err)
	}
	second, err := generator.Plan(value, source)
	if err != nil {
		t.Fatalf("second Plan() error = %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatal("Plan() output changed between identical invocations")
	}
	if len(commands) != 2 {
		t.Fatalf("runner commands = %d, want 2", len(commands))
	}
	if !reflect.DeepEqual(commands[0].Args, commands[1].Args) {
		t.Fatalf("protoc args are not deterministic:\n%q\n%q", commands[0].Args, commands[1].Args)
	}
	assertCommand(t, commands[0], root, value, source, lookup)

	wantPaths := []string{
		"internal/gen/greeter.pb.go",
		"internal/gen/greeter_grpc.pb.go",
		"internal/gen/kpro_public_api_grpc.gen.go",
		"internal/gen/kpro_public_api_service.go",
	}
	if len(first) != len(wantPaths) {
		t.Fatalf("plan files = %d, want %d: %#v", len(first), len(wantPaths), first)
	}
	for index, want := range wantPaths {
		if first[index].Path != want {
			t.Errorf("file[%d].Path = %q, want %q", index, first[index].Path, want)
		}
	}
	if first[0].Ownership != codegen.Generated ||
		first[1].Ownership != codegen.Generated ||
		first[2].Ownership != codegen.Generated {
		t.Fatal("standard protobuf and adapter outputs must be generator-owned")
	}
	if first[3].Ownership != codegen.Scaffold {
		t.Fatalf("service ownership = %q, want scaffold", first[3].Ownership)
	}
	adapter := string(first[2].Content)
	if !strings.Contains(adapter, "type KProPublicApiGRPCServices struct") ||
		!strings.Contains(adapter, "func (services KProPublicApiGRPCServices) RegisterGRPC(registrar grpc.ServiceRegistrar) error") ||
		!strings.Contains(adapter, "if kproPublicApiGRPCNil(registrar)") ||
		!strings.Contains(adapter, "if kproPublicApiGRPCNil(services.Greeter)") ||
		!strings.Contains(adapter, "RegisterGreeterServer(registrar, services.Greeter)") ||
		!strings.Contains(adapter, "return nil") ||
		!strings.Contains(adapter, "RegisterGRPC(grpc.ServiceRegistrar) error") {
		t.Fatalf("unexpected adapter:\n%s", adapter)
	}
	scaffold := string(first[3].Content)
	if !strings.Contains(scaffold, "type KProPublicApiGreeterService struct") ||
		!strings.Contains(scaffold, "UnimplementedGreeterServer") ||
		!strings.Contains(scaffold, "var _ GreeterServer = (*KProPublicApiGreeterService)(nil)") {
		t.Fatalf("unexpected scaffold:\n%s", scaffold)
	}

	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(source.Package))); !os.IsNotExist(err) {
		t.Fatalf("Plan() wrote into project output package, stat error = %v", err)
	}
}

func TestGeneratorWithInstalledToolchain(t *testing.T) {
	for _, tool := range []string{"protoc", "protoc-gen-go", "protoc-gen-go-grpc"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("optional real-toolchain smoke test: %s is unavailable: %v", tool, err)
		}
	}

	root, value, source := testProject(t)
	files, err := (Generator{Root: root}).Plan(value, source)
	if err != nil {
		t.Fatalf("real toolchain Plan() error = %v", err)
	}
	want := map[string]codegen.Ownership{
		"internal/gen/greeter.pb.go":               codegen.Generated,
		"internal/gen/greeter_grpc.pb.go":          codegen.Generated,
		"internal/gen/kpro_public_api_grpc.gen.go": codegen.Generated,
		"internal/gen/kpro_public_api_service.go":  codegen.Scaffold,
	}
	for _, file := range files {
		ownership, exists := want[file.Path]
		if !exists {
			continue
		}
		if file.Ownership != ownership || len(file.Content) == 0 {
			t.Errorf("real output %q = ownership %q, %d bytes", file.Path, file.Ownership, len(file.Content))
		}
		delete(want, file.Path)
	}
	if len(want) != 0 {
		t.Fatalf("real toolchain plan is incomplete; missing %#v", want)
	}
}

func TestSourceFileIdentifierDisambiguatesNormalizedCollisions(t *testing.T) {
	first := manifest.Source{ID: "public-api", Kind: manifest.KindProto, Package: "internal/gen"}
	second := manifest.Source{ID: "public_api", Kind: manifest.KindProto, Package: "internal/gen"}
	value := manifest.Manifest{Sources: []manifest.Source{first, second}}
	firstID := sourceFileIdentifier(value, first)
	secondID := sourceFileIdentifier(value, second)
	if firstID == secondID {
		t.Fatalf("colliding source file identifiers = %q", firstID)
	}
	if exportedIdentifier(firstID) == exportedIdentifier(secondID) {
		t.Fatalf("colliding source type identifiers = %q", exportedIdentifier(firstID))
	}
	if strings.Contains(firstID, "-") || strings.Contains(secondID, "-") {
		t.Fatalf("source file identifiers are not filename-safe: %q, %q", firstID, secondID)
	}
}

func TestGeneratorReturnsProtocDiagnosticsWithoutProjectWrites(t *testing.T) {
	root, value, source := testProject(t)
	generator := Generator{
		Root:       root,
		LookupTool: availableTools(root),
		Runner: runnerFunc(func(context.Context, Command) ([]byte, error) {
			return []byte("api/greeter.proto:7: invalid field"), errors.New("exit status 1")
		}),
	}
	_, err := generator.Plan(value, source)
	if err == nil ||
		!strings.Contains(err.Error(), "protoc failed") ||
		!strings.Contains(err.Error(), "api/greeter.proto:7: invalid field") {
		t.Fatalf("Plan() error = %v, want protoc diagnostic", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, filepath.FromSlash(source.Package))); !os.IsNotExist(statErr) {
		t.Fatalf("failed Plan() wrote project output, stat error = %v", statErr)
	}
}

func TestGeneratorRejectsDescriptorWithoutServices(t *testing.T) {
	root, value, source := testProject(t)
	generator := Generator{
		Root:       root,
		LookupTool: availableTools(root),
		Runner: runnerFunc(func(_ context.Context, command Command) ([]byte, error) {
			writeDescriptor(t, command.Dir, source.Path, 0)
			return nil, nil
		}),
	}
	_, err := generator.Plan(value, source)
	if err == nil || !strings.Contains(err.Error(), "declares no gRPC services") {
		t.Fatalf("Plan() error = %v, want no services diagnostic", err)
	}
}

func TestGeneratorRejectsNonProtoSourceBeforeToolLookup(t *testing.T) {
	root, value, source := testProject(t)
	source.Path = "api/greeter.txt"
	value.Sources[0] = source
	if err := os.Rename(
		filepath.Join(root, "api", "greeter.proto"),
		filepath.Join(root, "api", "greeter.txt"),
	); err != nil {
		t.Fatal(err)
	}
	lookedUp := false
	generator := Generator{
		Root: root,
		LookupTool: func(string) (string, error) {
			lookedUp = true
			return "", nil
		},
	}
	_, err := generator.Plan(value, source)
	if err == nil || !strings.Contains(err.Error(), "must use the .proto extension") {
		t.Fatalf("Plan() error = %v, want extension diagnostic", err)
	}
	if lookedUp {
		t.Fatal("tool lookup occurred before source validation")
	}
}

type runnerFunc func(context.Context, Command) ([]byte, error)

func (function runnerFunc) Run(ctx context.Context, command Command) ([]byte, error) {
	return function(ctx, command)
}

func testProject(t *testing.T) (string, manifest.Manifest, manifest.Source) {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "api"), 0o755); err != nil {
		t.Fatal(err)
	}
	source := manifest.Source{
		ID:      "public-api",
		Kind:    manifest.KindProto,
		Path:    "api/greeter.proto",
		Package: "internal/gen",
	}
	value := manifest.Manifest{
		APIVersion: manifest.APIVersionV1Alpha1,
		Module:     "example.com/orders",
		Service:    "orders",
		Sources:    []manifest.Source{source},
	}
	contract := `syntax = "proto3";
package example.greeter.v1;
option go_package = "example.com/orders/internal/gen";
service Greeter {
  rpc SayHello(HelloRequest) returns (HelloReply);
}
message HelloRequest { string name = 1; }
message HelloReply { string message = 1; }
`
	if err := os.WriteFile(filepath.Join(root, "api", "greeter.proto"), []byte(contract), 0o644); err != nil {
		t.Fatal(err)
	}
	return root, value, source
}

func availableTools(root string) LookupTool {
	return func(name string) (string, error) {
		return filepath.Join(root, "tools", name+".exe"), nil
	}
}

func writeFakeProtocOutput(t *testing.T, root, sourcePath, outputPackage string) {
	t.Helper()
	writeDescriptor(t, root, sourcePath, 1)
	directory := filepath.Join(root, filepath.FromSlash(outputPackage))
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	protobuf := `// Code generated by protoc-gen-go. DO NOT EDIT.
package gen

type HelloRequest struct { Name string }
type HelloReply struct { Message string }
`
	grpc := `// Code generated by protoc-gen-go-grpc. DO NOT EDIT.
package gen

import "google.golang.org/grpc"

type GreeterServer interface {
	mustEmbedUnimplementedGreeterServer()
}

type UnimplementedGreeterServer struct{}

func (UnimplementedGreeterServer) mustEmbedUnimplementedGreeterServer() {}

func RegisterGreeterServer(registrar grpc.ServiceRegistrar, service GreeterServer) {}
`
	if err := os.WriteFile(filepath.Join(directory, "greeter.pb.go"), []byte(protobuf), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "greeter_grpc.pb.go"), []byte(grpc), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeDescriptor(t *testing.T, root, sourcePath string, serviceCount int) {
	t.Helper()
	file := &descriptorpb.FileDescriptorProto{Name: proto.String(sourcePath)}
	for index := 0; index < serviceCount; index++ {
		file.Service = append(file.Service, &descriptorpb.ServiceDescriptorProto{
			Name: proto.String("Greeter"),
		})
	}
	data, err := proto.Marshal(&descriptorpb.FileDescriptorSet{
		File: []*descriptorpb.FileDescriptorProto{file},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, descriptorFilename), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func assertCommand(
	t *testing.T,
	command Command,
	root string,
	value manifest.Manifest,
	source manifest.Source,
	lookup LookupTool,
) {
	t.Helper()
	protocPath, _ := lookup("protoc")
	protocPath, _ = filepath.Abs(protocPath)
	if command.Path != protocPath {
		t.Fatalf("command.Path = %q, want %q", command.Path, protocPath)
	}
	goPlugin, _ := lookup("protoc-gen-go")
	goPlugin, _ = filepath.Abs(goPlugin)
	grpcPlugin, _ := lookup("protoc-gen-go-grpc")
	grpcPlugin, _ = filepath.Abs(grpcPlugin)
	resolvedRoot, err := resolveRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"--proto_path=" + resolvedRoot,
		"--descriptor_set_out=" + descriptorFilename,
		"--include_imports",
		"--plugin=protoc-gen-go=" + goPlugin,
		"--plugin=protoc-gen-go-grpc=" + grpcPlugin,
		"--go_out=.",
		"--go_opt=module=" + value.Module,
		"--go_opt=M" + source.Path + "=" + value.Module + "/" + source.Package,
		"--go-grpc_out=.",
		"--go-grpc_opt=module=" + value.Module,
		"--go-grpc_opt=M" + source.Path + "=" + value.Module + "/" + source.Package,
		"--go-grpc_opt=require_unimplemented_servers=true",
		source.Path,
	}
	if !reflect.DeepEqual(command.Args, want) {
		t.Fatalf("command.Args =\n%q\nwant\n%q", command.Args, want)
	}
	if command.Dir == "" || filepath.Clean(command.Dir) == filepath.Clean(root) {
		t.Fatalf("command.Dir = %q, want isolated temporary directory", command.Dir)
	}
}
