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
      required: [id, name, tags, matrix, envelopes]
      properties:
        id: { type: integer }
        name: { type: string }
        tags: { $ref: '#/components/schemas/TagList' }
        matrix: { $ref: '#/components/schemas/StringMatrix' }
        envelopes: { $ref: '#/components/schemas/EnvelopeList' }
    PetInput:
      type: object
      required: [name]
      properties:
        name: { type: string }
    PetPatch:
      type: object
      properties:
        name: { type: string }
        tags: { $ref: '#/components/schemas/TagList' }
        matrix: { $ref: '#/components/schemas/StringMatrix' }
        envelopes: { $ref: '#/components/schemas/EnvelopeList' }
        tree: { $ref: '#/components/schemas/Tree' }
    TagList:
      type: array
      items: { type: string }
    StringMatrix:
      type: array
      items:
        type: array
        items: { type: string }
    Envelope:
      type: object
      required: [tags]
      properties:
        tags: { $ref: '#/components/schemas/TagList' }
    EnvelopeList:
      type: array
      items: { $ref: '#/components/schemas/Envelope' }
    Tree:
      type: array
      items: { $ref: '#/components/schemas/Tree' }
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
	if len(first.Files) != 5 || len(second.Files) != len(first.Files) {
		t.Fatalf("files = %d", len(first.Files))
	}
	for i := range first.Files {
		if first.Files[i].Path != second.Files[i].Path || !bytes.Equal(first.Files[i].Content, second.Files[i].Content) {
			t.Fatalf("non-deterministic file %d", i)
		}
	}
	var scaffoldFound, clientFound bool
	for _, file := range first.Files {
		if file.Path == "internal/openapi/service/api.go" {
			scaffoldFound = file.Ownership == codegen.Scaffold
		}
		if file.Path == "internal/openapi/kprohttpclient/client.gen.go" {
			clientFound = file.Ownership == codegen.Generated
		}
	}
	if !scaffoldFound {
		t.Fatal("service scaffold missing or not create-once")
	}
	if !clientFound {
		t.Fatal("typed HTTP client missing or not generator-owned")
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
	if _, err = Parse([]byte(head)); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("HEAD error = %v", err)
	}

	inline := strings.Replace(sampleSpec, "schema: { $ref: '#/components/schemas/Pet' }", "schema: { type: string }", 1)
	if _, err = Parse([]byte(inline)); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("inline response error = %v", err)
	}

	nestedReference := strings.Replace(sampleSpec, "#/components/schemas/Pet", "#/components/schemas/Pet/properties/id", 1)
	if _, err = Parse([]byte(nestedReference)); !errors.Is(err, ErrUnsupported) || !strings.Contains(err.Error(), "exactly one component schema token") {
		t.Fatalf("nested component reference error = %v", err)
	}

	encodedNestedReference := strings.Replace(sampleSpec, "#/components/schemas/Pet", "#/components/schemas/Pet%2Fproperties%2Fid", 1)
	if _, err = Parse([]byte(encodedNestedReference)); !errors.Is(err, ErrUnsupported) || !strings.Contains(err.Error(), "exactly one component schema token") {
		t.Fatalf("encoded nested component reference error = %v", err)
	}
}

func TestLocalSchemaReferenceDecodesJSONPointerToken(t *testing.T) {
	doc, err := Parse([]byte(`openapi: 3.0.3
info: { title: escaped, version: v1 }
paths:
  /escaped:
    get:
      operationId: getEscaped
      responses:
        '200':
          description: escaped
          content:
            application/json:
              schema: { $ref: '#/components/schemas/Pet~1Envelope' }
components:
  schemas:
    Pet/Envelope:
      type: object
      required: [id]
      properties:
        id: { type: integer }
`))
	if err != nil {
		t.Fatalf("Parse escaped component reference: %v", err)
	}
	if len(doc.Components) != 1 || doc.Components[0].Name != "PetEnvelope" ||
		len(doc.Operations) != 1 || doc.Operations[0].Response == nil || doc.Operations[0].Response.Name != "PetEnvelope" {
		t.Fatalf("escaped component lowering = %#v", doc)
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
		{
			name:        "numeric constraint",
			spec:        strings.Replace(sampleSpec, "schema: { type: integer }", "schema: { type: integer, minimum: 1 }", 1),
			unsupported: true,
		},
		{
			name:        "string constraint",
			spec:        strings.Replace(sampleSpec, "name: { type: string }", "name: { type: string, pattern: '^[a-z]+$' }", 1),
			unsupported: true,
		},
		{
			name:        "unsupported scalar format",
			spec:        strings.Replace(sampleSpec, "schema: { type: integer }", "schema: { type: integer, format: int32 }", 1),
			unsupported: true,
		},
		{
			name:        "undeclared required property",
			spec:        strings.Replace(sampleSpec, "required: [id]", "required: [id, missing]", 1),
			unsupported: true,
		},
		{
			name: "generated JSON method field collision",
			spec: strings.Replace(sampleSpec, "name: { type: string }", "unmarshalJSON: { type: string }", 1),
		},
		{
			name: "generated JSON validator field collision",
			spec: strings.Replace(sampleSpec, "name: { type: string }", "validateJSON: { type: string }", 1),
		},
		{
			name:        "property cannot round trip through JSON tag",
			spec:        strings.Replace(sampleSpec, "name: { type: string }", `"a,b": { type: string }`, 1),
			unsupported: true,
		},
		{
			name:        "parameter cannot round trip through JSON tag",
			spec:        strings.Replace(sampleSpec, "        - name: verbose\n", "        - name: verbose,raw\n", 1),
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

func TestBuildPlanRejectsConflictingRoutesAndRequiredReferenceCycles(t *testing.T) {
	t.Run("equivalent wildcard routes", func(t *testing.T) {
		doc := Document{
			Components: []Schema{{Name: "Pet", Kind: Object}},
			Operations: []Operation{
				{
					ID: "GetPetByID", Method: "GET", Path: "/pets/{id}",
					Parameters: []Parameter{{Name: "id", GoName: "ID", In: "path", Type: TypeRef{Kind: String}, Required: true}},
					Response:   &TypeRef{Name: "Pet"}, Status: 200,
				},
				{
					ID: "GetPetByName", Method: "GET", Path: "/pets/{name}",
					Parameters: []Parameter{{Name: "name", GoName: "Name", In: "path", Type: TypeRef{Kind: String}, Required: true}},
					Response:   &TypeRef{Name: "Pet"}, Status: 200,
				},
			},
		}
		if _, err := BuildPlan(doc, Options{Module: "example.com/generated"}); err == nil || !strings.Contains(err.Error(), "conflicts") {
			t.Fatalf("BuildPlan route conflict error = %v", err)
		}
	})

	t.Run("required object reference cycle", func(t *testing.T) {
		doc := Document{
			Components: []Schema{
				{Name: "Parent", Kind: Object, Properties: []Field{{Name: "Child", JSONName: "child", Type: TypeRef{Name: "Child"}, Required: true}}},
				{Name: "Child", Kind: Object, Properties: []Field{{Name: "Parent", JSONName: "parent", Type: TypeRef{Name: "Parent"}, Required: true}}},
			},
			Operations: []Operation{{ID: "GetParent", Method: "GET", Path: "/parent", Response: &TypeRef{Name: "Parent"}, Status: 200}},
		}
		if _, err := BuildPlan(doc, Options{Module: "example.com/generated"}); !errors.Is(err, ErrUnsupported) || !strings.Contains(err.Error(), "Parent -> Child -> Parent") {
			t.Fatalf("BuildPlan required cycle error = %v", err)
		}
	})
}

func TestUnsupportedMethodsCallbacksAndEmptyDocumentsAreVisible(t *testing.T) {
	for _, method := range []string{"options", "trace"} {
		spec := strings.Replace(sampleSpec, "    get:\n      operationId: health", "    "+method+":\n      operationId: health", 1)
		if _, err := Parse([]byte(spec)); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("Parse %s error = %v, want ErrUnsupported", method, err)
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
    return types.Pet{Id: 1, Name: request.Name, Tags: types.TagList{}, Matrix: types.StringMatrix{{}}, Envelopes: types.EnvelopeList{}}, nil
}
func (api *recordingAPI) GetPet(_ context.Context, params contract.GetPetParams) (types.Pet, error) {
    api.id = params.Id
    tags := types.TagList{}
    matrix := types.StringMatrix{{}}
    envelopes := types.EnvelopeList{}
    if params.Id == 96 { envelopes = types.EnvelopeList{{Tags: nil}} }
    if params.Id == 98 { matrix = types.StringMatrix{nil} }
    if params.Id == 99 { tags = nil }
    return types.Pet{Id: params.Id, Name: "found", Tags: tags, Matrix: matrix, Envelopes: envelopes}, nil
}
func (api *recordingAPI) UpdatePet(_ context.Context, params contract.UpdatePetParams, request types.PetInput) (types.Pet, error) {
    api.id, api.name = params.Id, request.Name
    return types.Pet{Id: params.Id, Name: request.Name, Tags: types.TagList{}, Matrix: types.StringMatrix{{}}, Envelopes: types.EnvelopeList{}}, nil
}
func (api *recordingAPI) PatchPet(_ context.Context, params contract.PatchPetParams, request types.PetPatch) (types.Pet, error) {
    api.id = params.Id
    if request.Name != nil { api.name = *request.Name }
    return types.Pet{Id: params.Id, Name: api.name, Tags: types.TagList{}, Matrix: types.StringMatrix{{}}, Envelopes: types.EnvelopeList{}}, nil
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
	api.name = "unchanged"
	response = request(endpoint, http.MethodPost, "/pets?source=import", "application/json", ` + "`" + `{}` + "`" + `)
	if response.Code != http.StatusBadRequest || api.name != "unchanged" {
		t.Fatalf("missing required field = status %d name %q", response.Code, api.name)
	}
	response = request(endpoint, http.MethodPost, "/pets?source=import", "application/json", ` + "`" + `{"name":null}` + "`" + `)
	if response.Code != http.StatusBadRequest || api.name != "unchanged" {
		t.Fatalf("null required field = status %d name %q", response.Code, api.name)
	}
	response = request(endpoint, http.MethodPost, "/pets?source=import", "application/json", ` + "`" + `{"Name":"Milo"}` + "`" + `)
	if response.Code != http.StatusBadRequest || api.name != "unchanged" {
		t.Fatalf("case-mismatched required field = status %d name %q", response.Code, api.name)
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
	response = request(endpoint, http.MethodPatch, "/pets/11", "application/json", ` + "`" + `null` + "`" + `)
	if response.Code != http.StatusBadRequest || api.id != 10 || api.name != "Pip" {
		t.Fatalf("null PATCH = status %d id %d name %q", response.Code, api.id, api.name)
	}
	response = request(endpoint, http.MethodPatch, "/pets/11", "application/json", ` + "`" + `{"name":null}` + "`" + `)
	if response.Code != http.StatusBadRequest || api.id != 10 || api.name != "Pip" {
		t.Fatalf("null optional field = status %d id %d name %q", response.Code, api.id, api.name)
	}
	response = request(endpoint, http.MethodPatch, "/pets/11", "application/json", ` + "`" + `{"tags":[null]}` + "`" + `)
	if response.Code != http.StatusBadRequest || api.id != 10 || api.name != "Pip" {
		t.Fatalf("null scalar array item = status %d id %d name %q", response.Code, api.id, api.name)
	}
	response = request(endpoint, http.MethodPatch, "/pets/11", "application/json", ` + "`" + `{"matrix":[[null]]}` + "`" + `)
	if response.Code != http.StatusBadRequest || api.id != 10 || api.name != "Pip" {
		t.Fatalf("null nested array item = status %d id %d name %q", response.Code, api.id, api.name)
	}
	response = request(endpoint, http.MethodPatch, "/pets/11", "application/json", ` + "`" + `{"envelopes":[null]}` + "`" + `)
	if response.Code != http.StatusBadRequest || api.id != 10 || api.name != "Pip" {
		t.Fatalf("null referenced array item = status %d id %d name %q", response.Code, api.id, api.name)
	}
	response = request(endpoint, http.MethodPatch, "/pets/11", "application/json", ` + "`" + `{"envelopes":[{"tags":[null]}]}` + "`" + `)
	if response.Code != http.StatusBadRequest || api.id != 10 || api.name != "Pip" {
		t.Fatalf("null nested referenced field item = status %d id %d name %q", response.Code, api.id, api.name)
	}
	response = request(endpoint, http.MethodPatch, "/pets/13", "application/json", ` + "`" + `{"tree":[[]]}` + "`" + `)
	if response.Code != http.StatusOK || api.id != 13 || api.name != "Pip" {
		t.Fatalf("recursive array = status %d id %d name %q", response.Code, api.id, api.name)
	}
	response = request(endpoint, http.MethodPatch, "/pets/13", "application/json", ` + "`" + `{"tree":[null]}` + "`" + `)
	if response.Code != http.StatusBadRequest || api.id != 13 || api.name != "Pip" {
		t.Fatalf("null recursive array item = status %d id %d name %q", response.Code, api.id, api.name)
	}
	response = request(endpoint, http.MethodPatch, "/pets/12", "application/json", ` + "`" + `{"Name":"wrong case"}` + "`" + `)
	if response.Code != http.StatusOK || api.id != 12 || api.name != "Pip" {
		t.Fatalf("case-mismatched optional field = status %d id %d name %q", response.Code, api.id, api.name)
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

    response = request(endpoint, http.MethodGet, "/pets/96", "", "")
    if response.Code != http.StatusInternalServerError {
        t.Fatalf("invalid nested referenced response status = %d body %q", response.Code, response.Body.String())
    }
    response = request(endpoint, http.MethodGet, "/pets/98", "", "")
    if response.Code != http.StatusInternalServerError {
        t.Fatalf("invalid nested-array response status = %d body %q", response.Code, response.Body.String())
    }
    response = request(endpoint, http.MethodGet, "/pets/99", "", "")
    if response.Code != http.StatusInternalServerError {
        t.Fatalf("invalid nil-array response status = %d body %q", response.Code, response.Body.String())
    }
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
