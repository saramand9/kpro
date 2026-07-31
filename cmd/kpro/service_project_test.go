package main

import (
	"bytes"
	"go/format"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/saramand9/kpro/internal/codegen"
	"github.com/saramand9/kpro/internal/codegen/manifest"
)

func TestServiceProjectConfigCanonicalRoundTrip(t *testing.T) {
	input := []byte(`
apiVersion: " kpro.dev/service/v1alpha1 "
apis:
  - MESSAGE
  - Http
  - grpc
transports:
  - websocket
  - UDP
  - tcp
  - QUIC
  - kcp
`)
	value, err := parseServiceProjectConfig(input)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"http", "grpc", "message"}; !reflect.DeepEqual(value.APIs, want) {
		t.Fatalf("APIs = %v, want %v", value.APIs, want)
	}
	if want := []string{"tcp", "kcp", "quic", "udp", "websocket"}; !reflect.DeepEqual(value.Transports, want) {
		t.Fatalf("transports = %v, want %v", value.Transports, want)
	}

	encoded, err := encodeServiceProjectConfig(value)
	if err != nil {
		t.Fatal(err)
	}
	want := `apiVersion: kpro.dev/service/v1alpha1
apis:
  - http
  - grpc
  - message
transports:
  - tcp
  - kcp
  - quic
  - udp
  - websocket
`
	if string(encoded) != want {
		t.Fatalf("encoded config:\n%s\nwant:\n%s", encoded, want)
	}
	roundTrip, err := parseServiceProjectConfig(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(roundTrip, value) {
		t.Fatalf("round trip = %#v, want %#v", roundTrip, value)
	}
}

func TestServiceProjectScaffoldConfigMatchesCanonicalEncoding(t *testing.T) {
	tests := []struct {
		name    string
		data    scaffoldData
		project serviceProjectConfig
	}{
		{
			name: "HTTP only",
			data: scaffoldData{HasHTTP: true},
			project: serviceProjectConfig{
				APIVersion: serviceProjectAPIVersion,
				APIs:       []string{"http"},
			},
		},
		{
			name: "gRPC only",
			data: scaffoldData{HasGRPC: true},
			project: serviceProjectConfig{
				APIVersion: serviceProjectAPIVersion,
				APIs:       []string{"grpc"},
			},
		},
		{
			name: "Message transports",
			data: scaffoldData{
				HasMessage:   true,
				HasTCP:       true,
				HasWebSocket: true,
			},
			project: serviceProjectConfig{
				APIVersion: serviceProjectAPIVersion,
				APIs:       []string{"message"},
				Transports: []string{"tcp", "websocket"},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			if err := renderTemplate(
				root,
				"templates/profiles/service/kpro.service.yaml.tmpl",
				defaultServiceProjectFile,
				0o644,
				test.data,
			); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(filepath.Join(root, defaultServiceProjectFile))
			if err != nil {
				t.Fatal(err)
			}
			want, err := encodeServiceProjectConfig(test.project)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("scaffold config:\n%s\nwant canonical encoding:\n%s", got, want)
			}
			if !test.data.HasMessage && bytes.Contains(got, []byte("transports:")) {
				t.Fatalf("non-Message scaffold contains transports:\n%s", got)
			}
		})
	}
}
func TestServiceProjectConfigRejectsInvalidDocuments(t *testing.T) {
	tests := []struct {
		name     string
		document string
		want     string
	}{
		{
			name: "unknown field",
			document: `
apiVersion: kpro.dev/service/v1alpha1
apis: [http]
address: :8080
`,
			want: "field address not found",
		},
		{
			name: "multiple documents",
			document: `
apiVersion: kpro.dev/service/v1alpha1
apis: [http]
---
apiVersion: kpro.dev/service/v1alpha1
apis: [grpc]
`,
			want: "multiple YAML documents",
		},
		{
			name: "wrong version",
			document: `
apiVersion: kpro.dev/service/v2
apis: [http]
`,
			want: "unsupported apiVersion",
		},
		{
			name: "no APIs",
			document: `
apiVersion: kpro.dev/service/v1alpha1
apis: []
`,
			want: "at least one API",
		},
		{
			name: "duplicate API",
			document: `
apiVersion: kpro.dev/service/v1alpha1
apis: [http, HTTP]
`,
			want: `duplicate API "http"`,
		},
		{
			name: "unsupported API",
			document: `
apiVersion: kpro.dev/service/v1alpha1
apis: [graphql]
`,
			want: `unsupported API "graphql"`,
		},
		{
			name: "Message without transport",
			document: `
apiVersion: kpro.dev/service/v1alpha1
apis: [message]
`,
			want: "Message API requires at least one transport",
		},
		{
			name: "transport without Message",
			document: `
apiVersion: kpro.dev/service/v1alpha1
apis: [grpc]
transports: [tcp]
`,
			want: "transports require the Message API",
		},
		{
			name: "duplicate transport",
			document: `
apiVersion: kpro.dev/service/v1alpha1
apis: [message]
transports: [tcp, TCP]
`,
			want: `duplicate transport "tcp"`,
		},
		{
			name: "unsupported transport",
			document: `
apiVersion: kpro.dev/service/v1alpha1
apis: [message]
transports: [serial]
`,
			want: `unsupported transport "serial"`,
		},
		{
			name: "wrong APIs type",
			document: `
apiVersion: kpro.dev/service/v1alpha1
apis: http
`,
			want: "cannot unmarshal",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseServiceProjectConfig([]byte(test.document))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestValidateChecksConfiguredServiceAssembly(t *testing.T) {
	target := filepath.Join(t.TempDir(), "orders")
	var output bytes.Buffer
	if err := execute([]string{
		"new",
		"--module", "example.com/orders",
		"--name", "orders",
		"--profile", "service",
		"--api", "http",
		target,
	}, &output, &output); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if err := execute([]string{"validate", "--root", target}, &output, &output); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"validated manifest:", "validated service assembly:", "APIs: http"} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("validate output does not contain %q:\n%s", expected, &output)
		}
	}
	lockPath := filepath.Join(target, codegen.DefaultLockFile)
	lockBefore, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	drifted := []byte("apiVersion: kpro.dev/service/v1alpha1\napis:\n  - grpc\n")
	if err := os.WriteFile(filepath.Join(target, defaultServiceProjectFile), drifted, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, command := range [][]string{
		{"validate", "--root", target},
		{"generate", "--root", target, "--dry-run"},
	} {
		output.Reset()
		err := execute(command, &output, &output)
		if err == nil || !strings.Contains(err.Error(), `manifest source "http-api" requires API "http"`) {
			t.Fatalf("%v error = %v", command, err)
		}
		if output.Len() != 0 {
			t.Fatalf("%v reported success before rejecting assembly drift:\n%s", command, &output)
		}
		lockAfter, readErr := os.ReadFile(lockPath)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if !bytes.Equal(lockAfter, lockBefore) {
			t.Fatalf("%v changed generation lock after validation failure", command)
		}
	}
}

func TestEstablishedServiceProjectRejectsMissingServiceConfigWithoutWrites(t *testing.T) {
	installAddTestEnvironment(t)
	root := createAddTestProject(t, true)
	if err := os.Remove(filepath.Join(root, defaultServiceProjectFile)); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "internal", "kproapp", "endpoints.gen.go")); err != nil {
		t.Fatal(err)
	}
	before := snapshotAddTestTree(t, root)

	for _, command := range []string{"validate", "generate"} {
		t.Run(command, func(t *testing.T) {
			var output bytes.Buffer
			err := execute([]string{command, "--root", root}, &output, &output)
			if err == nil || !strings.Contains(err.Error(), defaultServiceProjectFile) || !strings.Contains(err.Error(), "established service project") {
				t.Fatalf("%s missing service config error = %v\n%s", command, err, output.String())
			}
			assertAddProjectUnchanged(t, root, before)
		})
	}
}

func TestBuildServiceAssemblyPlanAllAPIsAndTransports(t *testing.T) {
	project := serviceProjectConfig{
		APIVersion: serviceProjectAPIVersion,
		APIs:       []string{"message", "http", "grpc"},
		Transports: []string{"websocket", "udp", "quic", "kcp", "tcp"},
	}
	value := serviceProjectManifest("http", "grpc", "message")
	plan, err := buildServiceAssemblyPlan(project, value)
	if err != nil {
		t.Fatal(err)
	}
	files, err := plan.OrderedFiles()
	if err != nil {
		t.Fatal(err)
	}
	wantPaths := []string{
		"internal/kproapp/endpoints.gen.go",
		"internal/kproapp/grpc.go",
		"internal/kproapp/http.go",
		"internal/kproapp/message.go",
	}
	if got := serviceAssemblyPaths(files); !reflect.DeepEqual(got, wantPaths) {
		t.Fatalf("paths = %v, want %v", got, wantPaths)
	}
	for _, file := range files {
		if _, err := format.Source(file.Content); err != nil {
			t.Fatalf("%s is not gofmt-compatible: %v\n%s", file.Path, err, file.Content)
		}
		wantOwnership := codegen.Scaffold
		if file.Path == "internal/kproapp/endpoints.gen.go" {
			wantOwnership = codegen.Generated
		}
		if file.Ownership != wantOwnership {
			t.Fatalf("%s ownership = %q, want %q", file.Path, file.Ownership, wantOwnership)
		}
	}

	endpoints := string(serviceAssemblyContent(t, files, "internal/kproapp/endpoints.gen.go"))
	httpIndex := strings.Index(endpoints, "mountHTTP(app)")
	grpcIndex := strings.Index(endpoints, "mountGRPC(app)")
	messageIndex := strings.Index(endpoints, "mountMessage(app)")
	if httpIndex < 0 || grpcIndex <= httpIndex || messageIndex <= grpcIndex {
		t.Fatalf("endpoint order is not canonical:\n%s", endpoints)
	}
	grpc := string(serviceAssemblyContent(t, files, "internal/kproapp/grpc.go"))
	for _, expected := range []string{
		`grpcapi "example.com/orders/internal/grpcapi"`,
		"KProGrpcApiGRPCServices",
		"OrdersApi: &grpcapi.KProGrpcApiOrdersApiService{}",
		"RegisterGRPC(endpoint)",
	} {
		if !strings.Contains(grpc, expected) {
			t.Fatalf("gRPC mount does not contain %q:\n%s", expected, grpc)
		}
	}
	message := string(serviceAssemblyContent(t, files, "internal/kproapp/message.go"))
	previous := -1
	for _, expected := range []string{
		"tcp.New",
		"kcp.New",
		"quic.New",
		"udp.New",
		"websocket.New",
	} {
		index := strings.Index(message, expected)
		if index <= previous {
			t.Fatalf("Message transport %q is missing or out of order:\n%s", expected, message)
		}
		previous = index
	}
	if !strings.Contains(message, "RegisterOrdersApi") {
		t.Fatalf("Message mount uses the wrong service name:\n%s", message)
	}
}

func TestBuildServiceAssemblyPlanFormatsEverySelection(t *testing.T) {
	tests := []struct {
		name       string
		apis       []string
		transports []string
	}{
		{name: "HTTP", apis: []string{"http"}},
		{name: "gRPC", apis: []string{"grpc"}},
		{name: "TCP", apis: []string{"message"}, transports: []string{"tcp"}},
		{name: "KCP", apis: []string{"message"}, transports: []string{"kcp"}},
		{name: "QUIC", apis: []string{"message"}, transports: []string{"quic"}},
		{name: "UDP", apis: []string{"message"}, transports: []string{"udp"}},
		{name: "WebSocket", apis: []string{"message"}, transports: []string{"websocket"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan, err := buildServiceAssemblyPlan(serviceProjectConfig{
				APIVersion: serviceProjectAPIVersion,
				APIs:       test.apis,
				Transports: test.transports,
			}, serviceProjectManifest(test.apis...))
			if err != nil {
				t.Fatal(err)
			}
			files, err := plan.OrderedFiles()
			if err != nil {
				t.Fatal(err)
			}
			if len(files) != 2 {
				t.Fatalf("files = %v, want generated entrypoint plus one scaffold", serviceAssemblyPaths(files))
			}
			for _, file := range files {
				if _, err := format.Source(file.Content); err != nil {
					t.Fatalf("%s is not gofmt-compatible: %v\n%s", file.Path, err, file.Content)
				}
			}
		})
	}
}

func TestBuildServiceAssemblyPlanRejectsManifestDrift(t *testing.T) {
	httpProject := serviceProjectConfig{
		APIVersion: serviceProjectAPIVersion,
		APIs:       []string{"http"},
	}
	tests := []struct {
		name  string
		value manifest.Manifest
		want  string
	}{
		{
			name:  "selected source missing",
			value: serviceProjectManifest("grpc"),
			want:  `API "http" requires manifest source "http-api"`,
		},
		{
			name: "selected source kind mismatch",
			value: manifest.Manifest{
				APIVersion: manifest.APIVersionV1Alpha1,
				Module:     "example.com/orders",
				Service:    "orders_api",
				Sources: []manifest.Source{{
					ID:      "http-api",
					Kind:    manifest.KindProto,
					Path:    "api/http/service.proto",
					Package: "internal/httpapi",
				}},
			},
			want: `manifest source "http-api" has kind "proto", want "openapi"`,
		},
		{
			name:  "manifest source omitted from assembly",
			value: serviceProjectManifest("http", "grpc"),
			want:  `manifest source "grpc-api" requires API "grpc"`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := buildServiceAssemblyPlan(httpProject, test.value)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestServiceAssemblyScaffoldsAreCreateOnce(t *testing.T) {
	root := t.TempDir()
	firstPlan, err := buildServiceAssemblyPlan(serviceProjectConfig{
		APIVersion: serviceProjectAPIVersion,
		APIs:       []string{"http"},
	}, serviceProjectManifest("http"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := codegen.Execute(firstPlan, codegen.Options{Root: root}); err != nil {
		t.Fatal(err)
	}
	httpPath := filepath.Join(root, "internal", "kproapp", "http.go")
	customHTTP := []byte("package kproapp\n\n// application customization\nfunc customHTTP() {}\n")
	if err := os.WriteFile(httpPath, customHTTP, 0o644); err != nil {
		t.Fatal(err)
	}
	oldEndpoints, err := os.ReadFile(filepath.Join(root, "internal", "kproapp", "endpoints.gen.go"))
	if err != nil {
		t.Fatal(err)
	}

	secondPlan, err := buildServiceAssemblyPlan(serviceProjectConfig{
		APIVersion: serviceProjectAPIVersion,
		APIs:       []string{"grpc", "http"},
	}, serviceProjectManifest("http", "grpc"))
	if err != nil {
		t.Fatal(err)
	}
	result, err := codegen.Execute(secondPlan, codegen.Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(httpPath); err != nil || !bytes.Equal(got, customHTTP) {
		t.Fatalf("HTTP scaffold was overwritten: err=%v\n%s", err, got)
	}
	if _, err := os.Stat(filepath.Join(root, "internal", "kproapp", "grpc.go")); err != nil {
		t.Fatalf("gRPC scaffold was not created: %v", err)
	}
	newEndpoints, err := os.ReadFile(filepath.Join(root, "internal", "kproapp", "endpoints.gen.go"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(oldEndpoints, newEndpoints) || !bytes.Contains(newEndpoints, []byte("mountGRPC(app)")) {
		t.Fatalf("generated endpoints were not updated:\n%s", newEndpoints)
	}
	if len(result.Created) != 1 || result.Created[0] != "internal/kproapp/grpc.go" {
		t.Fatalf("created = %v, want only gRPC scaffold", result.Created)
	}
	if len(result.Updated) != 1 || result.Updated[0] != "internal/kproapp/endpoints.gen.go" {
		t.Fatalf("updated = %v, want only generated endpoints", result.Updated)
	}

	unchanged, err := codegen.Execute(secondPlan, codegen.Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.Changed() {
		t.Fatalf("second identical assembly execution changed files: %+v", unchanged)
	}
}

func serviceProjectManifest(apis ...string) manifest.Manifest {
	value := manifest.Manifest{
		APIVersion: manifest.APIVersionV1Alpha1,
		Module:     "example.com/orders",
		Service:    "orders_api",
	}
	for _, api := range apis {
		switch api {
		case "http":
			value.Sources = append(value.Sources, manifest.Source{
				ID:      "http-api",
				Kind:    manifest.KindOpenAPI,
				Path:    "api/http/openapi.yaml",
				Package: "internal/httpapi",
			})
		case "grpc":
			value.Sources = append(value.Sources, manifest.Source{
				ID:      "grpc-api",
				Kind:    manifest.KindProto,
				Path:    "api/grpc/service.proto",
				Package: "internal/grpcapi",
			})
		case "message":
			value.Sources = append(value.Sources, manifest.Source{
				ID:      "message-api",
				Kind:    manifest.KindMessage,
				Path:    "api/message/messages.yaml",
				Package: "internal/messageapi",
			})
		}
	}
	return value
}

func serviceAssemblyPaths(files []codegen.File) []string {
	paths := make([]string, 0, len(files))
	for _, file := range files {
		paths = append(paths, file.Path)
	}
	return paths
}

func serviceAssemblyContent(t *testing.T, files []codegen.File, name string) []byte {
	t.Helper()
	for _, file := range files {
		if file.Path == name {
			return file.Content
		}
	}
	t.Fatalf("missing assembly file %q in %v", name, serviceAssemblyPaths(files))
	return nil
}
