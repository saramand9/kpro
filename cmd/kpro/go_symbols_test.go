package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/saramand9/kpro/internal/codegen"
	"github.com/saramand9/kpro/internal/codegen/manifest"
)

func TestBuildGenerationPlanRejectsRealMessageSourcesWithSharedSymbols(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "api"), 0o755); err != nil {
		t.Fatal(err)
	}
	contract := []byte(`apiVersion: kpro.dev/message/v1alpha1
service: shared
encoding: json
messages:
  - id: 100
    name: health
    dispatch: sync
    request:
      fields: []
    response:
      fields: []
`)
	for _, name := range []string{"first.yaml", "second.yaml"} {
		if err := os.WriteFile(filepath.Join(root, "api", name), contract, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	value := manifest.Manifest{
		APIVersion: manifest.APIVersionV1Alpha1,
		Module:     "example.com/message-conflict",
		Service:    "message_conflict",
		Sources: []manifest.Source{
			{ID: "first", Kind: manifest.KindMessage, Path: "api/first.yaml", Package: "internal/shared"},
			{ID: "second", Kind: manifest.KindMessage, Path: "api/second.yaml", Package: "internal/shared"},
		},
	}
	_, err := buildGenerationPlan(root, value, false)
	if err == nil || !strings.Contains(err.Error(), "Go symbol") || !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("real message symbol conflict error = %v", err)
	}
}

func TestBuildGenerationPlanRejectsCrossSourceGoSymbolConflict(t *testing.T) {
	previous := sourceGenerators
	sourceGenerators = map[manifest.SourceKind]sourceGenerator{
		manifest.KindMessage: sourceGeneratorFunc(func(_ string, _ manifest.Manifest, source manifest.Source) (codegen.Plan, error) {
			return codegen.BuildPlan(codegen.File{
				Path:      source.Package + "/" + source.ID + ".gen.go",
				Content:   []byte("package shared\n\ntype SharedRequest struct{}\n"),
				Ownership: codegen.Generated,
			})
		}),
	}
	t.Cleanup(func() { sourceGenerators = previous })

	value := manifest.Manifest{
		APIVersion: manifest.APIVersionV1Alpha1,
		Module:     "example.com/conflict",
		Service:    "conflict",
		Sources: []manifest.Source{
			{ID: "first", Kind: manifest.KindMessage, Path: "api/first.yaml", Package: "internal/shared"},
			{ID: "second", Kind: manifest.KindMessage, Path: "api/second.yaml", Package: "internal/shared"},
		},
	}
	_, err := buildGenerationPlan(t.TempDir(), value, false)
	if err == nil || !strings.Contains(err.Error(), "SharedRequest") || !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("cross-source symbol conflict error = %v", err)
	}
}

func TestBuildGenerationPlanRejectsSymbolsInAliasedPackageDirectories(t *testing.T) {
	previous := sourceGenerators
	sourceGenerators = map[manifest.SourceKind]sourceGenerator{
		manifest.KindMessage: sourceGeneratorFunc(func(_ string, _ manifest.Manifest, source manifest.Source) (codegen.Plan, error) {
			return codegen.BuildPlan(codegen.File{
				Path:      source.Package + "/" + source.ID + ".gen.go",
				Content:   []byte("package shared\n\ntype SharedRequest struct{}\n"),
				Ownership: codegen.Generated,
			})
		}),
	}
	t.Cleanup(func() { sourceGenerators = previous })

	value := manifest.Manifest{
		APIVersion: manifest.APIVersionV1Alpha1,
		Module:     "example.com/alias-conflict",
		Service:    "alias_conflict",
		Sources: []manifest.Source{
			{ID: "first", Kind: manifest.KindMessage, Path: "api/first.yaml", Package: "internal/foo"},
			{ID: "second", Kind: manifest.KindMessage, Path: "api/second.yaml", Package: "internal/FOO"},
		},
	}
	_, err := buildGenerationPlan(t.TempDir(), value, false)
	if err == nil || !strings.Contains(err.Error(), "SharedRequest") || !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("aliased package-directory symbol conflict error = %v", err)
	}
}

func TestBuildGenerationPlanAllowsSafeProtoSourcesInSharedPackage(t *testing.T) {
	previous := sourceGenerators
	sourceGenerators = map[manifest.SourceKind]sourceGenerator{
		manifest.KindProto: sourceGeneratorFunc(func(_ string, _ manifest.Manifest, source manifest.Source) (codegen.Plan, error) {
			name := "First"
			if source.ID == "second" {
				name = "Second"
			}
			return codegen.BuildPlan(codegen.File{
				Path:      source.Package + "/" + source.ID + ".pb.go",
				Content:   []byte("package shared\n\ntype " + name + " struct{}\n"),
				Ownership: codegen.Generated,
			})
		}),
	}
	t.Cleanup(func() { sourceGenerators = previous })

	value := manifest.Manifest{
		APIVersion: manifest.APIVersionV1Alpha1,
		Module:     "example.com/safe",
		Service:    "safe",
		Sources: []manifest.Source{
			{ID: "first", Kind: manifest.KindProto, Path: "api/first.proto", Package: "internal/shared"},
			{ID: "second", Kind: manifest.KindProto, Path: "api/second.proto", Package: "internal/shared"},
		},
	}
	if _, err := buildGenerationPlan(t.TempDir(), value, false); err != nil {
		t.Fatalf("safe shared proto package rejected: %v", err)
	}
}
