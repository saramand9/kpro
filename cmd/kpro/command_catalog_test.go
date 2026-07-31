package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/saramand9/kpro/internal/codegen/manifest"
	"github.com/saramand9/kpro/internal/codegen/protogen"
)

const catalogManifest = `apiVersion: kpro.dev/codegen/v1alpha1
module: example.com/orders
service: orders
sources:
  - id: http-api
    kind: openapi
    path: api/http/openapi.yaml
    package: internal/httpapi
  - id: grpc-api
    kind: proto
    path: api/grpc/service.proto
    package: internal/grpcapi
  - id: message-api
    kind: message
    path: api/message/messages.yaml
    package: internal/messageapi
`

const catalogMessageSpec = `apiVersion: kpro.dev/message/v1alpha1
service: Orders
encoding: json
messages:
  - id: 200
    name: create
    dispatch: async
    request:
      fields:
        - name: name
          type: string
          required: true
  - id: 100
    name: health
    dispatch: sync
    request:
      fields: []
    response:
      fields:
        - name: status
          type: string
          required: true
`

const catalogServiceConfig = `apiVersion: kpro.dev/service/v1alpha1
apis:
  - http
  - grpc
  - message
transports:
  - tcp
  - websocket
`

func TestListAPITextAndJSONAreDeterministic(t *testing.T) {
	root := writeCatalogProject(t, true)
	installCatalogProtoFixture(t)

	var first bytes.Buffer
	if err := execute([]string{"list", "api", "--root", root}, &first, &first); err != nil {
		t.Fatal(err)
	}
	contents := first.String()
	for _, expected := range []string{
		"KIND",
		"http",
		"GET /orders/{id} -> 200",
		"example.orders.v1.Admin",
		"Ping",
		"unary",
		"example.orders.v1.Orders",
		"Watch",
		"bidi-streaming",
		"id=100 sync",
		"id=200 async",
	} {
		if !strings.Contains(contents, expected) {
			t.Fatalf("list api output does not contain %q:\n%s", expected, contents)
		}
	}
	assertCatalogOrder(t, contents, []string{
		"example.orders.v1.Admin",
		"example.orders.v1.Orders",
		"GET /orders/{id} -> 200",
		"id=100 sync",
		"id=200 async",
	})

	var second bytes.Buffer
	if err := execute([]string{"list", "api", "--root", root}, &second, &second); err != nil {
		t.Fatal(err)
	}
	if first.String() != second.String() {
		t.Fatalf("text output is not deterministic:\nfirst:\n%s\nsecond:\n%s", first.String(), second.String())
	}

	var jsonOutput bytes.Buffer
	if err := execute([]string{"list", "api", "--root", root, "--format", "json"}, &jsonOutput, &jsonOutput); err != nil {
		t.Fatal(err)
	}
	var decoded catalogAPIList
	if err := json.Unmarshal(jsonOutput.Bytes(), &decoded); err != nil {
		t.Fatalf("decode list JSON: %v\n%s", err, jsonOutput.String())
	}
	if decoded.APIVersion != catalogAPIVersion || decoded.Kind != "APIList" || decoded.Module != "example.com/orders" || decoded.Service != "orders" {
		t.Fatalf("list JSON metadata = %#v", decoded)
	}
	if decoded.Manifest != defaultCodegenConfig {
		t.Fatalf("manifest = %q, want %q", decoded.Manifest, defaultCodegenConfig)
	}
	if len(decoded.APIs) != 6 {
		t.Fatalf("API count = %d, want 6: %#v", len(decoded.APIs), decoded.APIs)
	}
	if decoded.APIs[0].GRPC == nil || decoded.APIs[0].API != "example.orders.v1.Admin" {
		t.Fatalf("first API = %#v, want sorted Admin RPC", decoded.APIs[0])
	}
	if decoded.APIs[3].HTTP == nil || decoded.APIs[3].Operation != "GetOrder" {
		t.Fatalf("fourth API = %#v, want HTTP GetOrder", decoded.APIs[3])
	}
	if decoded.APIs[4].Message == nil || decoded.APIs[4].Message.ID != 100 || decoded.APIs[4].Message.Response == nil {
		t.Fatalf("fifth API = %#v, want message 100 with response", decoded.APIs[4])
	}
	if decoded.APIs[5].Message == nil || decoded.APIs[5].Message.ID != 200 || decoded.APIs[5].Message.Response != nil {
		t.Fatalf("sixth API = %#v, want one-way message 200", decoded.APIs[5])
	}
	if !strings.Contains(jsonOutput.String(), `"fields": []`) {
		t.Fatalf("empty message fields are not a stable JSON array:\n%s", jsonOutput.String())
	}
}

func TestDescribeServiceReportsAssemblyAndSourceCounts(t *testing.T) {
	root := writeCatalogProject(t, true)
	installCatalogProtoFixture(t)

	var output bytes.Buffer
	if err := execute([]string{"describe", "service", "--root", root}, &output, &output); err != nil {
		t.Fatal(err)
	}
	contents := output.String()
	for _, expected := range []string{
		"Service: orders",
		"Module: example.com/orders",
		"Assembly APIs: http, grpc, message",
		"Message transports: tcp, websocket",
		"Scope: declared contracts (not runtime registrations)",
		"1 API, 1 operation",
		"2 services, 3 methods",
		"1 service, 2 messages",
	} {
		if !strings.Contains(contents, expected) {
			t.Fatalf("describe output does not contain %q:\n%s", expected, contents)
		}
	}

	output.Reset()
	if err := execute([]string{"describe", "service", "--root", root, "--format=json"}, &output, &output); err != nil {
		t.Fatal(err)
	}
	var decoded catalogDocument
	if err := json.Unmarshal(output.Bytes(), &decoded); err != nil {
		t.Fatalf("decode describe JSON: %v\n%s", err, output.String())
	}
	if decoded.APIVersion != catalogAPIVersion || decoded.Kind != "ServiceDescription" || decoded.Assembly == nil {
		t.Fatalf("description metadata = %#v", decoded)
	}
	if strings.Join(decoded.Assembly.APIs, ",") != "http,grpc,message" || strings.Join(decoded.Assembly.Transports, ",") != "tcp,websocket" {
		t.Fatalf("assembly = %#v", decoded.Assembly)
	}
	if len(decoded.Sources) != 3 || decoded.Sources[0].ID != "grpc-api" || decoded.Sources[0].Groups != 2 || decoded.Sources[0].Operations != 3 {
		t.Fatalf("sources = %#v", decoded.Sources)
	}
}

func TestDescribeServiceSupportsManifestWithoutAssembly(t *testing.T) {
	root := writeCatalogProject(t, false)
	installCatalogProtoFixture(t)

	var output bytes.Buffer
	if err := execute([]string{"describe", "service", "--root", root}, &output, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "Assembly: not configured") {
		t.Fatalf("standalone description:\n%s", output.String())
	}

	output.Reset()
	if err := execute([]string{"describe", "service", "--root", root, "--format", "json"}, &output, &output); err != nil {
		t.Fatal(err)
	}
	var decoded catalogDocument
	if err := json.Unmarshal(output.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Assembly != nil {
		t.Fatalf("standalone assembly = %#v, want nil", decoded.Assembly)
	}
}

func TestCatalogFailureProducesNoPartialOutput(t *testing.T) {
	root := writeCatalogProject(t, false)
	installCatalogProtoFixture(t)
	if err := os.WriteFile(
		filepath.Join(root, "api", "message", "messages.yaml"),
		[]byte("not: a valid message contract\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	err := execute([]string{"list", "api", "--root", root}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), `inspect source "message-api"`) {
		t.Fatalf("list error = %v", err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("failed list emitted partial stdout:\n%s", stdout.String())
	}
}

func TestCatalogAppliesBackendSemanticValidationBeforeListing(t *testing.T) {
	root := writeCatalogProject(t, false)
	installCatalogProtoFixture(t)
	filename := filepath.Join(root, "api", "http", "openapi.yaml")
	data, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	data = bytes.Replace(data, []byte("    get:\n      operationId: getOrder"), []byte("    head:\n      operationId: getOrder"), 1)
	if err := os.WriteFile(filename, data, 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	err = execute([]string{"list", "api", "--root", root}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), `inspect source "http-api"`) || !strings.Contains(err.Error(), "HTTP adapter") {
		t.Fatalf("backend semantic error = %v", err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("semantic failure emitted partial stdout:\n%s", stdout.String())
	}
}

func TestCatalogRejectsPluginsWithoutExecutingInspectors(t *testing.T) {
	root := writeCatalogProject(t, false)
	manifestPath := filepath.Join(root, defaultCodegenConfig)
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	data = bytes.Replace(data, []byte("    package: internal/httpapi\n"), []byte("    package: internal/httpapi\n    plugin:\n      command: custom-generator\n"), 1)
	if err := os.WriteFile(manifestPath, data, 0o644); err != nil {
		t.Fatal(err)
	}

	previous := inspectCatalogProto
	called := false
	inspectCatalogProto = func(context.Context, string, manifest.Manifest, manifest.Source) (protogen.Contract, error) {
		called = true
		return protogen.Contract{}, errors.New("must not be called")
	}
	t.Cleanup(func() { inspectCatalogProto = previous })

	var output bytes.Buffer
	err = execute([]string{"list", "api", "--root", root}, &output, &output)
	if err == nil || !strings.Contains(err.Error(), "never execute plugins") {
		t.Fatalf("plugin catalog error = %v", err)
	}
	if called {
		t.Fatal("proto inspector ran before plugin preflight completed")
	}
	if output.Len() != 0 {
		t.Fatalf("plugin rejection emitted output:\n%s", output.String())
	}
}

func TestCatalogCommandValidationAndNestedHelp(t *testing.T) {
	for _, arguments := range [][]string{
		{"list", "api", "--help"},
		{"describe", "service", "--help"},
	} {
		var stdout bytes.Buffer
		var stderr bytes.Buffer
		if err := execute(arguments, &stdout, &stderr); err != nil {
			t.Fatalf("execute(%v): %v", arguments, err)
		}
		if !strings.Contains(stderr.String(), "Usage of "+strings.Join(arguments[:2], " ")+":") {
			t.Fatalf("nested help for %v:\n%s", arguments, stderr.String())
		}
	}

	var output bytes.Buffer
	err := execute([]string{"list", "api", "--format", "xml"}, &output, &output)
	if err == nil || !strings.Contains(err.Error(), "unsupported format") {
		t.Fatalf("unsupported format error = %v", err)
	}
	if output.Len() != 0 {
		t.Fatalf("unsupported format loaded or rendered output:\n%s", output.String())
	}

	for _, arguments := range [][]string{{"list", "routes"}, {"describe", "runtime"}} {
		output.Reset()
		if err := execute(arguments, &output, &output); err == nil {
			t.Fatalf("execute(%v) unexpectedly succeeded", arguments)
		}
	}
}

func TestCatalogJSONUsesStableEmptyCollections(t *testing.T) {
	content, err := renderCatalogJSON(catalogDocument{
		APIVersion: catalogAPIVersion,
		Kind:       "ServiceDescription",
		Sources:    []catalogSource{},
		APIs:       []catalogAPI{},
		Assembly: &catalogAssembly{
			APIs:       []string{},
			Transports: []string{},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	contents := string(content)
	for _, expected := range []string{
		`"sources": []`,
		`"apis": []`,
		`"transports": []`,
	} {
		if !strings.Contains(contents, expected) {
			t.Fatalf("JSON does not contain %s:\n%s", expected, contents)
		}
	}

	nested, err := renderCatalogJSON(catalogAPI{
		HTTP: &catalogHTTPBinding{Parameters: []catalogHTTPParameter{}},
		Message: &catalogMessageBinding{
			Request: catalogMessagePayload{Fields: []catalogMessageField{}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{`"parameters": []`, `"fields": []`} {
		if !strings.Contains(string(nested), expected) {
			t.Fatalf("nested JSON does not contain %s:\n%s", expected, nested)
		}
	}
}

func TestCatalogTextEscapesControlsAndFormatCharacters(t *testing.T) {
	input := "订单 café\tline\n\a\x1b\x7f\u0085\u009b\u202e\U000e0001"
	got := catalogCell(input)
	want := `订单 café\u0009line\u000A\u0007\u001B\u007F\u0085\u009B\u202E\U000E0001`
	if got != want {
		t.Fatalf("catalogCell() = %q, want %q", got, want)
	}
	for name, forbidden := range map[string][]byte{
		"BEL":  {0x07},
		"ESC":  {0x1b},
		"bidi": []byte("\u202e"),
	} {
		if bytes.Contains([]byte(got), forbidden) {
			t.Fatalf("catalogCell() contains raw %s bytes %x: %q", name, forbidden, got)
		}
	}
	for _, character := range got {
		if catalogTextControl(character) {
			t.Fatalf("catalogCell() contains forbidden rune U+%04X: %q", character, got)
		}
	}
	rendered, err := renderAPIListText(catalogDocument{APIs: []catalogAPI{{
		Kind:      "http",
		SourceID:  "source",
		API:       input,
		Operation: "operation",
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(rendered, []byte(want)) {
		t.Fatalf("rendered text does not contain escaped cell %q:\n%s", want, rendered)
	}
	for _, forbidden := range [][]byte{{0x07}, {0x1b}, []byte("\u202e")} {
		if bytes.Contains(rendered, forbidden) {
			t.Fatalf("rendered text contains forbidden bytes %x:\n%s", forbidden, rendered)
		}
	}

	encoded, err := renderCatalogJSON(catalogAPI{API: input})
	if err != nil {
		t.Fatal(err)
	}
	var decoded catalogAPI
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.API != input {
		t.Fatalf("JSON API = %q, want original value %q", decoded.API, input)
	}
}

func writeCatalogProject(t *testing.T, withAssembly bool) string {
	t.Helper()
	root := t.TempDir()
	for _, directory := range []string{
		filepath.Join(root, "api", "http"),
		filepath.Join(root, "api", "grpc"),
		filepath.Join(root, "api", "message"),
	} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for name, content := range map[string]string{
		defaultCodegenConfig:                             catalogManifest,
		filepath.Join("api", "http", "openapi.yaml"):     testOpenAPISpec,
		filepath.Join("api", "grpc", "service.proto"):    `syntax = "proto3"; package example.orders.v1; service Orders {}`,
		filepath.Join("api", "message", "messages.yaml"): catalogMessageSpec,
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if withAssembly {
		if err := os.WriteFile(filepath.Join(root, defaultServiceProjectFile), []byte(catalogServiceConfig), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func installCatalogProtoFixture(t *testing.T) {
	t.Helper()
	previous := inspectCatalogProto
	inspectCatalogProto = func(
		_ context.Context,
		_ string,
		_ manifest.Manifest,
		source manifest.Source,
	) (protogen.Contract, error) {
		return protogen.Contract{
			Path:    source.Path,
			Package: "example.orders.v1",
			Services: []protogen.Service{
				{
					Name:     "Orders",
					FullName: "example.orders.v1.Orders",
					Methods: []protogen.Method{
						{
							Name:            "Watch",
							Input:           "example.orders.v1.WatchRequest",
							Output:          "example.orders.v1.WatchResponse",
							ClientStreaming: true,
							ServerStreaming: true,
						},
						{Name: "Get", Input: "example.orders.v1.GetRequest", Output: "example.orders.v1.GetResponse"},
					},
				},
				{
					Name:     "Admin",
					FullName: "example.orders.v1.Admin",
					Methods: []protogen.Method{{
						Name:   "Ping",
						Input:  "example.orders.v1.PingRequest",
						Output: "example.orders.v1.PingResponse",
					}},
				},
			},
		}, nil
	}
	t.Cleanup(func() { inspectCatalogProto = previous })
}

func assertCatalogOrder(t *testing.T, contents string, ordered []string) {
	t.Helper()
	previous := -1
	for _, value := range ordered {
		index := strings.Index(contents, value)
		if index < 0 {
			t.Fatalf("output does not contain %q:\n%s", value, contents)
		}
		if index <= previous {
			t.Fatalf("%q is out of order in:\n%s", value, contents)
		}
		previous = index
	}
}
