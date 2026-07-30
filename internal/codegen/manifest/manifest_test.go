package manifest

import (
	"strings"
	"testing"
)

const validManifest = `apiVersion: kpro.dev/codegen/v1alpha1
module: example.com/orders
service: orders
sources:
  - id: public-api
    kind: openapi
    path: api/orders.openapi.yaml
    package: internal/modules/orders
  - id: order-rpc
    kind: proto
    path: api/orders.proto
    package: internal/modules/orderrpc
`

func TestParseValidManifest(t *testing.T) {
	value, err := Parse([]byte(validManifest))
	if err != nil {
		t.Fatal(err)
	}
	if value.Module != "example.com/orders" || value.Service != "orders" {
		t.Fatalf("Parse() = %#v", value)
	}
	if got := value.Sources[0].Path; got != "api/orders.openapi.yaml" {
		t.Fatalf("normalized path = %q", got)
	}
}

func TestParseRejectsUnknownField(t *testing.T) {
	_, err := Parse([]byte(strings.Replace(validManifest, "service: orders", "service: orders\nunknown: true", 1)))
	if err == nil || !strings.Contains(err.Error(), "field unknown not found") {
		t.Fatalf("Parse() error = %v", err)
	}
}

func TestParseRejectsMultipleDocuments(t *testing.T) {
	_, err := Parse([]byte(validManifest + "---\nservice: other\n"))
	if err == nil || !strings.Contains(err.Error(), "multiple YAML documents") {
		t.Fatalf("Parse() error = %v", err)
	}
}

func TestValidateRejectsUnsafeAndDuplicateSources(t *testing.T) {
	tests := []struct {
		name    string
		replace string
		with    string
		want    string
	}{
		{
			name:    "traversal",
			replace: "api/orders.openapi.yaml",
			with:    "../orders.yaml",
			want:    "unsafe path",
		},
		{
			name:    "absolute windows path",
			replace: "api/orders.openapi.yaml",
			with:    `C:\orders.yaml`,
			want:    "unsafe path",
		},
		{
			name:    "duplicate id",
			replace: "id: order-rpc",
			with:    "id: public-api",
			want:    "duplicates id",
		},
		{
			name:    "duplicate source path",
			replace: "api/orders.proto",
			with:    "api/orders.openapi.yaml",
			want:    "reuses path",
		},
		{
			name:    "invalid package",
			replace: "internal/modules/orderrpc",
			with:    "internal/modules/order-rpc",
			want:    "not a Go identifier",
		},
		{
			name:    "unsupported kind",
			replace: "kind: proto",
			with:    "kind: graphql",
			want:    "unsupported kind",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			data := strings.Replace(validManifest, test.replace, test.with, 1)
			_, err := Parse([]byte(data))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Parse() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestValidateRejectsInvalidTopLevelValues(t *testing.T) {
	tests := []struct {
		name    string
		replace string
		with    string
		want    string
	}{
		{
			name:    "version",
			replace: APIVersionV1Alpha1,
			with:    "kpro.dev/codegen/v2",
			want:    "unsupported apiVersion",
		},
		{
			name:    "module",
			replace: "example.com/orders",
			with:    "not a module path",
			want:    "invalid module",
		},
		{
			name:    "service",
			replace: "service: orders",
			with:    "service: for",
			want:    "invalid service name",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			data := strings.Replace(validManifest, test.replace, test.with, 1)
			_, err := Parse([]byte(data))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Parse() error = %v, want containing %q", err, test.want)
			}
		})
	}
}
