package openapi

import (
	"strings"
	"testing"
)

func validDirectDocument() Document {
	return Document{
		Title: "Widgets",
		Components: []Schema{
			{
				Name: "WidgetInput",
				Kind: Object,
				Properties: []Field{
					{Name: "Name", JSONName: "name", Type: TypeRef{Kind: String}, Required: true},
				},
			},
			{
				Name: "Widget",
				Kind: Object,
				Properties: []Field{
					{Name: "ID", JSONName: "id", Type: TypeRef{Kind: Integer}, Required: true},
				},
			},
		},
		Operations: []Operation{
			{
				ID:     "CreateWidget",
				Method: "POST",
				Path:   "/widgets/{id}",
				Parameters: []Parameter{
					{Name: "id", GoName: "ID", In: "path", Type: TypeRef{Kind: Integer}, Required: true},
					{Name: "X-Trace", GoName: "XTrace", In: "header", Type: TypeRef{Kind: String}},
				},
				Request:         &TypeRef{Name: "WidgetInput"},
				RequestRequired: true,
				Response:        &TypeRef{Name: "Widget"},
				Status:          201,
			},
			{
				ID:     "DeleteWidget",
				Method: "DELETE",
				Path:   "/widgets/{id}",
				Parameters: []Parameter{
					{Name: "id", GoName: "ID", In: "path", Type: TypeRef{Kind: Integer}, Required: true},
				},
				Status: 204,
			},
		},
	}
}

func TestDocumentValidateAcceptsCheckedDirectIR(t *testing.T) {
	doc := validDirectDocument()
	if err := doc.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestDocumentValidateRejectsMalformedDirectIR(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Document)
		want   string
	}{
		{
			name: "no operations",
			mutate: func(doc *Document) {
				doc.Operations = nil
			},
			want: "no operations",
		},
		{
			name: "duplicate component",
			mutate: func(doc *Document) {
				doc.Components = append(doc.Components, Schema{Name: "Widget", Kind: String})
			},
			want: "duplicate component",
		},
		{
			name: "invalid component Go name",
			mutate: func(doc *Document) {
				doc.Components[0].Name = "widget-input"
			},
			want: "valid Go identifier",
		},
		{
			name: "scalar with object fields",
			mutate: func(doc *Document) {
				doc.Components[0].Kind = String
			},
			want: "scalar must not declare",
		},
		{
			name: "array without items",
			mutate: func(doc *Document) {
				doc.Components[0].Kind = Array
				doc.Components[0].Properties = nil
			},
			want: "array has no items",
		},
		{
			name: "duplicate field Go name",
			mutate: func(doc *Document) {
				doc.Components[0].Properties = append(doc.Components[0].Properties,
					Field{Name: "Name", JSONName: "other", Type: TypeRef{Kind: String}})
			},
			want: "duplicate field",
		},
		{
			name: "invalid field Go name",
			mutate: func(doc *Document) {
				doc.Components[0].Properties[0].Name = "bad-name"
			},
			want: "valid Go identifier",
		},
		{
			name: "duplicate JSON field name",
			mutate: func(doc *Document) {
				doc.Components[0].Properties = append(doc.Components[0].Properties,
					Field{Name: "Other", JSONName: "name", Type: TypeRef{Kind: String}})
			},
			want: "duplicate JSON field",
		},
		{
			name: "encoding json ignored field name",
			mutate: func(doc *Document) {
				doc.Components[0].Properties[0].JSONName = "-"
			},
			want: "cannot be represented exactly by encoding/json",
		},
		{
			name: "field conflicts with generated method",
			mutate: func(doc *Document) {
				doc.Components[0].Properties[0].Name = "UnmarshalJSON"
			},
			want: "generated method",
		},
		{
			name: "missing component reference",
			mutate: func(doc *Document) {
				doc.Components[0].Properties[0].Type = TypeRef{Name: "Missing"}
			},
			want: "missing component",
		},
		{
			name: "mixed component reference and inline shape",
			mutate: func(doc *Document) {
				doc.Components[0].Properties[0].Type = TypeRef{Name: "Widget", Kind: Object}
			},
			want: "must not declare a kind",
		},
		{
			name: "inline object field",
			mutate: func(doc *Document) {
				doc.Components[0].Properties[0].Type = TypeRef{Kind: Object}
			},
			want: "inline object",
		},
		{
			name: "recursive inline array",
			mutate: func(doc *Document) {
				ref := &TypeRef{Kind: Array}
				ref.Items = ref
				doc.Components[0].Properties[0].Type = *ref
			},
			want: "recursive inline type",
		},
		{
			name: "duplicate operation ID",
			mutate: func(doc *Document) {
				doc.Operations[1].ID = doc.Operations[0].ID
			},
			want: "duplicate operation ID",
		},
		{
			name: "invalid operation Go name",
			mutate: func(doc *Document) {
				doc.Operations[0].ID = "create-widget"
			},
			want: "valid Go identifier",
		},
		{
			name: "unsupported method",
			mutate: func(doc *Document) {
				doc.Operations[1].Method = "HEAD"
			},
			want: "HTTP adapter for HEAD",
		},
		{
			name: "GET with request",
			mutate: func(doc *Document) {
				doc.Operations[0].Method = "GET"
			},
			want: "GET operation",
		},
		{
			name: "POST without request",
			mutate: func(doc *Document) {
				doc.Operations[0].Request = nil
				doc.Operations[0].RequestRequired = false
			},
			want: "requires a request body",
		},
		{
			name: "optional request",
			mutate: func(doc *Document) {
				doc.Operations[0].RequestRequired = false
			},
			want: "optional request body",
		},
		{
			name: "bodyless response with body status",
			mutate: func(doc *Document) {
				doc.Operations[1].Status = 200
			},
			want: "without a response body must use status 204",
		},
		{
			name: "response body with no-content status",
			mutate: func(doc *Document) {
				doc.Operations[0].Status = 204
			},
			want: "with a response body uses status 204",
		},
		{
			name: "duplicate parameter Go name",
			mutate: func(doc *Document) {
				doc.Operations[0].Parameters = append(doc.Operations[0].Parameters,
					Parameter{Name: "other", GoName: "XTrace", In: "query", Type: TypeRef{Kind: String}})
			},
			want: "duplicate parameter Go field",
		},
		{
			name: "invalid parameter Go name",
			mutate: func(doc *Document) {
				doc.Operations[0].Parameters[1].GoName = "x-trace"
			},
			want: "valid Go identifier",
		},
		{
			name: "parameter is not scalar",
			mutate: func(doc *Document) {
				doc.Operations[0].Parameters[1].Type = TypeRef{Kind: Array, Items: &TypeRef{Kind: String}}
			},
			want: "inline scalar type",
		},
		{
			name: "optional path parameter",
			mutate: func(doc *Document) {
				doc.Operations[0].Parameters[0].Required = false
			},
			want: "must be required",
		},
		{
			name: "path parameter without template",
			mutate: func(doc *Document) {
				doc.Operations[0].Path = "/widgets"
			},
			want: "no matching template",
		},
		{
			name: "template without path parameter",
			mutate: func(doc *Document) {
				doc.Operations[0].Parameters = doc.Operations[0].Parameters[1:]
			},
			want: "has no required path parameter",
		},
		{
			name: "invalid header token",
			mutate: func(doc *Document) {
				doc.Operations[0].Parameters[1].Name = "Bad Header"
			},
			want: "not a valid HTTP field name",
		},
		{
			name: "case-insensitive duplicate header",
			mutate: func(doc *Document) {
				doc.Operations[0].Parameters = append(doc.Operations[0].Parameters,
					Parameter{Name: "x-trace", GoName: "OtherTrace", In: "header", Type: TypeRef{Kind: String}})
			},
			want: "duplicate case-insensitive header",
		},
		{
			name: "reserved header",
			mutate: func(doc *Document) {
				doc.Operations[0].Parameters[1].Name = "Content-Type"
				doc.Operations[0].Parameters[1].GoName = "ContentType"
			},
			want: "controlled by HTTP semantics",
		},
		{
			name: "raw query delimiter",
			mutate: func(doc *Document) {
				doc.Operations[0].Path = "/widgets/{id}?view=full"
			},
			want: "raw query or fragment",
		},
		{
			name: "raw fragment delimiter",
			mutate: func(doc *Document) {
				doc.Operations[0].Path = "/widgets/{id}#view"
			},
			want: "raw query or fragment",
		},
		{
			name: "repeated slash",
			mutate: func(doc *Document) {
				doc.Operations[0].Path = "/widgets//{id}"
			},
			want: "repeated slashes",
		},
		{
			name: "raw dot segment",
			mutate: func(doc *Document) {
				doc.Operations[0].Path = "/widgets/../{id}"
			},
			want: "dot segments",
		},
		{
			name: "encoded dot segment",
			mutate: func(doc *Document) {
				doc.Operations[0].Path = "/widgets/%2E%2e/{id}"
			},
			want: "dot segments",
		},
		{
			name: "raw control character",
			mutate: func(doc *Document) {
				doc.Operations[0].Path = "/widgets/\x1b/{id}"
			},
			want: "control or format characters",
		},
		{
			name: "encoded control character",
			mutate: func(doc *Document) {
				doc.Operations[0].Path = "/widgets/%1B/{id}"
			},
			want: "raw or encoded control or format characters",
		},
		{
			name: "Unicode format character",
			mutate: func(doc *Document) {
				doc.Operations[0].Path = "/widgets/\u202e/{id}"
			},
			want: "control or format characters",
		},
		{
			name: "encoded invalid UTF-8",
			mutate: func(doc *Document) {
				doc.Operations[0].Path = "/widgets/%FF/{id}"
			},
			want: "not valid UTF-8",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			doc := validDirectDocument()
			test.mutate(&doc)
			err := doc.Validate()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestDocumentValidateRejectsCyclesAndConflictingRoutes(t *testing.T) {
	t.Run("required reference cycle", func(t *testing.T) {
		doc := Document{
			Components: []Schema{
				{Name: "Parent", Kind: Object, Properties: []Field{{Name: "Child", JSONName: "child", Type: TypeRef{Name: "Child"}, Required: true}}},
				{Name: "Child", Kind: Object, Properties: []Field{{Name: "Parent", JSONName: "parent", Type: TypeRef{Name: "Parent"}, Required: true}}},
			},
			Operations: []Operation{{ID: "GetParent", Method: "GET", Path: "/parent", Response: &TypeRef{Name: "Parent"}, Status: 200}},
		}
		if err := doc.Validate(); err == nil || !strings.Contains(err.Error(), "Parent -> Child -> Parent") {
			t.Fatalf("Validate cycle error = %v", err)
		}
	})

	t.Run("equivalent wildcard routes", func(t *testing.T) {
		doc := Document{
			Components: []Schema{{Name: "Widget", Kind: Object}},
			Operations: []Operation{
				{
					ID: "GetByID", Method: "GET", Path: "/widgets/{id}", Status: 200,
					Parameters: []Parameter{{Name: "id", GoName: "ID", In: "path", Type: TypeRef{Kind: String}, Required: true}},
					Response:   &TypeRef{Name: "Widget"},
				},
				{
					ID: "GetByName", Method: "GET", Path: "/widgets/{name}", Status: 200,
					Parameters: []Parameter{{Name: "name", GoName: "Name", In: "path", Type: TypeRef{Kind: String}, Required: true}},
					Response:   &TypeRef{Name: "Widget"},
				},
			},
		}
		if err := doc.Validate(); err == nil || !strings.Contains(err.Error(), "conflicts") {
			t.Fatalf("Validate route conflict error = %v", err)
		}
	})
}

func TestDocumentValidateRejectsHTTPControlledHeaders(t *testing.T) {
	reserved := []string{
		"Accept",
		"Accept-Encoding",
		"Connection",
		"Content-Encoding",
		"Content-Length",
		"Content-Type",
		"Host",
		"Keep-Alive",
		"Proxy-Connection",
		"TE",
		"Trailer",
		"Transfer-Encoding",
		"Upgrade",
	}
	for _, name := range reserved {
		t.Run(name, func(t *testing.T) {
			doc := validDirectDocument()
			doc.Operations[0].Parameters[1].Name = strings.ToLower(name)
			if err := doc.Validate(); err == nil || !strings.Contains(err.Error(), "controlled by HTTP semantics") {
				t.Fatalf("Validate reserved header %q error = %v", name, err)
			}
		})
	}
}

func TestParseRunsDocumentValidation(t *testing.T) {
	tests := []struct {
		name string
		spec string
		want string
	}{
		{
			name: "HTTP-controlled header",
			spec: strings.Replace(sampleSpec, "        - name: X-Trace\n", "        - name: Host\n", 1),
			want: "controlled by HTTP semantics",
		},
		{
			name: "non-canonical route",
			spec: strings.Replace(sampleSpec, "  /health:\n", "  /health/%2e:\n", 1),
			want: "dot segments",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := Parse([]byte(test.spec)); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Parse error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestValidateOptions(t *testing.T) {
	tests := []struct {
		name    string
		options Options
		want    string
	}{
		{name: "default output", options: Options{Module: "example.com/generated"}, want: defaultOutput},
		{name: "portable separator", options: Options{Module: "example.com/generated", Output: `internal\widgetapi`}, want: "internal/widgetapi"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			output, err := validateOptions(test.options)
			if err != nil || output != test.want {
				t.Fatalf("validateOptions = %q, %v; want %q, nil", output, err, test.want)
			}
		})
	}

	invalid := []struct {
		name    string
		options Options
		want    string
	}{
		{name: "missing module", options: Options{}, want: "module is required"},
		{name: "invalid module", options: Options{Module: "bad module"}, want: "module"},
		{name: "module surrounding whitespace", options: Options{Module: " example.com/generated "}, want: "module"},
		{name: "module vendor segment", options: Options{Module: "example.com/vendor/generated"}, want: "vendor path segment"},
		{name: "absolute slash output", options: Options{Module: "example.com/generated", Output: "/internal/openapi"}, want: "unsafe relative path"},
		{name: "absolute volume output", options: Options{Module: "example.com/generated", Output: `C:\generated`}, want: "unsafe relative path"},
		{name: "trailing slash output", options: Options{Module: "example.com/generated", Output: "internal/openapi/"}, want: "unsafe relative path"},
		{name: "traversal output", options: Options{Module: "example.com/generated", Output: "../openapi"}, want: "unsafe relative path"},
		{name: "leading vendor output", options: Options{Module: "example.com/generated", Output: "vendor/widgetapi"}, want: "vendor path segment"},
		{name: "nested vendor output", options: Options{Module: "example.com/generated", Output: "api/vendor/widgetapi"}, want: "vendor path segment"},
		{name: "output leading whitespace", options: Options{Module: "example.com/generated", Output: " internal/openapi"}, want: "output import path"},
		{name: "invalid import output", options: Options{Module: "example.com/generated", Output: "internal/open api"}, want: "output import path"},
	}
	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			if output, err := validateOptions(test.options); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validateOptions = %q, %v; want error containing %q", output, err, test.want)
			}
		})
	}
}
