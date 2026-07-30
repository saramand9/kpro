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

const crudSpec = `openapi: 3.0.3
info:
  title: CRUD Pets
  version: 1.0.0
paths:
  /pets:
    post:
      operationId: createPet
      parameters:
        - name: source
          in: query
          required: true
          schema: { type: string }
      requestBody:
        required: true
        content:
          application/json:
            schema: { $ref: '#/components/schemas/PetInput' }
      responses:
        '201':
          description: created pet
          content:
            application/json:
              schema: { $ref: '#/components/schemas/Pet' }
  /pets/{id}:
    get:
      operationId: getPet
      parameters:
        - name: id
          in: path
          required: true
          schema: { type: integer }
      responses:
        '200':
          description: pet
          content:
            application/json:
              schema: { $ref: '#/components/schemas/Pet' }
    put:
      operationId: updatePet
      parameters:
        - name: id
          in: path
          required: true
          schema: { type: integer }
      requestBody:
        required: true
        content:
          application/json:
            schema: { $ref: '#/components/schemas/PetInput' }
      responses:
        '200':
          description: updated pet
          content:
            application/json:
              schema: { $ref: '#/components/schemas/Pet' }
    patch:
      operationId: patchPet
      parameters:
        - name: id
          in: path
          required: true
          schema: { type: integer }
      requestBody:
        required: true
        content:
          application/json:
            schema: { $ref: '#/components/schemas/PetPatch' }
      responses:
        '200':
          description: patched pet
          content:
            application/json:
              schema: { $ref: '#/components/schemas/Pet' }
    delete:
      operationId: deletePet
      parameters:
        - name: id
          in: path
          required: true
          schema: { type: integer }
      responses:
        '204': { description: deleted }
components:
  schemas:
    Pet:
      type: object
      required: [id, name]
      properties:
        id: { type: integer }
        name: { type: string }
    PetInput:
      type: object
      required: [name]
      properties:
        name: { type: string }
    PetPatch:
      type: object
      properties:
        name: { type: string }
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

	head := strings.Replace(sampleSpec, "    get:\n      operationId: health", "    head:\n      operationId: health", 1)
	doc, err := Parse([]byte(head))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = BuildPlan(doc, Options{Module: "example.com/generated"}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("HEAD error = %v", err)
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

func TestCRUDGeneratedRoutesCompileAndBind(t *testing.T) {
	doc, err := Parse([]byte(crudSpec))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	for _, operation := range doc.Operations {
		if operation.Request != nil && !operation.RequestRequired {
			t.Fatalf("operation %s lost required request state", operation.ID)
		}
	}
	plan, err := BuildPlan(doc, Options{Module: "example.com/generated"})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}

	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for _, file := range plan.Files {
		target := filepath.Join(dir, filepath.FromSlash(file.Path))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, file.Content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	generatedTest := `package kprohttp

import (
    "context"
    "net/http"
    "net/http/httptest"
    "strings"
    "testing"

    contract "example.com/generated/internal/openapi/contract"
    types "example.com/generated/internal/openapi/types"
    "github.com/saramand9/kpro/transport/httpx"
)

type recordingAPI struct {
    source string
    id int64
    name string
}

var _ contract.API = (*recordingAPI)(nil)

func (api *recordingAPI) CreatePet(_ context.Context, params contract.CreatePetParams, request types.PetInput) (types.Pet, error) {
    api.source, api.name = params.Source, request.Name
    return types.Pet{Id: 1, Name: request.Name}, nil
}
func (api *recordingAPI) GetPet(_ context.Context, params contract.GetPetParams) (types.Pet, error) {
    api.id = params.Id
    return types.Pet{Id: params.Id, Name: "found"}, nil
}
func (api *recordingAPI) UpdatePet(_ context.Context, params contract.UpdatePetParams, request types.PetInput) (types.Pet, error) {
    api.id, api.name = params.Id, request.Name
    return types.Pet{Id: params.Id, Name: request.Name}, nil
}
func (api *recordingAPI) PatchPet(_ context.Context, params contract.PatchPetParams, request types.PetPatch) (types.Pet, error) {
    api.id = params.Id
    if request.Name != nil { api.name = *request.Name }
    return types.Pet{Id: params.Id, Name: api.name}, nil
}
func (api *recordingAPI) DeletePet(_ context.Context, params contract.DeletePetParams) error {
    api.id = params.Id
    return nil
}

func request(endpoint *httpx.Endpoint, method, target, contentType, body string) *httptest.ResponseRecorder {
    req := httptest.NewRequest(method, target, strings.NewReader(body))
    if contentType != "" { req.Header.Set("Content-Type", contentType) }
    recorder := httptest.NewRecorder()
    endpoint.ServeHTTP(recorder, req)
    return recorder
}

func TestGeneratedBindings(t *testing.T) {
    var typedNil *recordingAPI
    if err := Register(httpx.New(httpx.Config{}), typedNil); err == nil {
        t.Fatal("Register accepted a typed-nil API")
    }
    api := &recordingAPI{}
    endpoint := httpx.New(httpx.Config{})
    if err := Register(endpoint, api); err != nil { t.Fatal(err) }

    response := request(endpoint, http.MethodPost, "/pets?source=import", "application/json; charset=utf-8", ` + "`" + `{"name":"Milo"}` + "`" + `)
    if response.Code != http.StatusCreated || api.source != "import" || api.name != "Milo" {
        t.Fatalf("POST = status %d source %q name %q", response.Code, api.source, api.name)
    }
    response = request(endpoint, http.MethodPut, "/pets/7", "application/json", ` + "`" + `{"name":"Otis"}` + "`" + `)
    if response.Code != http.StatusOK || api.id != 7 || api.name != "Otis" {
        t.Fatalf("PUT = status %d id %d name %q", response.Code, api.id, api.name)
    }
    response = request(endpoint, http.MethodPatch, "/pets/8", "application/json", ` + "`" + `{"name":"Pip"}` + "`" + `)
    if response.Code != http.StatusOK || api.id != 8 || api.name != "Pip" {
        t.Fatalf("PATCH = status %d id %d name %q", response.Code, api.id, api.name)
    }
    response = request(endpoint, http.MethodPatch, "/pets/10", "application/json", ` + "`" + `{}` + "`" + `)
    if response.Code != http.StatusOK || api.id != 10 || api.name != "Pip" {
        t.Fatalf("empty PATCH = status %d id %d name %q", response.Code, api.id, api.name)
    }
    response = request(endpoint, http.MethodDelete, "/pets/9", "", "")
    if response.Code != http.StatusNoContent || api.id != 9 {
        t.Fatalf("DELETE = status %d id %d", response.Code, api.id)
    }
    response = request(endpoint, http.MethodPost, "/pets?source=import", "application/json", ` + "`" + `{"name":` + "`" + `)
    if response.Code != http.StatusBadRequest { t.Fatalf("invalid JSON status = %d", response.Code) }
    response = request(endpoint, http.MethodPost, "/pets?source=import", "text/plain", ` + "`" + `{"name":"Milo"}` + "`" + `)
    if response.Code != http.StatusUnsupportedMediaType { t.Fatalf("content type status = %d", response.Code) }

    limited := httpx.New(httpx.Config{MaxBodyBytes: 8})
    if err := Register(limited, &recordingAPI{}); err != nil { t.Fatal(err) }
    response = request(limited, http.MethodPost, "/pets?source=import", "application/json", ` + "`" + `{"name":"too long"}` + "`" + `)
    if response.Code != http.StatusRequestEntityTooLarge { t.Fatalf("oversize status = %d body %q", response.Code, response.Body.String()) }
}
`
	testPath := filepath.Join(dir, "internal", "openapi", "kprohttp", "routes_test.go")
	if err := os.WriteFile(testPath, []byte(generatedTest), 0o644); err != nil {
		t.Fatal(err)
	}
	mod := "module example.com/generated\n\ngo 1.25.12\n\nrequire github.com/saramand9/kpro v0.0.0\n\nreplace github.com/saramand9/kpro => " + filepath.ToSlash(root) + "\n"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(mod), 0o644); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("go", "test", "./...")
	command.Dir = dir
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("generated CRUD packages/tests fail: %v\n%s", err, output)
	}
}

func TestRequestBodySubsetRejectsOptionalAndInlineSchemas(t *testing.T) {
	missing := strings.Replace(crudSpec, "      requestBody:\n        required: true\n        content:\n          application/json:\n            schema: { $ref: '#/components/schemas/PetInput' }\n", "", 1)
	if _, err := Parse([]byte(missing)); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("missing POST request error = %v", err)
	}
	optional := strings.Replace(crudSpec, "      requestBody:\n        required: true", "      requestBody:\n        required: false", 1)
	if _, err := Parse([]byte(optional)); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("optional request error = %v", err)
	}
	inline := strings.Replace(crudSpec, "schema: { $ref: '#/components/schemas/PetInput' }", "schema: { type: object, properties: { name: { type: string } } }", 1)
	if _, err := Parse([]byte(inline)); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("inline request error = %v", err)
	}
	getBody := strings.Replace(sampleSpec, "      responses:\n        '200':", "      requestBody:\n        required: true\n        content:\n          application/json:\n            schema: { $ref: '#/components/schemas/Pet' }\n      responses:\n        '200':", 1)
	if _, err := Parse([]byte(getBody)); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("GET body error = %v", err)
	}
}

func TestResponseRepresentationsAreStrict(t *testing.T) {
	extraRepresentation := strings.Replace(sampleSpec, `            application/json:
              schema: { $ref: '#/components/schemas/Pet' }`, `            application/json:
              schema: { $ref: '#/components/schemas/Pet' }
            application/xml:
              schema: { $ref: '#/components/schemas/Pet' }`, 1)
	if _, err := Parse([]byte(extraRepresentation)); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("extra response representation error = %v", err)
	}

	contentOnNoContent := strings.Replace(sampleSpec, `        '204': { description: healthy }`, `        '204':
          description: healthy
          content:
            application/json:
              schema: { $ref: '#/components/schemas/Pet' }`, 1)
	if _, err := Parse([]byte(contentOnNoContent)); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("204 content error = %v", err)
	}

	multipleSuccess := strings.Replace(sampleSpec, `        '204': { description: healthy }`, `        '200':
          description: healthy payload
          content:
            application/json:
              schema: { $ref: '#/components/schemas/Pet' }
        '204': { description: healthy }`, 1)
	if _, err := Parse([]byte(multipleSuccess)); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("multiple success responses error = %v", err)
	}

	unsupportedSuccess := strings.Replace(sampleSpec, `              schema: { $ref: '#/components/schemas/Pet' }`, `              schema: { $ref: '#/components/schemas/Pet' }
        '206':
          description: partial pet
          content:
            application/json:
              schema: { $ref: '#/components/schemas/Pet' }`, 1)
	if _, err := Parse([]byte(unsupportedSuccess)); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("unsupported success response error = %v", err)
	}

	wildcardSuccess := strings.Replace(sampleSpec, `'200':`, `'2XX':`, 1)
	if _, err := Parse([]byte(wildcardSuccess)); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("wildcard success response error = %v", err)
	}
}
