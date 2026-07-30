package messagegen

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/saramand9/kpro/internal/codegen"
	"github.com/saramand9/kpro/internal/codegen/manifest"
)

func TestGeneratorPlansManifestSourceWithoutProjectWrites(t *testing.T) {
	root, value, source := messageProject(t, validMessageSpec)
	generator := Generator{Root: root}

	first, err := generator.Plan(value, source)
	if err != nil {
		t.Fatalf("first Plan() error = %v", err)
	}
	second, err := generator.Plan(value, source)
	if err != nil {
		t.Fatalf("second Plan() error = %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatal("Generator.Plan() is not deterministic")
	}
	if len(first) != 4 {
		t.Fatalf("plan files = %d, want 4: %#v", len(first), first)
	}
	for _, file := range first {
		if file.Ownership != codegen.Generated && file.Ownership != codegen.Scaffold {
			t.Fatalf("file %q has ownership %q", file.Path, file.Ownership)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "internal")); !os.IsNotExist(err) {
		t.Fatalf("planning wrote project outputs, stat error = %v", err)
	}
}

func TestGeneratorRejectsInvalidSourceWithoutProjectWrites(t *testing.T) {
	root, value, source := messageProject(
		t,
		strings.Replace(validMessageSpec, "type: string", "type: timestamp", 1),
	)
	files, err := (Generator{Root: root}).Plan(value, source)
	if err == nil || !strings.Contains(err.Error(), `type "timestamp"`) || len(files) != 0 {
		t.Fatalf("Plan() = %#v, %v; want unsupported type and no files", files, err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "internal")); !os.IsNotExist(statErr) {
		t.Fatalf("failed planning wrote project outputs, stat error = %v", statErr)
	}
}

func TestGeneratorChecksKindMembershipAndExtensionBeforeReading(t *testing.T) {
	root, value, source := messageProject(t, validMessageSpec)
	tests := []struct {
		name    string
		mutate  func(*manifest.Manifest, *manifest.Source)
		contain string
	}{
		{
			name: "kind",
			mutate: func(value *manifest.Manifest, source *manifest.Source) {
				source.Kind = manifest.KindOpenAPI
				value.Sources[0] = *source
			},
			contain: `has kind "openapi", want "message"`,
		},
		{
			name: "membership",
			mutate: func(_ *manifest.Manifest, source *manifest.Source) {
				source.ID = "other-source"
			},
			contain: "is not present in the manifest",
		},
		{
			name: "extension",
			mutate: func(value *manifest.Manifest, source *manifest.Source) {
				source.Path = "api/orders.txt"
				value.Sources[0] = *source
			},
			contain: "must use a .yaml, .yml, or .json extension",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			testValue := value
			testValue.Sources = append([]manifest.Source(nil), value.Sources...)
			testSource := source
			test.mutate(&testValue, &testSource)
			_, err := (Generator{Root: root}).Plan(testValue, testSource)
			if err == nil || !strings.Contains(err.Error(), test.contain) {
				t.Fatalf("Plan() error = %v, want %q", err, test.contain)
			}
		})
	}
}

func TestGeneratorRejectsEscapingSymlinkWhenSupported(t *testing.T) {
	root, value, source := messageProject(t, validMessageSpec)
	outside := filepath.Join(t.TempDir(), "outside.yaml")
	if err := os.WriteFile(outside, []byte(validMessageSpec), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "api", "linked.yaml")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink creation is unavailable: %v", err)
	}
	source.Path = "api/linked.yaml"
	value.Sources[0] = source

	_, err := (Generator{Root: root}).Plan(value, source)
	if err == nil || !strings.Contains(err.Error(), "resolves outside the project root") {
		t.Fatalf("Plan() error = %v, want escaping symlink rejection", err)
	}
}

func messageProject(
	t *testing.T,
	spec string,
) (string, manifest.Manifest, manifest.Source) {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "api"), 0o755); err != nil {
		t.Fatal(err)
	}
	source := manifest.Source{
		ID:      "message-api",
		Kind:    manifest.KindMessage,
		Path:    "api/orders.message.yaml",
		Package: "internal/messages",
	}
	if err := os.WriteFile(
		filepath.Join(root, filepath.FromSlash(source.Path)),
		[]byte(spec),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	value := manifest.Manifest{
		APIVersion: manifest.APIVersionV1Alpha1,
		Module:     "example.com/orders",
		Service:    "orders",
		Sources:    []manifest.Source{source},
	}
	return root, value, source
}
