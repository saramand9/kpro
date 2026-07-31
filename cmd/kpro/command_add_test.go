package main

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/saramand9/kpro/internal/codegen"
	"github.com/saramand9/kpro/internal/codegen/manifest"
)

func TestAddGRPCHermeticSuccessNoOpAndPreservesCustomScaffold(t *testing.T) {
	installAddTestEnvironment(t)
	root := createAddTestProject(t, true)

	httpScaffold := filepath.Join(root, "internal", "kproapp", "http.go")
	customHTTP := []byte("package kproapp\n\n// user-owned HTTP customization\nfunc customHTTPMarker() {}\n")
	if err := os.WriteFile(httpScaffold, customHTTP, 0o640); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	if err := runAdd([]string{"--api", "grpc", "--root", root}, &output, &output); err != nil {
		t.Fatalf("first add: %v\n%s", err, output.String())
	}

	if got, err := os.ReadFile(httpScaffold); err != nil || !bytes.Equal(got, customHTTP) {
		t.Fatalf("custom HTTP scaffold changed: err=%v\n%s", err, got)
	}
	manifestData := readAddTestFile(t, root, "kpro.gen.yaml")
	for _, expected := range []string{"# keep manifest comment", "# keep HTTP source comment", "id: grpc-api", "path: api/grpc/service.proto"} {
		if !bytes.Contains(manifestData, []byte(expected)) {
			t.Fatalf("updated manifest lost %q:\n%s", expected, manifestData)
		}
	}
	value, err := manifest.Parse(manifestData)
	if err != nil {
		t.Fatal(err)
	}
	grpcSource, exists := serviceAssemblySource(value.Sources, "grpc-api")
	if !exists || !canonicalAddGRPCSource(grpcSource) {
		t.Fatalf("gRPC source = %#v/%v, want canonical source", grpcSource, exists)
	}

	serviceData := readAddTestFile(t, root, defaultServiceProjectFile)
	if !bytes.Contains(serviceData, []byte("# keep service comment")) {
		t.Fatalf("updated service config lost its comment:\n%s", serviceData)
	}
	project, err := parseServiceProjectConfig(serviceData)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"http", "grpc"}; !reflect.DeepEqual(project.APIs, want) {
		t.Fatalf("service APIs = %v, want %v", project.APIs, want)
	}

	proto := readAddTestFile(t, root, addGRPCProtoPath)
	if !bytes.Contains(proto, []byte("service OrdersApi")) {
		t.Fatalf("generated proto has wrong service:\n%s", proto)
	}
	starter := readAddTestFile(t, root, addGRPCServicePath)
	if !bytes.Contains(starter, []byte(`Status: "ready"`)) {
		t.Fatalf("gRPC starter is not ready by default:\n%s", starter)
	}
	endpoints := readAddTestFile(t, root, "internal/kproapp/endpoints.gen.go")
	if !bytes.Contains(endpoints, []byte("mountGRPC(app)")) {
		t.Fatalf("generated endpoint assembly did not mount gRPC:\n%s", endpoints)
	}

	plan, err := buildGenerationPlan(root, value, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := codegen.Execute(plan, codegen.Options{Root: root, Mode: codegen.Check}); err != nil {
		t.Fatalf("project is not current after add: %v", err)
	}

	beforeSecond := snapshotAddTestTree(t, root)
	output.Reset()
	if err := runAdd([]string{"--api", "grpc", "--root", root}, &output, &output); err != nil {
		t.Fatalf("second add: %v\n%s", err, output.String())
	}
	if afterSecond := snapshotAddTestTree(t, root); !reflect.DeepEqual(afterSecond, beforeSecond) {
		t.Fatalf("second add changed an already-current project\nbefore=%v\nafter=%v", beforeSecond, afterSecond)
	}
	if !strings.Contains(strings.ToLower(output.String()), "already") {
		t.Fatalf("second add did not report a no-op:\n%s", output.String())
	}
}

func TestAddGRPCPreservesDeletedCreateOnceScaffold(t *testing.T) {
	installAddTestEnvironment(t)
	root := createAddTestProject(t, true)
	deleted := filepath.Join(root, "internal", "kproapp", "http.go")
	if err := os.Remove(deleted); err != nil {
		t.Fatal(err)
	}

	value, err := manifest.Load(filepath.Join(root, "kpro.gen.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	currentPlan, err := buildGenerationPlan(root, value, false)
	if err != nil {
		t.Fatal(err)
	}
	current, err := codegen.Execute(currentPlan, codegen.Options{Root: root, Mode: codegen.DryRun})
	if err != nil {
		t.Fatalf("dry-run current plan with deleted scaffold: %v", err)
	}
	if len(current.Created) != 0 || len(current.Updated) != 0 || len(current.Removed) != 0 {
		t.Fatalf("deleted create-once scaffold made outputs stale: %+v", current)
	}

	var output bytes.Buffer
	if err := runAdd([]string{"--api", "grpc", "--root", root}, &output, &output); err != nil {
		t.Fatalf("add with deleted scaffold: %v\n%s", err, output.String())
	}
	if _, err := os.Lstat(deleted); !os.IsNotExist(err) {
		t.Fatalf("deleted create-once scaffold was restored: %v", err)
	}

	beforeSecond := snapshotAddTestTree(t, root)
	output.Reset()
	if err := runAdd([]string{"--api", "grpc", "--root", root}, &output, &output); err != nil {
		t.Fatalf("second add with deleted scaffold: %v\n%s", err, output.String())
	}
	if afterSecond := snapshotAddTestTree(t, root); !reflect.DeepEqual(afterSecond, beforeSecond) {
		t.Fatalf("second add changed project with deleted scaffold\nbefore=%v\nafter=%v", beforeSecond, afterSecond)
	}
	if _, err := os.Lstat(deleted); !os.IsNotExist(err) {
		t.Fatalf("second add restored deleted create-once scaffold: %v", err)
	}
}
func TestAddGRPCMatchesDirectDesiredProject(t *testing.T) {
	installAddTestEnvironment(t)
	incremental := createAddTestProject(t, true)
	var output bytes.Buffer
	if err := runAdd([]string{"--api", "grpc", "--root", incremental}, &output, &output); err != nil {
		t.Fatalf("incremental add: %v\n%s", err, output.String())
	}
	direct := createAddTestDesiredGRPCProject(t)

	value, err := manifest.Load(filepath.Join(incremental, "kpro.gen.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := buildGenerationPlan(incremental, value, false)
	if err != nil {
		t.Fatal(err)
	}
	files, err := plan.OrderedFiles()
	if err != nil {
		t.Fatal(err)
	}
	paths := map[string]struct{}{
		"kpro.gen.yaml":           {},
		defaultServiceProjectFile: {},
		codegen.DefaultLockFile:   {},
		"api/http/openapi.yaml":   {},
		addGRPCProtoPath:          {},
		addGRPCServicePath:        {},
	}
	for _, file := range files {
		paths[file.Path] = struct{}{}
	}
	for name := range paths {
		incrementalState, err := inspectAddProjectFile(incremental, name)
		if err != nil {
			t.Fatalf("inspect incremental %s: %v", name, err)
		}
		directState, err := inspectAddProjectFile(direct, name)
		if err != nil {
			t.Fatalf("inspect direct %s: %v", name, err)
		}
		if incrementalState.exists != directState.exists ||
			!bytes.Equal(incrementalState.content, directState.content) ||
			incrementalState.mode.Perm() != directState.mode.Perm() {
			t.Fatalf("incremental %s does not match direct desired project\nincremental=%#v\ndirect=%#v", name, incrementalState, directState)
		}
	}
}

func TestAddGRPCCommitFailureWritesNothing(t *testing.T) {
	installAddTestEnvironment(t)
	root := createAddTestProject(t, true)
	before := snapshotAddTestTree(t, root)
	addCommitFileTransaction = func([]codegen.FileMutation, codegen.FileTransactionOptions) (codegen.FileTransactionResult, error) {
		return codegen.FileTransactionResult{}, errors.New("injected commit failure")
	}

	var output bytes.Buffer
	err := runAdd([]string{"--api", "grpc", "--root", root}, &output, &output)
	if err == nil || !strings.Contains(err.Error(), "injected commit failure") {
		t.Fatalf("commit-failure error = %v", err)
	}
	if after := snapshotAddTestTree(t, root); !reflect.DeepEqual(after, before) {
		t.Fatalf("commit failure changed files\nbefore=%v\nafter=%v", before, after)
	}
}
func TestAddGRPCReportsCleanupWarningsAfterSuccessfulCommit(t *testing.T) {
	installAddTestEnvironment(t)
	root := createAddTestProject(t, true)
	before := snapshotAddTestTree(t, root)
	addCommitFileTransaction = func([]codegen.FileMutation, codegen.FileTransactionOptions) (codegen.FileTransactionResult, error) {
		return codegen.FileTransactionResult{
			Created:         []string{addGRPCProtoPath},
			CleanupWarnings: []string{"retained transaction backup for manual cleanup"},
		}, nil
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if err := runAdd([]string{"--api", "grpc", "--root", root}, &stdout, &stderr); err != nil {
		t.Fatalf("successful commit with cleanup warning: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	for _, expected := range []string{"create " + addGRPCProtoPath, "next:", "go mod tidy"} {
		if !strings.Contains(stdout.String(), expected) {
			t.Fatalf("stdout does not contain %q:\n%s", expected, stdout.String())
		}
	}
	if !strings.Contains(stderr.String(), "warning: retained transaction backup for manual cleanup") {
		t.Fatalf("cleanup warning was not written to stderr:\n%s", stderr.String())
	}
	assertAddProjectUnchanged(t, root, before)
	assertNoAddStageSiblings(t, root)
}

func TestAddGRPCDryRunWritesNothing(t *testing.T) {
	installAddTestEnvironment(t)
	root := createAddTestProject(t, true)
	before := snapshotAddTestTree(t, root)

	var output bytes.Buffer
	if err := runAdd([]string{"--api", "grpc", "--root", root, "--dry-run"}, &output, &output); err != nil {
		t.Fatalf("dry run: %v\n%s", err, output.String())
	}
	if after := snapshotAddTestTree(t, root); !reflect.DeepEqual(after, before) {
		t.Fatalf("dry run changed the project\nbefore=%v\nafter=%v", before, after)
	}
	for _, expected := range []string{"would create api/grpc/service.proto", "would update kpro.gen.yaml", "would update kpro.service.yaml"} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("dry-run output does not contain %q:\n%s", expected, output.String())
		}
	}
}

func TestAddGRPCRejectsLegacyProjectWithoutWrites(t *testing.T) {
	installAddTestEnvironment(t)
	root := createAddTestProject(t, false)
	before := snapshotAddTestTree(t, root)

	var output bytes.Buffer
	err := runAdd([]string{"--api", "grpc", "--root", root}, &output, &output)
	if err == nil || !strings.Contains(err.Error(), defaultServiceProjectFile) {
		t.Fatalf("legacy project error = %v, want %s requirement", err, defaultServiceProjectFile)
	}
	if after := snapshotAddTestTree(t, root); !reflect.DeepEqual(after, before) {
		t.Fatalf("legacy-project rejection changed files\nbefore=%v\nafter=%v", before, after)
	}
}

func TestAddGRPCMissingToolWritesNothing(t *testing.T) {
	installAddTestEnvironment(t)
	root := createAddTestProject(t, true)
	before := snapshotAddTestTree(t, root)
	addLookPath = func(name string) (string, error) {
		if name == "protoc-gen-go" {
			return "", os.ErrNotExist
		}
		return filepath.Join("tools", name), nil
	}

	var output bytes.Buffer
	err := runAdd([]string{"--api", "grpc", "--root", root}, &output, &output)
	if err == nil || !strings.Contains(err.Error(), "protoc-gen-go") {
		t.Fatalf("missing-tool error = %v", err)
	}
	if after := snapshotAddTestTree(t, root); !reflect.DeepEqual(after, before) {
		t.Fatalf("missing-tool rejection changed files\nbefore=%v\nafter=%v", before, after)
	}
}

func TestAddGRPCRejectsModifiedGeneratedOutputWithoutWrites(t *testing.T) {
	installAddTestEnvironment(t)
	root := createAddTestProject(t, true)
	generatedPath := filepath.Join(root, "internal", "kproapp", "endpoints.gen.go")
	if err := os.WriteFile(generatedPath, []byte("package kproapp\n\n// conflicting generated edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := snapshotAddTestTree(t, root)

	var output bytes.Buffer
	err := runAdd([]string{"--api", "grpc", "--root", root}, &output, &output)
	if err == nil || !strings.Contains(err.Error(), "generated output was modified") {
		t.Fatalf("modified-generated-output error = %v", err)
	}
	if after := snapshotAddTestTree(t, root); !reflect.DeepEqual(after, before) {
		t.Fatalf("generated-output conflict changed files\nbefore=%v\nafter=%v", before, after)
	}
}
func TestAddGRPCUnmanagedConflictWritesNothing(t *testing.T) {
	installAddTestEnvironment(t)
	root := createAddTestProject(t, true)
	writeAddTestFile(t, root, addGRPCProtoPath, []byte("// application-owned conflicting proto\n"))
	before := snapshotAddTestTree(t, root)

	var output bytes.Buffer
	err := runAdd([]string{"--api", "grpc", "--root", root}, &output, &output)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "unmanaged") {
		t.Fatalf("unmanaged-conflict error = %v", err)
	}
	if after := snapshotAddTestTree(t, root); !reflect.DeepEqual(after, before) {
		t.Fatalf("unmanaged conflict changed files\nbefore=%v\nafter=%v", before, after)
	}
}

func TestAddGRPCRejectsSymlinkedProjectPathsWithoutWrites(t *testing.T) {
	tests := []struct {
		name string
		path string
	}{
		{name: "generation manifest", path: "kpro.gen.yaml"},
		{name: "manifest source", path: "api/http/openapi.yaml"},
		{name: "new generated output", path: "internal/grpcapi/contract.gen.go"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			installAddTestEnvironment(t)
			root := createAddTestProject(t, true)
			link := filepath.Join(root, filepath.FromSlash(test.path))
			targetContent := []byte("external target remains untouched\n")
			if current, err := os.ReadFile(link); err == nil {
				targetContent = current
			} else if !os.IsNotExist(err) {
				t.Fatal(err)
			}
			target := filepath.Join(t.TempDir(), "symlink-target")
			if err := os.WriteFile(target, targetContent, 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(link); err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			if err := os.Symlink(target, link); err != nil {
				t.Skipf("symbolic links are unavailable on this platform: %v", err)
			}
			before := snapshotAddTestTree(t, root)

			var output bytes.Buffer
			err := runAdd([]string{"--api", "grpc", "--root", root}, &output, &output)
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), "symbolic link") {
				t.Fatalf("symlink rejection error = %v\n%s", err, output.String())
			}
			assertAddProjectUnchanged(t, root, before)
			assertNoAddStageSiblings(t, root)
		})
	}
}

func TestAddGRPCRejectsInconsistentSelectionsWithoutWrites(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, string)
	}{
		{
			name: "service selects grpc but manifest does not",
			mutate: func(t *testing.T, root string) {
				service := readAddTestFile(t, root, defaultServiceProjectFile)
				updated, err := addServiceAPIYAML(service, "grpc")
				if err != nil {
					t.Fatal(err)
				}
				writeAddTestFile(t, root, defaultServiceProjectFile, updated)
			},
		},
		{
			name: "manifest selects grpc but service does not",
			mutate: func(t *testing.T, root string) {
				generationManifest := readAddTestFile(t, root, "kpro.gen.yaml")
				updated, err := appendAddGRPCManifestSource(generationManifest)
				if err != nil {
					t.Fatal(err)
				}
				writeAddTestFile(t, root, "kpro.gen.yaml", updated)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			installAddTestEnvironment(t)
			root := createAddTestProject(t, true)
			test.mutate(t, root)
			before := snapshotAddTestTree(t, root)

			var output bytes.Buffer
			err := runAdd([]string{"--api", "grpc", "--root", root}, &output, &output)
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), "inconsistent") {
				t.Fatalf("inconsistent-selection error = %v\n%s", err, output.String())
			}
			assertAddProjectUnchanged(t, root, before)
			assertNoAddStageSiblings(t, root)
		})
	}
}

func TestAddGRPCStageGeneratorFailureWritesNothing(t *testing.T) {
	installAddTestEnvironment(t)
	root := createAddTestProject(t, true)
	before := snapshotAddTestTree(t, root)
	sourceGenerators[manifest.KindProto] = sourceGeneratorFunc(func(string, manifest.Manifest, manifest.Source) (codegen.Plan, error) {
		return codegen.Plan{}, errors.New("injected proto generator failure")
	})

	var output bytes.Buffer
	err := runAdd([]string{"--api", "grpc", "--root", root}, &output, &output)
	if err == nil || !strings.Contains(err.Error(), "injected proto generator failure") {
		t.Fatalf("stage-generator error = %v\n%s", err, output.String())
	}
	assertAddProjectUnchanged(t, root, before)
	assertNoAddStageSiblings(t, root)
}

func TestAddGRPCStagesAndSnapshotsTransitiveProtoImports(t *testing.T) {
	installAddTestEnvironment(t)
	root := createAddTestProject(t, true)
	manifestData := readAddTestFile(t, root, defaultCodegenConfig)
	manifestData = append(manifestData, []byte(`  - id: existing-proto
    kind: proto
    path: api/existing/service.proto
    package: internal/existing
`)...)
	writeAddTestFile(t, root, defaultCodegenConfig, manifestData)
	writeAddTestFile(t, root, "api/existing/service.proto", []byte(`syntax = "proto3";
package existing;
import "api/common/common.proto";
service ExistingApi {}
`))
	commonContent := []byte("syntax = \"proto3\";\npackage common;\nmessage Shared {}\n")
	writeAddTestFile(t, root, "api/common/common.proto", commonContent)

	sourceGenerators[manifest.KindProto] = sourceGeneratorFunc(func(root string, value manifest.Manifest, source manifest.Source) (codegen.Plan, error) {
		content, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(source.Path)))
		if err != nil {
			return codegen.Plan{}, err
		}
		imports, err := parseProtoImports(content)
		if err != nil {
			return codegen.Plan{}, err
		}
		for _, dependency := range imports {
			if _, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(dependency))); err != nil {
				return codegen.Plan{}, fmt.Errorf("read imported proto %q: %w", dependency, err)
			}
		}
		planned, err := addTestSourcePlan(root, value, source)
		if err != nil || source.ID == "grpc-api" {
			return planned, err
		}
		files, err := planned.OrderedFiles()
		if err != nil {
			return codegen.Plan{}, err
		}
		filtered := files[:0]
		for _, file := range files {
			if file.Path != addGRPCServicePath {
				filtered = append(filtered, file)
			}
		}
		return codegen.BuildPlan(filtered...)
	})
	value, err := manifest.Load(filepath.Join(root, defaultCodegenConfig))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := buildGenerationPlan(root, value, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := codegen.Execute(plan, codegen.Options{Root: root}); err != nil {
		t.Fatalf("generate current project with imported proto: %v", err)
	}

	commit := addCommitFileTransaction
	sawDependency := false
	addCommitFileTransaction = func(mutations []codegen.FileMutation, options codegen.FileTransactionOptions) (codegen.FileTransactionResult, error) {
		for _, mutation := range mutations {
			if mutation.Path == "api/common/common.proto" {
				sawDependency = true
			}
		}
		return commit(mutations, options)
	}
	var output bytes.Buffer
	if err := runAdd([]string{"--api", "grpc", "--root", root}, &output, &output); err != nil {
		t.Fatalf("add with transitive proto import: %v\n%s", err, output.String())
	}
	if !sawDependency {
		t.Fatal("transitive proto dependency was not included in the add CAS read set")
	}
	if got := readAddTestFile(t, root, "api/common/common.proto"); !bytes.Equal(got, commonContent) {
		t.Fatalf("transitive proto dependency changed:\n%s", got)
	}
}

func installAddTestEnvironment(t *testing.T) {
	t.Helper()
	previousGenerators := sourceGenerators
	previousLookPath := addLookPath
	previousCommit := addCommitFileTransaction
	sourceGenerators = map[manifest.SourceKind]sourceGenerator{
		manifest.KindOpenAPI: sourceGeneratorFunc(addTestSourcePlan),
		manifest.KindProto:   sourceGeneratorFunc(addTestSourcePlan),
		manifest.KindMessage: sourceGeneratorFunc(addTestSourcePlan),
	}
	addLookPath = func(name string) (string, error) {
		return filepath.Join("tools", name), nil
	}
	addCommitFileTransaction = codegen.CommitFileTransaction
	t.Cleanup(func() {
		sourceGenerators = previousGenerators
		addLookPath = previousLookPath
		addCommitFileTransaction = previousCommit
	})
}

func addTestSourcePlan(root string, value manifest.Manifest, source manifest.Source) (codegen.Plan, error) {
	if _, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(source.Path))); err != nil {
		return codegen.Plan{}, fmt.Errorf("read fake source: %w", err)
	}
	packageName := path.Base(source.Package)
	files := []codegen.File{{
		Path:      path.Join(source.Package, "contract.gen.go"),
		Content:   []byte("// Code generated by add test. DO NOT EDIT.\npackage " + packageName + "\n"),
		Ownership: codegen.Generated,
	}}
	if source.Kind == manifest.KindProto {
		files = append(files, codegen.File{
			Path:      addGRPCServicePath,
			Content:   []byte("package grpcapi\n\n// generator default; add must preserve its ready starter instead\ntype KProGrpcApiOrdersApiService struct{}\n"),
			Ownership: codegen.Scaffold,
		})
	}
	return codegen.BuildPlan(files...)
}

func createAddTestProject(t *testing.T, unified bool) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "project")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	writeAddTestFile(t, root, "kpro.gen.yaml", []byte(`# keep manifest comment
apiVersion: kpro.dev/codegen/v1alpha1
module: example.com/orders
service: orders_api
sources:
  # keep HTTP source comment
  - id: http-api
    kind: openapi
    path: api/http/openapi.yaml
    package: internal/httpapi
`))
	writeAddTestFile(t, root, "api/http/openapi.yaml", []byte("openapi: 3.1.0\ninfo:\n  title: Orders\n  version: 1.0.0\npaths: {}\n"))
	if unified {
		writeAddTestFile(t, root, defaultServiceProjectFile, []byte(`# keep service comment
apiVersion: kpro.dev/service/v1alpha1
apis:
  - http
`))
	}
	value, err := manifest.Load(filepath.Join(root, "kpro.gen.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := buildGenerationPlan(root, value, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := codegen.Execute(plan, codegen.Options{Root: root}); err != nil {
		t.Fatal(err)
	}
	return root
}

func createAddTestDesiredGRPCProject(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "direct-project")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	baseManifest := []byte(`# keep manifest comment
apiVersion: kpro.dev/codegen/v1alpha1
module: example.com/orders
service: orders_api
sources:
  # keep HTTP source comment
  - id: http-api
    kind: openapi
    path: api/http/openapi.yaml
    package: internal/httpapi
`)
	updatedManifest, err := appendAddGRPCManifestSource(baseManifest)
	if err != nil {
		t.Fatal(err)
	}
	writeAddTestFile(t, root, "kpro.gen.yaml", updatedManifest)
	baseService := []byte(`# keep service comment
apiVersion: kpro.dev/service/v1alpha1
apis:
  - http
`)
	updatedService, err := addServiceAPIYAML(baseService, "grpc")
	if err != nil {
		t.Fatal(err)
	}
	writeAddTestFile(t, root, defaultServiceProjectFile, updatedService)
	writeAddTestFile(t, root, "api/http/openapi.yaml", []byte("openapi: 3.1.0\ninfo:\n  title: Orders\n  version: 1.0.0\npaths: {}\n"))
	data := scaffoldData{
		Module:            "example.com/orders",
		ServiceIdentifier: "orders_api",
		ServiceExported:   "OrdersApi",
	}
	if err := renderTemplate(root, "templates/profiles/service/service.proto.tmpl", addGRPCProtoPath, 0o644, data); err != nil {
		t.Fatal(err)
	}
	if err := renderTemplate(root, "templates/profiles/service/grpc_service.go.tmpl", addGRPCServicePath, 0o644, data); err != nil {
		t.Fatal(err)
	}
	value, err := manifest.Load(filepath.Join(root, "kpro.gen.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := buildGenerationPlan(root, value, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := codegen.Execute(plan, codegen.Options{Root: root}); err != nil {
		t.Fatal(err)
	}
	return root
}
func writeAddTestFile(t *testing.T, root, name string, content []byte) {
	t.Helper()
	filename := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(filename), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filename, content, 0o644); err != nil {
		t.Fatal(err)
	}
}

func readAddTestFile(t *testing.T, root, name string) []byte {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
	if err != nil {
		t.Fatal(err)
	}
	return content
}

type addTestTreeEntry struct {
	content string
	mode    fs.FileMode
}

func snapshotAddTestTree(t *testing.T, root string) map[string]addTestTreeEntry {
	t.Helper()
	result := make(map[string]addTestTreeEntry)
	err := filepath.WalkDir(root, func(filename string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		content, err := os.ReadFile(filename)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, filename)
		if err != nil {
			return err
		}
		result[filepath.ToSlash(relative)] = addTestTreeEntry{content: string(content), mode: info.Mode()}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func assertAddProjectUnchanged(t *testing.T, root string, before map[string]addTestTreeEntry) {
	t.Helper()
	if after := snapshotAddTestTree(t, root); !reflect.DeepEqual(after, before) {
		t.Fatalf("failed add changed project files\nbefore=%v\nafter=%v", before, after)
	}
}

func assertNoAddStageSiblings(t *testing.T, root string) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Dir(root))
	if err != nil {
		t.Fatal(err)
	}
	prefix := "." + filepath.Base(root) + "-kpro-add-"
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), prefix) {
			t.Fatalf("staging sibling was not removed: %s", filepath.Join(filepath.Dir(root), entry.Name()))
		}
	}
}
