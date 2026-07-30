package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/saramand9/kpro/internal/codegen"
	"github.com/saramand9/kpro/internal/codegen/manifest"
)

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
