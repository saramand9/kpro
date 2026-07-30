// Package protogen orchestrates the standard protobuf Go generators and
// converts their output into KPro's file-system-independent codegen plan.
//
// The package intentionally generates against grpc.ServiceRegistrar. KPro's
// grpcx.Server implements that standard interface, so generated application
// code does not need to import the dependency-neutral server package.
package protogen

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"github.com/saramand9/kpro/internal/codegen"
	"github.com/saramand9/kpro/internal/codegen/manifest"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
)

const descriptorFilename = "kpro_descriptor_set.pb"

// Command is one external tool invocation. Dir is an isolated temporary
// directory; Args never direct protoc to write into the project.
type Command struct {
	Path string
	Args []string
	Dir  string
}

// Runner executes protoc. It is an interface so callers and tests can provide
// a hermetic implementation.
type Runner interface {
	Run(context.Context, Command) ([]byte, error)
}

// ExecRunner executes a command with os/exec and captures its combined output.
type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, command Command) ([]byte, error) {
	process := exec.CommandContext(ctx, command.Path, command.Args...)
	process.Dir = command.Dir
	return process.CombinedOutput()
}

// LookupTool resolves a required executable.
type LookupTool func(string) (string, error)

// Generator plans standard .pb.go and _grpc.pb.go files, a generated
// grpc.ServiceRegistrar adapter, and a create-once service implementation
// skeleton.
type Generator struct {
	// Root contains the manifest and source contracts. An empty Root means the
	// current directory.
	Root string
	// Runner defaults to ExecRunner.
	Runner Runner
	// LookupTool defaults to exec.LookPath.
	LookupTool LookupTool
}

// Plan implements the source-generator seam used by the KPro CLI.
func (g Generator) Plan(value manifest.Manifest, source manifest.Source) ([]codegen.File, error) {
	return g.PlanContext(context.Background(), value, source)
}

// PlanContext builds all outputs in an isolated temporary directory and
// returns a virtual plan. It never writes into Root.
func (g Generator) PlanContext(ctx context.Context, value manifest.Manifest, source manifest.Source) ([]codegen.File, error) {
	if ctx == nil {
		return nil, errors.New("proto codegen: context is nil")
	}
	if err := value.Validate(); err != nil {
		return nil, fmt.Errorf("proto codegen: invalid manifest: %w", err)
	}
	if source.Kind != manifest.KindProto {
		return nil, fmt.Errorf("proto codegen: source %q has kind %q, want %q", source.ID, source.Kind, manifest.KindProto)
	}
	if !manifestContains(value, source) {
		return nil, fmt.Errorf("proto codegen: source %q is not present in the manifest", source.ID)
	}
	if path.Ext(source.Path) != ".proto" {
		return nil, fmt.Errorf("proto codegen: source %q must use the .proto extension", source.Path)
	}

	root, err := resolveRoot(g.Root)
	if err != nil {
		return nil, err
	}
	if err := validateInput(root, source.Path); err != nil {
		return nil, err
	}

	lookup := g.LookupTool
	if lookup == nil {
		lookup = exec.LookPath
	}
	tools, err := resolveTools(lookup)
	if err != nil {
		return nil, err
	}

	temporary, err := os.MkdirTemp("", "kpro-protoc-*")
	if err != nil {
		return nil, fmt.Errorf("proto codegen: create isolated output directory: %w", err)
	}
	defer os.RemoveAll(temporary)

	command := Command{
		Path: tools.protoc,
		Args: protocArgs(root, value, source, tools),
		Dir:  temporary,
	}
	runner := g.Runner
	if runner == nil {
		runner = ExecRunner{}
	}
	output, err := runner.Run(ctx, command)
	if err != nil {
		detail := strings.TrimSpace(string(output))
		if detail == "" {
			return nil, fmt.Errorf("proto codegen: protoc failed: %w", err)
		}
		return nil, fmt.Errorf("proto codegen: protoc failed: %w: %s", err, detail)
	}

	serviceCount, err := readServiceCount(filepath.Join(temporary, descriptorFilename), source.Path)
	if err != nil {
		return nil, err
	}
	if serviceCount == 0 {
		return nil, fmt.Errorf("proto codegen: source %q declares no gRPC services", source.Path)
	}

	outputs, packageName, registrations, err := collectGenerated(temporary, source.Package)
	if err != nil {
		return nil, err
	}
	if len(registrations) != serviceCount {
		return nil, fmt.Errorf(
			"proto codegen: protoc generated %d service registrations for %d declared services",
			len(registrations),
			serviceCount,
		)
	}

	fileID := sourceFileIdentifier(value, source)
	prefix := exportedIdentifier(fileID)
	adapter, err := renderAdapter(packageName, prefix, registrations)
	if err != nil {
		return nil, err
	}
	scaffold, err := renderScaffold(packageName, prefix, registrations)
	if err != nil {
		return nil, err
	}
	outputs = append(outputs,
		codegen.File{
			Path:      path.Join(source.Package, "kpro_"+fileID+"_grpc.gen.go"),
			Content:   adapter,
			Ownership: codegen.Generated,
		},
		codegen.File{
			Path:      path.Join(source.Package, "kpro_"+fileID+"_service.go"),
			Content:   scaffold,
			Ownership: codegen.Scaffold,
		},
	)

	plan, err := codegen.BuildPlan(outputs...)
	if err != nil {
		return nil, fmt.Errorf("proto codegen: build plan: %w", err)
	}
	return plan.OrderedFiles()
}

type resolvedTools struct {
	protoc       string
	protocGo     string
	protocGoGRPC string
}

func resolveTools(lookup LookupTool) (resolvedTools, error) {
	var result resolvedTools
	for _, required := range []struct {
		name   string
		target *string
	}{
		{name: "protoc", target: &result.protoc},
		{name: "protoc-gen-go", target: &result.protocGo},
		{name: "protoc-gen-go-grpc", target: &result.protocGoGRPC},
	} {
		found, err := lookup(required.name)
		if err != nil {
			return resolvedTools{}, fmt.Errorf(
				"proto codegen: required tool %q was not found; install it and ensure it is on PATH: %w",
				required.name,
				err,
			)
		}
		if strings.TrimSpace(found) == "" {
			return resolvedTools{}, fmt.Errorf("proto codegen: required tool %q resolved to an empty path", required.name)
		}
		absolute, err := filepath.Abs(found)
		if err != nil {
			return resolvedTools{}, fmt.Errorf("proto codegen: resolve tool %q: %w", required.name, err)
		}
		*required.target = absolute
	}
	return result, nil
}

func protocArgs(root string, value manifest.Manifest, source manifest.Source, tools resolvedTools) []string {
	importPath := value.Module + "/" + source.Package
	mapping := "M" + source.Path + "=" + importPath
	return []string{
		"--proto_path=" + root,
		"--descriptor_set_out=" + descriptorFilename,
		"--include_imports",
		"--plugin=protoc-gen-go=" + tools.protocGo,
		"--plugin=protoc-gen-go-grpc=" + tools.protocGoGRPC,
		"--go_out=.",
		"--go_opt=module=" + value.Module,
		"--go_opt=" + mapping,
		"--go-grpc_out=.",
		"--go-grpc_opt=module=" + value.Module,
		"--go-grpc_opt=" + mapping,
		"--go-grpc_opt=require_unimplemented_servers=true",
		source.Path,
	}
}

func resolveRoot(configured string) (string, error) {
	if configured == "" {
		configured = "."
	}
	absolute, err := filepath.Abs(configured)
	if err != nil {
		return "", fmt.Errorf("proto codegen: resolve root: %w", err)
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return "", fmt.Errorf("proto codegen: inspect root: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("proto codegen: root %q is not a directory", configured)
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("proto codegen: resolve root links: %w", err)
	}
	return resolved, nil
}

func validateInput(root, portablePath string) error {
	normalized, err := codegen.SafeRelativePath(portablePath)
	if err != nil {
		return fmt.Errorf("proto codegen: unsafe source path %q: %w", portablePath, err)
	}
	filename := filepath.Join(root, filepath.FromSlash(normalized))
	resolved, err := filepath.EvalSymlinks(filename)
	if err != nil {
		return fmt.Errorf("proto codegen: resolve source %q: %w", portablePath, err)
	}
	relative, err := filepath.Rel(root, resolved)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("proto codegen: source %q resolves outside the project root", portablePath)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return fmt.Errorf("proto codegen: inspect source %q: %w", portablePath, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("proto codegen: source %q is not a regular file", portablePath)
	}
	return nil
}

func manifestContains(value manifest.Manifest, source manifest.Source) bool {
	for _, candidate := range value.Sources {
		if candidate == source {
			return true
		}
	}
	return false
}

func readServiceCount(filename, sourcePath string) (int, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return 0, fmt.Errorf("proto codegen: read descriptor set: %w", err)
	}
	var descriptor descriptorpb.FileDescriptorSet
	if err := proto.Unmarshal(data, &descriptor); err != nil {
		return 0, fmt.Errorf("proto codegen: decode descriptor set: %w", err)
	}
	for _, file := range descriptor.File {
		if file.GetName() == sourcePath {
			return len(file.Service), nil
		}
	}
	return 0, fmt.Errorf("proto codegen: descriptor set does not contain source %q", sourcePath)
}

type registration struct {
	Function   string
	Service    string
	ServerType string
}

func collectGenerated(root, outputPackage string) ([]codegen.File, string, []registration, error) {
	var outputs []codegen.File
	var registrations []registration
	packageName := ""
	expectedDirectory := filepath.FromSlash(outputPackage)

	err := filepath.WalkDir(root, func(filename string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if filename == root {
			return nil
		}
		relative, err := filepath.Rel(root, filename)
		if err != nil {
			return err
		}
		if relative == descriptorFilename {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("proto codegen: protoc produced a symbolic link %q", filepath.ToSlash(relative))
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || filepath.Ext(relative) != ".go" {
			return fmt.Errorf("proto codegen: protoc produced unexpected output %q", filepath.ToSlash(relative))
		}
		if filepath.Dir(relative) != expectedDirectory {
			return fmt.Errorf(
				"proto codegen: protoc produced %q outside configured package %q",
				filepath.ToSlash(relative),
				outputPackage,
			)
		}

		content, err := os.ReadFile(filename)
		if err != nil {
			return err
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), filename, content, 0)
		if err != nil {
			return fmt.Errorf("proto codegen: parse generated file %q: %w", filepath.ToSlash(relative), err)
		}
		if packageName == "" {
			packageName = parsed.Name.Name
		} else if parsed.Name.Name != packageName {
			return fmt.Errorf(
				"proto codegen: generated package mismatch: %q uses %q, want %q",
				filepath.ToSlash(relative),
				parsed.Name.Name,
				packageName,
			)
		}
		if strings.HasSuffix(relative, "_grpc.pb.go") {
			found, err := findRegistrations(parsed)
			if err != nil {
				return fmt.Errorf("proto codegen: inspect %q: %w", filepath.ToSlash(relative), err)
			}
			registrations = append(registrations, found...)
		}
		outputs = append(outputs, codegen.File{
			Path:      filepath.ToSlash(relative),
			Content:   content,
			Ownership: codegen.Generated,
		})
		return nil
	})
	if err != nil {
		return nil, "", nil, err
	}
	if len(outputs) == 0 {
		return nil, "", nil, errors.New("proto codegen: protoc produced no Go files")
	}
	if packageName == "" {
		return nil, "", nil, errors.New("proto codegen: protoc output has no Go package")
	}
	sort.Slice(registrations, func(i, j int) bool {
		return registrations[i].Function < registrations[j].Function
	})
	for i := 1; i < len(registrations); i++ {
		if registrations[i-1].Function == registrations[i].Function ||
			registrations[i-1].Service == registrations[i].Service {
			return nil, "", nil, fmt.Errorf("proto codegen: duplicate service registration %q", registrations[i].Function)
		}
	}
	return outputs, packageName, registrations, nil
}

func findRegistrations(file *ast.File) ([]registration, error) {
	var result []registration
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Recv != nil {
			continue
		}
		name := function.Name.Name
		if !strings.HasPrefix(name, "Register") || !strings.HasSuffix(name, "Server") {
			continue
		}
		service := strings.TrimSuffix(strings.TrimPrefix(name, "Register"), "Server")
		if service == "" || function.Type.Params == nil || len(function.Type.Params.List) != 2 {
			continue
		}
		serverType, ok := function.Type.Params.List[1].Type.(*ast.Ident)
		if !ok || serverType.Name != service+"Server" {
			continue
		}
		result = append(result, registration{
			Function:   name,
			Service:    service,
			ServerType: serverType.Name,
		})
	}
	return result, nil
}

func renderAdapter(packageName, prefix string, registrations []registration) ([]byte, error) {
	var source bytes.Buffer
	fmt.Fprintln(&source, "// Code generated by kpro. DO NOT EDIT.")
	fmt.Fprintln(&source)
	fmt.Fprintf(&source, "package %s\n\n", packageName)
	fmt.Fprintln(&source, `import (`)
	fmt.Fprintln(&source, `	"errors"`)
	fmt.Fprintln(&source, `	"fmt"`)
	fmt.Fprintln(&source, `	"reflect"`)
	fmt.Fprintln(&source)
	fmt.Fprintln(&source, `	"google.golang.org/grpc"`)
	fmt.Fprintln(&source, `)`)
	fmt.Fprintln(&source)
	typeName := "KPro" + prefix + "GRPCServices"
	helperName := "kpro" + prefix + "GRPCNil"
	fmt.Fprintf(&source, "// %s groups the service implementations declared by this protobuf source.\n", typeName)
	fmt.Fprintf(&source, "type %s struct {\n", typeName)
	for _, registration := range registrations {
		fmt.Fprintf(&source, "\t%s %s\n", registration.Service, registration.ServerType)
	}
	fmt.Fprintln(&source, "}")
	fmt.Fprintln(&source)
	fmt.Fprintln(&source, "// RegisterGRPC registers every service with a KPro grpcx.Server or any standard gRPC registrar.")
	fmt.Fprintf(&source, "func (services %s) RegisterGRPC(registrar grpc.ServiceRegistrar) error {\n", typeName)
	fmt.Fprintf(&source, "\tif %s(registrar) {\n", helperName)
	fmt.Fprintln(&source, `		return errors.New("register gRPC services: registrar is nil")`)
	fmt.Fprintln(&source, "\t}")
	for _, registration := range registrations {
		fmt.Fprintf(&source, "\tif %s(services.%s) {\n", helperName, registration.Service)
		fmt.Fprintf(
			&source,
			"\t\treturn fmt.Errorf(\"register gRPC services: service %%q is nil\", %q)\n",
			registration.Service,
		)
		fmt.Fprintln(&source, "\t}")
	}
	for _, registration := range registrations {
		fmt.Fprintf(&source, "\t%s(registrar, services.%s)\n", registration.Function, registration.Service)
	}
	fmt.Fprintln(&source, "\treturn nil")
	fmt.Fprintln(&source, "}")
	fmt.Fprintln(&source)
	fmt.Fprintf(&source, "func %s(value any) bool {\n", helperName)
	fmt.Fprintln(&source, "\tif value == nil {")
	fmt.Fprintln(&source, "\t\treturn true")
	fmt.Fprintln(&source, "\t}")
	fmt.Fprintln(&source, "\treflected := reflect.ValueOf(value)")
	fmt.Fprintln(&source, "\tswitch reflected.Kind() {")
	fmt.Fprintln(&source, "\tcase reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:")
	fmt.Fprintln(&source, "\t\treturn reflected.IsNil()")
	fmt.Fprintln(&source, "\tdefault:")
	fmt.Fprintln(&source, "\t\treturn false")
	fmt.Fprintln(&source, "\t}")
	fmt.Fprintln(&source, "}")
	fmt.Fprintln(&source)
	fmt.Fprintf(
		&source,
		"var _ interface{ RegisterGRPC(grpc.ServiceRegistrar) error } = %s{}\n",
		typeName,
	)
	formatted, err := format.Source(source.Bytes())
	if err != nil {
		return nil, fmt.Errorf("proto codegen: format registration adapter: %w", err)
	}
	return formatted, nil
}

func renderScaffold(packageName, prefix string, registrations []registration) ([]byte, error) {
	var source bytes.Buffer
	fmt.Fprintln(&source, "// This file is created once by kpro and is safe to edit.")
	fmt.Fprintln(&source)
	fmt.Fprintf(&source, "package %s\n\n", packageName)
	for _, registration := range registrations {
		typeName := "KPro" + prefix + registration.Service + "Service"
		fmt.Fprintf(&source, "// %s is the initial application-owned implementation for %s.\n", typeName, registration.Service)
		fmt.Fprintf(&source, "type %s struct {\n", typeName)
		fmt.Fprintf(&source, "\tUnimplemented%s\n", registration.ServerType)
		fmt.Fprintln(&source, "}")
		fmt.Fprintln(&source)
		fmt.Fprintf(&source, "var _ %s = (*%s)(nil)\n\n", registration.ServerType, typeName)
	}
	formatted, err := format.Source(source.Bytes())
	if err != nil {
		return nil, fmt.Errorf("proto codegen: format service scaffold: %w", err)
	}
	return formatted, nil
}

func exportedIdentifier(value string) string {
	var output strings.Builder
	upperNext := true
	for _, character := range value {
		if character == '-' || character == '_' {
			upperNext = true
			continue
		}
		if upperNext {
			output.WriteRune(unicode.ToUpper(character))
			upperNext = false
			continue
		}
		output.WriteRune(character)
	}
	return output.String()
}

func sourceFileIdentifier(value manifest.Manifest, source manifest.Source) string {
	identifier := strings.ReplaceAll(source.ID, "-", "_")
	for _, candidate := range value.Sources {
		if candidate.ID == source.ID ||
			candidate.Kind != manifest.KindProto ||
			candidate.Package != source.Package ||
			strings.ReplaceAll(candidate.ID, "-", "_") != identifier {
			continue
		}
		digest := sha256.Sum256([]byte(source.ID))
		return fmt.Sprintf("%s_%x", identifier, digest[:4])
	}
	return identifier
}
