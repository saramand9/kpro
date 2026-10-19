package main

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"text/template"
	"unicode"

	"github.com/saramand9/kpro/internal/codegen"
	"github.com/saramand9/kpro/internal/codegen/manifest"
	"gopkg.in/yaml.v3"
)

const (
	serviceProjectAPIVersion  = "kpro.dev/service/v1alpha1"
	defaultServiceProjectFile = "kpro.service.yaml"
)

var (
	serviceProjectAPIOrder       = []string{"http", "grpc", "message"}
	serviceProjectTransportOrder = []string{"tcp", "kcp", "quic", "udp", "websocket"}
)

//go:embed templates/service-project/*
var serviceProjectTemplateFiles embed.FS

// serviceProjectConfig is the versioned, declarative endpoint-assembly input.
// Contract generation remains in kpro.gen.yaml; this file only records which
// generated APIs are mounted and which transports expose a Message API.
type serviceProjectConfig struct {
	APIVersion string   `yaml:"apiVersion"`
	APIs       []string `yaml:"apis"`
	Transports []string `yaml:"transports,omitempty"`
}

type serviceAssemblyData struct {
	Module          string
	ServiceExported string
	HTTPPackage     string
	GRPCPackage     string
	MessagePackage  string
	HasHTTP         bool
	HasGRPC         bool
	HasMessage      bool
	HasTCP          bool
	HasKCP          bool
	HasQUIC         bool
	HasUDP          bool
	HasWebSocket    bool
}

type serviceAssemblyTemplate struct {
	source    string
	target    string
	ownership codegen.Ownership
}

var serviceAssemblyTemplates = []serviceAssemblyTemplate{
	{
		source:    "templates/service-project/endpoints.go.tmpl",
		target:    "internal/kproapp/endpoints.gen.go",
		ownership: codegen.Generated,
	},
	{
		source:    "templates/service-project/http.go.tmpl",
		target:    "internal/kproapp/http.go",
		ownership: codegen.Scaffold,
	},
	{
		source:    "templates/service-project/grpc.go.tmpl",
		target:    "internal/kproapp/grpc.go",
		ownership: codegen.Scaffold,
	},
	{
		source:    "templates/service-project/message.go.tmpl",
		target:    "internal/kproapp/message.go",
		ownership: codegen.Scaffold,
	},
}

func loadServiceProjectConfig(filename string) (serviceProjectConfig, error) {
	data, err := os.ReadFile(filename) // #nosec G304 -- Local CLI reads an operator-selected project or artifact path.
	if err != nil {
		return serviceProjectConfig{}, fmt.Errorf("read service project config: %w", err)
	}
	return parseServiceProjectConfig(data)
}

func loadConfiguredServiceProject(root string) (serviceProjectConfig, bool, error) {
	candidate := filepath.Join(root, defaultServiceProjectFile)
	if _, err := os.Lstat(candidate); os.IsNotExist(err) {
		for _, name := range []string{"endpoints.gen.go", "http.go", "grpc.go", "message.go"} {
			marker := filepath.Join(root, "internal", "kproapp", name)
			if _, markerErr := os.Lstat(marker); markerErr == nil {
				return serviceProjectConfig{}, false, fmt.Errorf(
					"%s is missing from an established service project; restore it before validate or generate",
					defaultServiceProjectFile,
				)
			} else if !os.IsNotExist(markerErr) {
				return serviceProjectConfig{}, false, fmt.Errorf("inspect service project marker %q: %w", filepath.ToSlash(marker), markerErr)
			}
		}
		return serviceProjectConfig{}, false, nil
	} else if err != nil {
		return serviceProjectConfig{}, false, fmt.Errorf("inspect service project config: %w", err)
	}
	resolved, err := resolveProjectFile(root, defaultServiceProjectFile)
	if err != nil {
		return serviceProjectConfig{}, false, fmt.Errorf("resolve service project config: %w", err)
	}
	project, err := loadServiceProjectConfig(resolved)
	if err != nil {
		return serviceProjectConfig{}, false, err
	}
	return project, true, nil
}

func parseServiceProjectConfig(data []byte) (serviceProjectConfig, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)

	var value serviceProjectConfig
	if err := decoder.Decode(&value); err != nil {
		return serviceProjectConfig{}, fmt.Errorf("decode service project config: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return serviceProjectConfig{}, errors.New("decode service project config: multiple YAML documents are not supported")
		}
		return serviceProjectConfig{}, fmt.Errorf("decode service project config: %w", err)
	}
	return normalizeServiceProjectConfig(value)
}

func encodeServiceProjectConfig(value serviceProjectConfig) ([]byte, error) {
	normalized, err := normalizeServiceProjectConfig(value)
	if err != nil {
		return nil, err
	}
	var output bytes.Buffer
	encoder := yaml.NewEncoder(&output)
	encoder.SetIndent(2)
	if err := encoder.Encode(normalized); err != nil {
		return nil, fmt.Errorf("encode service project config: %w", err)
	}
	if err := encoder.Close(); err != nil {
		return nil, fmt.Errorf("encode service project config: %w", err)
	}
	return output.Bytes(), nil
}

func normalizeServiceProjectConfig(value serviceProjectConfig) (serviceProjectConfig, error) {
	value.APIVersion = strings.TrimSpace(value.APIVersion)
	if value.APIVersion != serviceProjectAPIVersion {
		return serviceProjectConfig{}, fmt.Errorf(
			"validate service project config: unsupported apiVersion %q",
			value.APIVersion,
		)
	}
	apis, err := canonicalServiceProjectSelections("API", value.APIs, serviceProjectAPIOrder)
	if err != nil {
		return serviceProjectConfig{}, err
	}
	if len(apis) == 0 {
		return serviceProjectConfig{}, errors.New("validate service project config: at least one API is required")
	}
	transports, err := canonicalServiceProjectSelections(
		"transport",
		value.Transports,
		serviceProjectTransportOrder,
	)
	if err != nil {
		return serviceProjectConfig{}, err
	}
	hasMessage := serviceProjectHasSelection(apis, "message")
	if hasMessage && len(transports) == 0 {
		return serviceProjectConfig{}, errors.New(
			"validate service project config: Message API requires at least one transport",
		)
	}
	if !hasMessage && len(transports) != 0 {
		return serviceProjectConfig{}, errors.New(
			"validate service project config: transports require the Message API",
		)
	}
	value.APIs = apis
	value.Transports = transports
	return value, nil
}

func canonicalServiceProjectSelections(label string, values, allowed []string) ([]string, error) {
	selected := make(map[string]struct{}, len(values))
	for _, raw := range values {
		value := strings.ToLower(strings.TrimSpace(raw))
		if !serviceProjectHasSelection(allowed, value) {
			return nil, fmt.Errorf(
				"validate service project config: unsupported %s %q (supported: %s)",
				label,
				raw,
				strings.Join(allowed, ", "),
			)
		}
		if _, exists := selected[value]; exists {
			return nil, fmt.Errorf(
				"validate service project config: duplicate %s %q",
				label,
				value,
			)
		}
		selected[value] = struct{}{}
	}
	ordered := make([]string, 0, len(selected))
	for _, value := range allowed {
		if _, exists := selected[value]; exists {
			ordered = append(ordered, value)
		}
	}
	return ordered, nil
}

func serviceProjectHasSelection(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func buildServiceAssemblyPlan(
	project serviceProjectConfig,
	value manifest.Manifest,
) (codegen.Plan, error) {
	project, err := normalizeServiceProjectConfig(project)
	if err != nil {
		return codegen.Plan{}, err
	}
	if err := value.Validate(); err != nil {
		return codegen.Plan{}, fmt.Errorf("build service assembly: invalid generation manifest: %w", err)
	}

	data := serviceAssemblyData{
		Module:          value.Module,
		ServiceExported: serviceAssemblyExportedIdentifier(value.Service),
		HasHTTP:         serviceProjectHasSelection(project.APIs, "http"),
		HasGRPC:         serviceProjectHasSelection(project.APIs, "grpc"),
		HasMessage:      serviceProjectHasSelection(project.APIs, "message"),
		HasTCP:          serviceProjectHasSelection(project.Transports, "tcp"),
		HasKCP:          serviceProjectHasSelection(project.Transports, "kcp"),
		HasQUIC:         serviceProjectHasSelection(project.Transports, "quic"),
		HasUDP:          serviceProjectHasSelection(project.Transports, "udp"),
		HasWebSocket:    serviceProjectHasSelection(project.Transports, "websocket"),
	}
	expectedSources := []struct {
		api    string
		id     string
		kind   manifest.SourceKind
		target *string
	}{
		{api: "http", id: "http-api", kind: manifest.KindOpenAPI, target: &data.HTTPPackage},
		{api: "grpc", id: "grpc-api", kind: manifest.KindProto, target: &data.GRPCPackage},
		{api: "message", id: "message-api", kind: manifest.KindMessage, target: &data.MessagePackage},
	}
	for _, expected := range expectedSources {
		source, exists := serviceAssemblySource(value.Sources, expected.id)
		selected := serviceProjectHasSelection(project.APIs, expected.api)
		if selected && !exists {
			return codegen.Plan{}, fmt.Errorf(
				"build service assembly: API %q requires manifest source %q",
				expected.api,
				expected.id,
			)
		}
		if !selected && exists {
			return codegen.Plan{}, fmt.Errorf(
				"build service assembly: manifest source %q requires API %q in %s",
				expected.id,
				expected.api,
				defaultServiceProjectFile,
			)
		}
		if !selected {
			continue
		}
		if source.Kind != expected.kind {
			return codegen.Plan{}, fmt.Errorf(
				"build service assembly: manifest source %q has kind %q, want %q",
				expected.id,
				source.Kind,
				expected.kind,
			)
		}
		*expected.target = source.Package
	}

	files := make([]codegen.File, 0, len(serviceAssemblyTemplates))
	for _, candidate := range serviceAssemblyTemplates {
		switch candidate.target {
		case "internal/kproapp/http.go":
			if !data.HasHTTP {
				continue
			}
		case "internal/kproapp/grpc.go":
			if !data.HasGRPC {
				continue
			}
		case "internal/kproapp/message.go":
			if !data.HasMessage {
				continue
			}
		}
		content, err := renderServiceAssemblyTemplate(candidate.source, data)
		if err != nil {
			return codegen.Plan{}, err
		}
		files = append(files, codegen.File{
			Path:      candidate.target,
			Content:   content,
			Ownership: candidate.ownership,
			Mode:      0o644,
		})
	}
	plan, err := codegen.BuildPlan(files...)
	if err != nil {
		return codegen.Plan{}, fmt.Errorf("build service assembly plan: %w", err)
	}
	return plan, nil
}

func serviceAssemblySource(sources []manifest.Source, id string) (manifest.Source, bool) {
	for _, source := range sources {
		if source.ID == id {
			return source, true
		}
	}
	return manifest.Source{}, false
}

func renderServiceAssemblyTemplate(filename string, data serviceAssemblyData) ([]byte, error) {
	name := path.Base(filename)
	parsed, err := template.New(name).Option("missingkey=error").ParseFS(
		serviceProjectTemplateFiles,
		filename,
	)
	if err != nil {
		return nil, fmt.Errorf("parse service assembly template %q: %w", filename, err)
	}
	var output bytes.Buffer
	if err := parsed.ExecuteTemplate(&output, name, data); err != nil {
		return nil, fmt.Errorf("render service assembly template %q: %w", filename, err)
	}
	return output.Bytes(), nil
}

func serviceAssemblyExportedIdentifier(value string) string {
	var output strings.Builder
	upperNext := true
	for _, character := range value {
		if character == '_' {
			upperNext = true
			continue
		}
		if upperNext {
			character = unicode.ToUpper(character)
			upperNext = false
		}
		output.WriteRune(character)
	}
	return output.String()
}
