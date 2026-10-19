package protogen

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"

	"github.com/saramand9/kpro/internal/codegen/manifest"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
)

// Contract is the descriptor-backed service model declared by one manifest
// protobuf source. Imported files are available to protoc for type resolution,
// but their services are not included in this value.
type Contract struct {
	Path     string
	Package  string
	Services []Service
}

// Service describes one protobuf service in its source declaration.
type Service struct {
	Name     string
	FullName string
	Methods  []Method
}

// Method describes one protobuf RPC method.
type Method struct {
	Name            string
	Input           string
	Output          string
	ClientStreaming bool
	ServerStreaming bool
}

// Inspector compiles protobuf source into an isolated descriptor set without
// invoking protoc-gen-go, protoc-gen-go-grpc, or another protoc plugin.
type Inspector struct {
	// Root contains the manifest and protobuf sources. An empty Root means the
	// current directory.
	Root string
	// Runner defaults to ExecRunner.
	Runner Runner
	// LookupTool defaults to exec.LookPath. Only protoc is resolved.
	LookupTool LookupTool
}

// Inspect describes one protobuf source using a descriptor-only protoc run.
func (inspector Inspector) Inspect(value manifest.Manifest, source manifest.Source) (Contract, error) {
	return inspector.InspectContext(context.Background(), value, source)
}

// InspectContext describes one protobuf source using a descriptor-only protoc
// run. It writes only to an isolated temporary directory.
func (inspector Inspector) InspectContext(
	ctx context.Context,
	value manifest.Manifest,
	source manifest.Source,
) (Contract, error) {
	if ctx == nil {
		return Contract{}, errors.New("proto inspect: context is nil")
	}
	if err := value.Validate(); err != nil {
		return Contract{}, fmt.Errorf("proto inspect: invalid manifest: %w", err)
	}
	if source.Kind != manifest.KindProto {
		return Contract{}, fmt.Errorf(
			"proto inspect: source %q has kind %q, want %q",
			source.ID,
			source.Kind,
			manifest.KindProto,
		)
	}
	if !manifestContains(value, source) {
		return Contract{}, fmt.Errorf("proto inspect: source %q is not present in the manifest", source.ID)
	}
	if path.Ext(source.Path) != ".proto" {
		return Contract{}, fmt.Errorf("proto inspect: source %q must use the .proto extension", source.Path)
	}

	root, err := resolveRoot(inspector.Root)
	if err != nil {
		return Contract{}, fmt.Errorf("proto inspect: %w", err)
	}
	snapshot, err := captureImportClosure(root, source.Path)
	if err != nil {
		return Contract{}, fmt.Errorf("proto inspect: validate import closure: %w", err)
	}
	source.Path = snapshot.sourcePath

	lookup := inspector.LookupTool
	if lookup == nil {
		lookup = exec.LookPath
	}
	protocPath, err := resolveTool(lookup, "protoc")
	if err != nil {
		return Contract{}, fmt.Errorf("proto inspect: %w", err)
	}

	inputRoot, err := materializeImportSnapshot(snapshot)
	if err != nil {
		return Contract{}, fmt.Errorf("proto inspect: %w", err)
	}
	defer os.RemoveAll(inputRoot)

	outputRoot, err := os.MkdirTemp("", "kpro-protoc-inspect-output-*")
	if err != nil {
		return Contract{}, fmt.Errorf("proto inspect: create isolated descriptor directory: %w", err)
	}
	defer os.RemoveAll(outputRoot)

	command := Command{
		Path: protocPath,
		Args: descriptorArgs(inputRoot, source),
		Dir:  outputRoot,
	}
	runner := inspector.Runner
	if runner == nil {
		runner = ExecRunner{}
	}
	output, err := runner.Run(ctx, command)
	if err != nil {
		detail := strings.TrimSpace(string(output))
		if detail == "" {
			return Contract{}, fmt.Errorf("proto inspect: protoc failed: %w", err)
		}
		return Contract{}, fmt.Errorf("proto inspect: protoc failed: %w: %s", err, detail)
	}

	contract, err := loadDescriptorSet(filepath.Join(outputRoot, descriptorFilename), source.Path)
	if err != nil {
		return Contract{}, fmt.Errorf("proto inspect: %w", err)
	}
	if len(contract.Services) == 0 {
		return Contract{}, fmt.Errorf("proto inspect: source %q declares no gRPC services", source.Path)
	}
	return contract, nil
}

func descriptorArgs(root string, source manifest.Source) []string {
	return []string{
		"--proto_path=" + root,
		"--descriptor_set_out=" + descriptorFilename,
		"--include_imports",
		source.Path,
	}
}

func resolveTool(lookup LookupTool, name string) (string, error) {
	found, err := lookup(name)
	if err != nil {
		return "", fmt.Errorf(
			"required tool %q was not found; install it and ensure it is on PATH: %w",
			name,
			err,
		)
	}
	if strings.TrimSpace(found) == "" {
		return "", fmt.Errorf("required tool %q resolved to an empty path", name)
	}
	absolute, err := filepath.Abs(found)
	if err != nil {
		return "", fmt.Errorf("resolve tool %q: %w", name, err)
	}
	return absolute, nil
}

func loadDescriptorSet(filename, sourcePath string) (Contract, error) {
	data, err := os.ReadFile(filename) // #nosec G304 -- Local CLI reads an operator-selected project or artifact path.
	if err != nil {
		return Contract{}, fmt.Errorf("read descriptor set: %w", err)
	}
	return ParseDescriptorSet(data, sourcePath)
}

// ParseDescriptorSet extracts the services declared by sourcePath from one
// protoc FileDescriptorSet. Method type names are returned without protobuf's
// leading dot and otherwise retain their fully qualified descriptor names.
func ParseDescriptorSet(data []byte, sourcePath string) (Contract, error) {
	var descriptor descriptorpb.FileDescriptorSet
	if err := proto.Unmarshal(data, &descriptor); err != nil {
		return Contract{}, fmt.Errorf("decode descriptor set: %w", err)
	}

	var matched *descriptorpb.FileDescriptorProto
	for _, file := range descriptor.File {
		if file.GetName() != sourcePath {
			continue
		}
		if matched != nil {
			return Contract{}, fmt.Errorf("descriptor set contains source %q more than once", sourcePath)
		}
		matched = file
	}
	if matched == nil {
		return Contract{}, fmt.Errorf("descriptor set does not contain source %q", sourcePath)
	}

	contract := Contract{
		Path:    sourcePath,
		Package: matched.GetPackage(),
	}
	for _, serviceDescriptor := range matched.Service {
		name := serviceDescriptor.GetName()
		if name == "" {
			return Contract{}, errors.New("descriptor set contains a service without a name")
		}
		fullName := name
		if contract.Package != "" {
			fullName = contract.Package + "." + name
		}
		service := Service{Name: name, FullName: fullName}
		for _, methodDescriptor := range serviceDescriptor.Method {
			name := methodDescriptor.GetName()
			input := strings.TrimPrefix(methodDescriptor.GetInputType(), ".")
			output := strings.TrimPrefix(methodDescriptor.GetOutputType(), ".")
			if name == "" || input == "" || output == "" {
				return Contract{}, fmt.Errorf("descriptor service %q contains an incomplete method", fullName)
			}
			service.Methods = append(service.Methods, Method{
				Name:            name,
				Input:           input,
				Output:          output,
				ClientStreaming: methodDescriptor.GetClientStreaming(),
				ServerStreaming: methodDescriptor.GetServerStreaming(),
			})
		}
		contract.Services = append(contract.Services, service)
	}
	return contract, nil
}
