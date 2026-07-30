package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/saramand9/kpro/internal/codegen"
	"github.com/saramand9/kpro/internal/codegen/manifest"
)

type pluginPlannerFunc func(context.Context, manifest.Manifest, manifest.Source, []byte) (codegen.Plan, error)

func (f pluginPlannerFunc) Plan(ctx context.Context, value manifest.Manifest, source manifest.Source, content []byte) (codegen.Plan, error) {
	return f(ctx, value, source, content)
}

const testCodegenManifest = `apiVersion: kpro.dev/codegen/v1alpha1
module: example.com/orders
service: orders
sources:
  - id: public-api
    kind: openapi
    path: api/orders.openapi.yaml
    package: internal/modules/orders
`

const testOpenAPISpec = `openapi: 3.0.3
info:
  title: Orders API
  version: 1.0.0
paths:
  /orders/{id}:
    get:
      operationId: getOrder
      parameters:
        - name: id
          in: path
          required: true
          schema:
            type: string
      responses:
        "200":
          description: Order found
          content:
            application/json:
              schema:
                $ref: "#/components/schemas/Order"
components:
  schemas:
    Order:
      type: object
      required:
        - id
      properties:
        id:
          type: string
`

func TestValidateLoadsStrictManifestFromRootAndConfig(t *testing.T) {
	root := t.TempDir()
	config := filepath.Join(root, "config", "orders.yaml")
	if err := os.Mkdir(filepath.Dir(config), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config, []byte(testCodegenManifest), 0o644); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	if err := execute([]string{"validate", "--root", root, "--config", "config/orders.yaml"}, &output, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "source public-api: openapi api/orders.openapi.yaml -> internal/modules/orders") {
		t.Fatalf("validate output = %q", output.String())
	}
}

func TestValidateRejectsStrictManifest(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, defaultCodegenConfig), []byte(strings.Replace(testCodegenManifest, "service: orders", "service: orders\nunknown: true", 1)), 0o644); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	err := execute([]string{"validate", "--root", root}, &output, &output)
	if err == nil || !strings.Contains(err.Error(), "field unknown not found") {
		t.Fatalf("validate error = %v", err)
	}
}

func TestCodegenCommandsRejectUnexpectedArguments(t *testing.T) {
	for _, command := range []string{"validate", "generate"} {
		t.Run(command, func(t *testing.T) {
			var output bytes.Buffer
			err := execute([]string{command, "extra"}, &output, &output)
			if err == nil || !strings.Contains(err.Error(), "usage: kpro "+command) {
				t.Fatalf("%s error = %v", command, err)
			}
		})
	}
}

func TestGenerateRejectsMutuallyExclusiveModesBeforeLoadingManifest(t *testing.T) {
	var output bytes.Buffer
	err := execute([]string{"generate", "--check", "--dry-run"}, &output, &output)
	if err == nil || !strings.Contains(err.Error(), "cannot be used together") {
		t.Fatalf("generate error = %v", err)
	}
}

func TestGenerateReportsUnsupportedSourceAfterValidation(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, defaultCodegenConfig), []byte(testCodegenManifest), 0o644); err != nil {
		t.Fatal(err)
	}

	generator, registered := sourceGenerators[manifest.KindOpenAPI]
	delete(sourceGenerators, manifest.KindOpenAPI)
	t.Cleanup(func() {
		if registered {
			sourceGenerators[manifest.KindOpenAPI] = generator
			return
		}
		delete(sourceGenerators, manifest.KindOpenAPI)
	})

	var output bytes.Buffer
	err := execute([]string{"generate", "--root", root}, &output, &output)
	if err == nil || !strings.Contains(err.Error(), `source "public-api": unsupported source kind "openapi"`) {
		t.Fatalf("generate error = %v", err)
	}
}

func TestGenerateOpenAPIApplyCheckAndProtectGeneratedFiles(t *testing.T) {
	root := t.TempDir()
	writeOpenAPICodegenProject(t, root)

	var output bytes.Buffer
	if err := execute([]string{"generate", "--root", root}, &output, &output); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		"internal/modules/orders/types/types.gen.go",
		"internal/modules/orders/contract/contract.gen.go",
		"internal/modules/orders/kprohttp/routes.gen.go",
		codegen.DefaultLockFile,
	} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(name))); err != nil {
			t.Fatalf("generated output %q: %v", name, err)
		}
	}
	if !strings.Contains(output.String(), "create internal/modules/orders/types/types.gen.go") {
		t.Fatalf("generate output = %q", output.String())
	}

	output.Reset()
	if err := execute([]string{"generate", "--root", root, "--check"}, &output, &output); err != nil {
		t.Fatalf("check generated outputs: %v", err)
	}

	generated := filepath.Join(root, "internal", "modules", "orders", "types", "types.gen.go")
	if err := os.WriteFile(generated, []byte("// edited outside codegen\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	err := execute([]string{"generate", "--root", root, "--check"}, &output, &output)
	if !errors.Is(err, codegen.ErrGeneratedModified) {
		t.Fatalf("modified generated output error = %v", err)
	}
}

func TestGenerateOpenAPIDryRunDoesNotWrite(t *testing.T) {
	root := t.TempDir()
	writeOpenAPICodegenProject(t, root)

	var output bytes.Buffer
	if err := execute([]string{"generate", "--root", root, "--dry-run"}, &output, &output); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		"internal/modules/orders/types/types.gen.go",
		"internal/modules/orders/contract/contract.gen.go",
		"internal/modules/orders/kprohttp/routes.gen.go",
		codegen.DefaultLockFile,
	} {
		_, err := os.Stat(filepath.Join(root, filepath.FromSlash(name)))
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("dry-run output %q exists or stat failed: %v", name, err)
		}
	}
	if !strings.Contains(output.String(), "create internal/modules/orders/types/types.gen.go") {
		t.Fatalf("dry-run output = %q", output.String())
	}
}

func TestGenerateUsesConfigScopedLockAndRejectsOutsideConfig(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "api"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "config", "orders.yaml"), []byte(testCodegenManifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "api", "orders.openapi.yaml"), []byte(testOpenAPISpec), 0o644); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	if err := execute([]string{"generate", "--root", root, "--config", "config/orders.yaml"}, &output, &output); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "config", "orders.lock.json")); err != nil {
		t.Fatalf("scoped generation lock: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, codegen.DefaultLockFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unexpected default lock: %v", err)
	}

	outside := filepath.Join(t.TempDir(), "outside.yaml")
	if err := os.WriteFile(outside, []byte(testCodegenManifest), 0o644); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	err := execute([]string{"validate", "--root", root, "--config", outside}, &output, &output)
	if err == nil || !strings.Contains(err.Error(), "must be inside root") {
		t.Fatalf("outside config error = %v", err)
	}
}

func TestPluginValidateDoesNotExecuteAndGenerateRequiresAuthorization(t *testing.T) {
	root := t.TempDir()
	value := pluginCodegenManifest("tools/missing-plugin")
	if err := os.WriteFile(filepath.Join(root, defaultCodegenConfig), []byte(value), 0o644); err != nil {
		t.Fatal(err)
	}

	calls := 0
	previousFactory := newPluginHost
	newPluginHost = func(string, []string) pluginPlanner {
		calls++
		return pluginPlannerFunc(func(context.Context, manifest.Manifest, manifest.Source, []byte) (codegen.Plan, error) {
			return codegen.Plan{}, errors.New("must not run")
		})
	}
	t.Cleanup(func() { newPluginHost = previousFactory })

	var output bytes.Buffer
	if err := execute([]string{"validate", "--root", root}, &output, &output); err != nil {
		t.Fatalf("validate plugin manifest: %v", err)
	}
	if calls != 0 {
		t.Fatalf("validate constructed %d plugin hosts", calls)
	}

	output.Reset()
	err := execute([]string{"generate", "--root", root}, &output, &output)
	if err == nil || !strings.Contains(err.Error(), "--allow-plugins") {
		t.Fatalf("generate without authorization error = %v", err)
	}
	if calls != 0 {
		t.Fatalf("unauthorized generate constructed %d plugin hosts", calls)
	}
}

func TestGenerateAuthorizedPluginPreservesExecutionModes(t *testing.T) {
	root := t.TempDir()
	value := pluginCodegenManifest("tools/test-plugin")
	if err := os.WriteFile(filepath.Join(root, defaultCodegenConfig), []byte(value), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "api"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "tools"), 0o755); err != nil {
		t.Fatal(err)
	}
	sourceContent := []byte("plugin contract bytes\n")
	if err := os.WriteFile(filepath.Join(root, "api", "orders.openapi.yaml"), sourceContent, 0o644); err != nil {
		t.Fatal(err)
	}
	pluginFilename := filepath.Join(root, "tools", "test-plugin")
	if err := os.WriteFile(pluginFilename, []byte("test seam executable placeholder"), 0o755); err != nil {
		t.Fatal(err)
	}

	plan, err := codegen.BuildPlan(codegen.File{
		Path:      "internal/plugin/plugin.gen.go",
		Content:   []byte("package plugin\n\nconst Generated = true\n"),
		Ownership: codegen.Generated,
	})
	if err != nil {
		t.Fatal(err)
	}
	var calls int
	var gotCommand string
	var gotArgs []string
	previousFactory := newPluginHost
	newPluginHost = func(command string, args []string) pluginPlanner {
		gotCommand = command
		gotArgs = append([]string(nil), args...)
		return pluginPlannerFunc(func(ctx context.Context, _ manifest.Manifest, source manifest.Source, content []byte) (codegen.Plan, error) {
			calls++
			if ctx == nil {
				t.Fatal("plugin context is nil")
			}
			if source.ID != "public-api" {
				t.Fatalf("plugin source = %#v", source)
			}
			if !bytes.Equal(content, sourceContent) {
				t.Fatalf("plugin source content = %q", content)
			}
			return plan, nil
		})
	}
	t.Cleanup(func() { newPluginHost = previousFactory })

	generated := filepath.Join(root, "internal", "plugin", "plugin.gen.go")
	var output bytes.Buffer
	if err := execute([]string{"generate", "--root", root, "--allow-plugins", "--dry-run"}, &output, &output); err != nil {
		t.Fatalf("plugin dry-run: %v", err)
	}
	if _, err := os.Stat(generated); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("plugin dry-run wrote output: %v", err)
	}
	if !strings.Contains(output.String(), "create internal/plugin/plugin.gen.go") {
		t.Fatalf("plugin dry-run output = %q", output.String())
	}

	output.Reset()
	if err := execute([]string{"generate", "--root", root, "--allow-plugins"}, &output, &output); err != nil {
		t.Fatalf("plugin apply: %v", err)
	}
	if _, err := os.Stat(generated); err != nil {
		t.Fatalf("plugin apply output: %v", err)
	}
	output.Reset()
	if err := execute([]string{"generate", "--root", root, "--allow-plugins", "--check"}, &output, &output); err != nil {
		t.Fatalf("plugin check: %v", err)
	}
	if calls != 3 {
		t.Fatalf("plugin Plan calls = %d, want 3", calls)
	}
	wantCommand, err := filepath.Abs(pluginFilename)
	if err != nil {
		t.Fatal(err)
	}
	if gotCommand != wantCommand {
		t.Fatalf("plugin command = %q, want %q", gotCommand, wantCommand)
	}
	if len(gotArgs) != 1 || gotArgs[0] != "--strict" {
		t.Fatalf("plugin args = %#v", gotArgs)
	}
}

func TestValidateRejectsEscapingPluginCommand(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, defaultCodegenConfig), []byte(pluginCodegenManifest("../outside-plugin")), 0o644); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	err := execute([]string{"validate", "--root", root}, &output, &output)
	if err == nil || !strings.Contains(err.Error(), "unsafe command") {
		t.Fatalf("escaping plugin command error = %v", err)
	}
}

func TestResolvePluginCommandRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside-plugin")
	if err := os.WriteFile(outside, []byte("outside"), 0o755); err != nil {
		t.Fatal(err)
	}
	tools := filepath.Join(root, "tools")
	if err := os.MkdirAll(tools, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(tools, "plugin")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks are unavailable: %v", err)
	}
	_, err := resolvePluginCommand(root, "tools/plugin")
	if err == nil || !strings.Contains(err.Error(), "outside root") {
		t.Fatalf("symlink escape error = %v", err)
	}
}

func TestResolvePluginCommandUsesPATHForPlainName(t *testing.T) {
	want, err := exec.LookPath("go")
	if err != nil {
		t.Skipf("go is unavailable on PATH: %v", err)
	}
	want, err = filepath.Abs(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := resolvePluginCommand(t.TempDir(), "go")
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("resolved PATH command = %q, want %q", got, want)
	}
}

func TestGenerateBuiltInSourceDoesNotConstructPluginHost(t *testing.T) {
	root := t.TempDir()
	writeOpenAPICodegenProject(t, root)
	previousFactory := newPluginHost
	newPluginHost = func(string, []string) pluginPlanner {
		t.Fatal("built-in generator constructed a plugin host")
		return nil
	}
	t.Cleanup(func() { newPluginHost = previousFactory })

	var output bytes.Buffer
	if err := execute([]string{"generate", "--root", root, "--dry-run"}, &output, &output); err != nil {
		t.Fatal(err)
	}
}

func pluginCodegenManifest(command string) string {
	return strings.Replace(
		testCodegenManifest,
		"    package: internal/modules/orders",
		"    package: internal/modules/orders\n    plugin:\n      command: "+command+"\n      args: [--strict]",
		1,
	)
}

func TestGenerateMessageApplyAndCheck(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "api"), 0o755); err != nil {
		t.Fatal(err)
	}
	value := `apiVersion: kpro.dev/codegen/v1alpha1
module: example.com/generated
service: generated
sources:
  - id: session-messages
    kind: message
    path: api/messages.yaml
    package: internal/messages
`
	contract := `apiVersion: kpro.dev/message/v1alpha1
service: orders
encoding: json
messages:
  - id: 100
    name: create-order
    dispatch: sync
    request:
      fields:
        - name: order-id
          type: string
          required: true
    response:
      fields:
        - name: accepted
          type: bool
          required: true
`
	if err := os.WriteFile(filepath.Join(root, defaultCodegenConfig), []byte(value), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "api", "messages.yaml"), []byte(contract), 0o644); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	if err := execute([]string{"generate", "--root", root}, &output, &output); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		"internal/messages/types/messages_orders.gen.go",
		"internal/messages/contract/service_orders.gen.go",
		"internal/messages/kpromessage/routes_orders.gen.go",
		"internal/messages/service/orders.go",
		codegen.DefaultLockFile,
	} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(name))); err != nil {
			t.Fatalf("message output %q: %v", name, err)
		}
	}
	output.Reset()
	if err := execute([]string{"generate", "--root", root, "--check"}, &output, &output); err != nil {
		t.Fatalf("check message outputs: %v", err)
	}
}

func TestGenerateOpenAPIRejectsSymlinkSourceEscape(t *testing.T) {
	root := t.TempDir()
	writeOpenAPICodegenProject(t, root)
	outside := filepath.Join(t.TempDir(), "outside.openapi.yaml")
	if err := os.WriteFile(outside, []byte(testOpenAPISpec), 0o644); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "api", "orders.openapi.yaml")
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, source); err != nil {
		t.Skipf("symlinks are unavailable: %v", err)
	}

	var output bytes.Buffer
	err := execute([]string{"generate", "--root", root}, &output, &output)
	if err == nil || !strings.Contains(err.Error(), "outside root") {
		t.Fatalf("symlink source escape error = %v", err)
	}
}
func TestGenerateProtoWithInstalledToolchain(t *testing.T) {
	for _, tool := range []string{"protoc", "protoc-gen-go", "protoc-gen-go-grpc"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("optional protobuf CLI integration: %s is unavailable: %v", tool, err)
		}
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "api"), 0o755); err != nil {
		t.Fatal(err)
	}
	value := `apiVersion: kpro.dev/codegen/v1alpha1
module: example.com/generated
service: generated
sources:
  - id: public-rpc
    kind: proto
    path: api/greeter.proto
    package: internal/gen
`
	contract := `syntax = "proto3";
package generated.v1;
option go_package = "example.com/generated/internal/gen";
service Greeter { rpc Hello(Request) returns (Reply); }
message Request { string name = 1; }
message Reply { string message = 1; }
`
	if err := os.WriteFile(filepath.Join(root, defaultCodegenConfig), []byte(value), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "api", "greeter.proto"), []byte(contract), 0o644); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	if err := execute([]string{"generate", "--root", root}, &output, &output); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		"internal/gen/greeter.pb.go",
		"internal/gen/greeter_grpc.pb.go",
		"internal/gen/kpro_public_rpc_grpc.gen.go",
		"internal/gen/kpro_public_rpc_service.go",
		codegen.DefaultLockFile,
	} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(name))); err != nil {
			t.Fatalf("protobuf output %q: %v", name, err)
		}
	}
	output.Reset()
	if err := execute([]string{"generate", "--root", root, "--check"}, &output, &output); err != nil {
		t.Fatalf("check protobuf outputs: %v", err)
	}
}
func writeOpenAPICodegenProject(t *testing.T, root string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, defaultCodegenConfig), []byte(testCodegenManifest), 0o644); err != nil {
		t.Fatal(err)
	}
	spec := filepath.Join(root, "api", "orders.openapi.yaml")
	if err := os.MkdirAll(filepath.Dir(spec), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(spec, []byte(testOpenAPISpec), 0o644); err != nil {
		t.Fatal(err)
	}
}
