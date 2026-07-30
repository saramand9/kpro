package openapi

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/saramand9/kpro/internal/codegen"
)

const sampleSpec = `openapi: 3.0.3
info:
  title: Pets
  version: 1.0.0
paths:
  /pets/{id}:
    get:
      operationId: getPet
      parameters:
        - name: id
          in: path
          required: true
          schema: { type: integer }
        - name: verbose
          in: query
          schema: { type: boolean }
        - name: X-Trace
          in: header
          schema: { type: string }
      responses:
        '200':
          description: pet
          content:
            application/json:
              schema: { $ref: '#/components/schemas/Pet' }
  /health:
    get:
      operationId: health
      responses:
        '204': { description: healthy }
components:
  schemas:
    Pet:
      type: object
      required: [id]
      properties:
        id: { type: integer }
        name: { type: string }
        tags:
          type: array
          items: { type: string }
`

func TestBuildPlanDeterministicAndCompiles(t *testing.T) {
	doc, err := Parse([]byte(sampleSpec))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	options := Options{Module: "example.com/generated"}
	first, err := BuildPlan(doc, options)
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	second, err := BuildPlan(doc, options)
	if err != nil {
		t.Fatalf("second BuildPlan: %v", err)
	}
	if len(first.Files) != 4 || len(second.Files) != len(first.Files) {
		t.Fatalf("files = %d", len(first.Files))
	}
	for i := range first.Files {
		if first.Files[i].Path != second.Files[i].Path || !bytes.Equal(first.Files[i].Content, second.Files[i].Content) {
			t.Fatalf("non-deterministic file %d", i)
		}
	}
	var scaffoldFound bool
	for _, file := range first.Files {
		if file.Path == "internal/openapi/service/api.go" {
			scaffoldFound = file.Ownership == codegen.Scaffold
		}
	}
	if !scaffoldFound {
		t.Fatal("service scaffold missing or not create-once")
	}
	contract := string(first.Files[0].Content) + string(first.Files[1].Content) + string(first.Files[2].Content)
	if !strings.Contains(contract, "HandleChecked") || !strings.Contains(contract, "Verbose *bool") || !strings.Contains(contract, "ctx.WriteHeader(http.StatusNoContent)") {
		t.Fatalf("expected generated adapter and optional bindings:\n%s", contract)
	}

	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for _, file := range first.Files {
		target := filepath.Join(dir, filepath.FromSlash(file.Path))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, file.Content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mod := "module example.com/generated\n\ngo 1.25.12\n\nrequire github.com/saramand9/kpro v0.0.0\n\nreplace github.com/saramand9/kpro => " + filepath.ToSlash(root) + "\n"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(mod), 0o644); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("go", "test", "./...")
	command.Dir = dir
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("generated package does not compile: %v\n%s", err, output)
	}
}

func TestUnsupportedAndConflictingInput(t *testing.T) {
	_, err := Parse([]byte(`openapi: 3.0.3
info: { title: x, version: v }
paths: {}
components:
  schemas:
    Pet: { $ref: './pet.yaml#/Pet' }
`))
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("external ref error = %v", err)
	}

	if _, err = Parse([]byte(strings.Replace(sampleSpec, "    get:\n      operationId: health", "    get:\n      operationId: getPet", 1))); err == nil || !strings.Contains(err.Error(), "duplicate operationId") {
		t.Fatalf("duplicate operationId error = %v", err)
	}

	post := strings.Replace(sampleSpec, "    get:\n      operationId: health", "    post:\n      operationId: health", 1)
	doc, err := Parse([]byte(post))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = BuildPlan(doc, Options{Module: "example.com/generated"}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("POST error = %v", err)
	}

	inline := strings.Replace(sampleSpec, "schema: { $ref: '#/components/schemas/Pet' }", "schema: { type: string }", 1)
	if _, err = Parse([]byte(inline)); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("inline response error = %v", err)
	}
}

func TestRouteAndBindingSemanticsAreValidated(t *testing.T) {
	tests := []struct {
		name        string
		spec        string
		unsupported bool
	}{
		{
			name: "missing path parameter",
			spec: strings.Replace(sampleSpec, "        - name: id\n          in: path\n          required: true\n          schema: { type: integer }\n", "", 1),
		},
		{
			name: "orphan path parameter",
			spec: strings.Replace(sampleSpec, "        - name: id\n", "        - name: petID\n", 1),
		},
		{
			name: "path parameter not required",
			spec: strings.Replace(sampleSpec, "          required: true\n", "", 1),
		},
		{
			name: "malformed template",
			spec: strings.Replace(sampleSpec, "/pets/{id}:", "/pets/{id:", 1),
		},
		{
			name: "duplicate template",
			spec: strings.Replace(sampleSpec, "/pets/{id}:", "/pets/{id}/{id}:", 1),
		},
		{
			name: "normalized parameter field collision",
			spec: strings.Replace(sampleSpec, "        - name: verbose\n", "        - name: id\n", 1),
		},
		{
			name:        "document security",
			spec:        strings.Replace(sampleSpec, "  version: 1.0.0\n", "  version: 1.0.0\nsecurity:\n  - bearerAuth: []\n", 1),
			unsupported: true,
		},
		{
			name:        "operation security",
			spec:        strings.Replace(sampleSpec, "      operationId: getPet\n", "      operationId: getPet\n      security:\n        - bearerAuth: []\n", 1),
			unsupported: true,
		},
		{
			name:        "parameter serialization",
			spec:        strings.Replace(sampleSpec, "          in: query\n", "          in: query\n          style: form\n", 1),
			unsupported: true,
		},
		{
			name:        "parameter allow empty value",
			spec:        strings.Replace(sampleSpec, "          in: query\n", "          in: query\n          allowEmptyValue: true\n", 1),
			unsupported: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Parse([]byte(test.spec))
			if err == nil {
				t.Fatal("Parse error = nil")
			}
			if test.unsupported && !errors.Is(err, ErrUnsupported) {
				t.Fatalf("Parse error = %v, want ErrUnsupported", err)
			}
		})
	}
}

func TestUnsupportedMethodsCallbacksAndEmptyDocumentsAreVisible(t *testing.T) {
	for _, method := range []string{"options", "trace"} {
		spec := strings.Replace(sampleSpec, "    get:\n      operationId: health", "    "+method+":\n      operationId: health", 1)
		doc, err := Parse([]byte(spec))
		if err != nil {
			t.Fatalf("Parse %s: %v", method, err)
		}
		if _, err = BuildPlan(doc, Options{Module: "example.com/generated"}); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("BuildPlan %s error = %v, want ErrUnsupported", method, err)
		}
	}

	_, err := Parse([]byte(`openapi: 3.0.3
info: { title: empty, version: v1 }
paths: {}
`))
	if err == nil || !strings.Contains(err.Error(), "no operations") {
		t.Fatalf("empty document error = %v", err)
	}

	callback := strings.Replace(sampleSpec, "      responses:\n        '200':", `      callbacks:
        onEvent:
          '{$request.query.url}':
            post:
              responses:
                '204': { description: callback complete }
      responses:
        '200':`, 1)
	if _, err = Parse([]byte(callback)); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("callback error = %v, want ErrUnsupported", err)
	}
}
