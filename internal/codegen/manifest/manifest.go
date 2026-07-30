// Package manifest defines the versioned, declarative input consumed by the
// KPro code generator. The manifest intentionally describes code generation
// only; runtime addresses, credentials, TLS material, and deployment policy
// remain application-owned configuration.
package manifest

import (
	"bytes"
	"errors"
	"fmt"
	"go/token"
	"io"
	"os"
	"path"
	"regexp"
	"strings"

	"github.com/saramand9/kpro/internal/codegen"
	"golang.org/x/mod/module"
	"gopkg.in/yaml.v3"
)

const (
	// APIVersionV1Alpha1 is the first KPro generation-manifest contract.
	APIVersionV1Alpha1 = "kpro.dev/codegen/v1alpha1"

	// KindOpenAPI consumes an OpenAPI document and generates HTTP bindings.
	KindOpenAPI SourceKind = "openapi"
	// KindProto consumes protobuf service definitions and generates gRPC
	// bindings around the standard Go protobuf output.
	KindProto SourceKind = "proto"
	// KindMessage consumes a KPro message contract and generates bindings for
	// message transports.
	KindMessage SourceKind = "message"
)

var identifierPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)

// SourceKind selects a contract reader.
type SourceKind string

// Manifest is the root kpro.gen.yaml document.
type Manifest struct {
	APIVersion string   `yaml:"apiVersion"`
	Module     string   `yaml:"module"`
	Service    string   `yaml:"service"`
	Sources    []Source `yaml:"sources"`
}

// Source identifies one version-controlled contract and the application-owned
// package in which generated bindings and create-once handler stubs belong.
type Source struct {
	ID      string     `yaml:"id"`
	Kind    SourceKind `yaml:"kind"`
	Path    string     `yaml:"path"`
	Package string     `yaml:"package"`
}

// Load reads, strictly decodes, normalizes, and validates a manifest.
func Load(filename string) (Manifest, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return Manifest{}, fmt.Errorf("read codegen manifest: %w", err)
	}
	return Parse(data)
}

// Parse strictly decodes, normalizes, and validates one manifest document.
func Parse(data []byte) (Manifest, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)

	var value Manifest
	if err := decoder.Decode(&value); err != nil {
		return Manifest{}, fmt.Errorf("decode codegen manifest: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return Manifest{}, errors.New("decode codegen manifest: multiple YAML documents are not supported")
		}
		return Manifest{}, fmt.Errorf("decode codegen manifest: %w", err)
	}

	value.normalize()
	if err := value.Validate(); err != nil {
		return Manifest{}, err
	}
	return value, nil
}

// Validate checks stable identifiers, source uniqueness, and safe project
// relative paths before a generator can plan any writes.
func (m Manifest) Validate() error {
	if m.APIVersion != APIVersionV1Alpha1 {
		return fmt.Errorf("validate codegen manifest: unsupported apiVersion %q", m.APIVersion)
	}
	if err := module.CheckPath(m.Module); err != nil {
		return fmt.Errorf("validate codegen manifest: invalid module %q: %w", m.Module, err)
	}
	if !validGoIdentifier(m.Service) {
		return fmt.Errorf("validate codegen manifest: invalid service name %q", m.Service)
	}
	if len(m.Sources) == 0 {
		return errors.New("validate codegen manifest: at least one source is required")
	}

	ids := make(map[string]struct{}, len(m.Sources))
	contractPaths := make(map[string]string, len(m.Sources))
	for i, source := range m.Sources {
		prefix := fmt.Sprintf("validate codegen manifest: source[%d]", i)
		if !identifierPattern.MatchString(source.ID) {
			return fmt.Errorf("%s has invalid id %q", prefix, source.ID)
		}
		if _, exists := ids[source.ID]; exists {
			return fmt.Errorf("%s duplicates id %q", prefix, source.ID)
		}
		ids[source.ID] = struct{}{}

		switch source.Kind {
		case KindOpenAPI, KindProto, KindMessage:
		default:
			return fmt.Errorf("%s has unsupported kind %q", prefix, source.Kind)
		}
		if _, err := codegen.SafeRelativePath(source.Path); err != nil {
			return fmt.Errorf("%s has unsafe path %q: %w", prefix, source.Path, err)
		}
		if previous, exists := contractPaths[source.Path]; exists {
			return fmt.Errorf("%s reuses path %q from source %q", prefix, source.Path, previous)
		}
		contractPaths[source.Path] = source.ID
		if err := validatePackagePath(source.Package); err != nil {
			return fmt.Errorf("%s has invalid package %q: %w", prefix, source.Package, err)
		}
	}
	return nil
}

func (m *Manifest) normalize() {
	m.APIVersion = strings.TrimSpace(m.APIVersion)
	m.Module = strings.TrimSpace(m.Module)
	m.Service = strings.TrimSpace(m.Service)
	for i := range m.Sources {
		m.Sources[i].ID = strings.TrimSpace(m.Sources[i].ID)
		m.Sources[i].Kind = SourceKind(strings.TrimSpace(string(m.Sources[i].Kind)))
		if normalized, err := codegen.SafeRelativePath(strings.TrimSpace(m.Sources[i].Path)); err == nil {
			m.Sources[i].Path = normalized
		}
		if normalized, err := codegen.SafeRelativePath(strings.TrimSpace(m.Sources[i].Package)); err == nil {
			m.Sources[i].Package = normalized
		}
	}
}

func validatePackagePath(value string) error {
	normalized, err := codegen.SafeRelativePath(value)
	if err != nil {
		return err
	}
	for _, segment := range strings.Split(normalized, "/") {
		if !validGoIdentifier(segment) {
			return fmt.Errorf("path segment %q is not a Go identifier", segment)
		}
	}
	if path.Ext(normalized) != "" {
		return errors.New("package path must name a directory")
	}
	return nil
}

func validGoIdentifier(value string) bool {
	return token.IsIdentifier(value) && !token.Lookup(value).IsKeyword()
}
