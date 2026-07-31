package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestNewAppliesVersionedLocalScaffoldTemplateOverlay(t *testing.T) {
	templateRoot := filepath.Join(t.TempDir(), "company-template")
	writeLocalScaffoldTemplate(t, templateRoot, `apiVersion: kpro.dev/scaffold-template/v1alpha1
name: company-minimal
version: v1.2.3
profile: minimal
files:
  - source: files/readme.tmpl
    target: README.md
    action: replace
  - source: files/run.tmpl
    target: scripts/run.sh
    action: add
    mode: "0755"
`, map[string]string{
		"files/readme.tmpl": "# {{.ServiceName}}\n\nmodule: {{.Module}}\n",
		"files/run.tmpl":    "#!/bin/sh\necho {{.ServiceIdentifier}}\n",
	})

	target := filepath.Join(t.TempDir(), "orders")
	var output bytes.Buffer
	if err := execute([]string{
		"new",
		target,
		"--module", "example.com/orders",
		"--template", templateRoot,
	}, &output, &output); err != nil {
		t.Fatal(err)
	}

	readme, err := os.ReadFile(filepath.Join(target, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(readme), "# orders\n\nmodule: example.com/orders\n"; got != want {
		t.Fatalf("custom README = %q, want %q", got, want)
	}
	runPath := filepath.Join(target, "scripts", "run.sh")
	runScript, err := os.ReadFile(runPath)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(runScript), "#!/bin/sh\necho orders\n"; got != want {
		t.Fatalf("custom script = %q, want %q", got, want)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(runPath)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o755 {
			t.Fatalf("custom script mode = %#o, want 0755", got)
		}
	}

	lockData, err := os.ReadFile(filepath.Join(target, scaffoldTemplateLockFile))
	if err != nil {
		t.Fatal(err)
	}
	var lock scaffoldTemplateProvenance
	if err := json.Unmarshal(lockData, &lock); err != nil {
		t.Fatalf("decode scaffold template lock: %v", err)
	}
	if lock.Version != 1 || lock.Template.Name != "company-minimal" || lock.Template.Version != "v1.2.3" || len(lock.Template.SHA256) != 64 {
		t.Fatalf("unexpected scaffold template lock: %+v", lock)
	}
	if strings.Contains(string(lockData), templateRoot) {
		t.Fatalf("scaffold template lock leaks local path:\n%s", lockData)
	}
	if !strings.Contains(output.String(), "using scaffold template company-minimal v1.2.3 (sha256:"+lock.Template.SHA256+")") {
		t.Fatalf("new output does not report pinned template:\n%s", output.String())
	}
}

func TestNewLocalScaffoldTemplateConditions(t *testing.T) {
	templateRoot := filepath.Join(t.TempDir(), "service-template")
	writeLocalScaffoldTemplate(t, templateRoot, `apiVersion: kpro.dev/scaffold-template/v1alpha1
name: company-service
version: v0.4.0
profile: service
files:
  - source: files/http.tmpl
    target: company/http.txt
    action: add
    when:
      apis: [http]
  - source: files/message.tmpl
    target: company/message.txt
    action: add
    when:
      apis: [message]
      transports: [tcp]
`, map[string]string{
		"files/http.tmpl":    "http {{.ServiceName}}\n",
		"files/message.tmpl": "message {{.ServiceName}}\n",
	})

	target := filepath.Join(t.TempDir(), "conditional")
	var output bytes.Buffer
	if err := execute([]string{
		"new",
		"--module", "example.com/conditional",
		"--profile", "service",
		"--api", "http",
		"--template", templateRoot,
		target,
	}, &output, &output); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(target, "company", "http.txt")); err != nil {
		t.Fatalf("selected HTTP template file is missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(target, "company", "message.txt")); !os.IsNotExist(err) {
		t.Fatalf("unselected Message template file exists: %v", err)
	}
	output.Reset()
	if err := execute([]string{"generate", "--root", target, "--check"}, &output, &output); err != nil {
		t.Fatalf("customized service scaffold is not current: %v\n%s", err, output.String())
	}
}

func TestParseLocalScaffoldTemplateManifestRejectsInvalidContracts(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		contain string
	}{
		{
			name: "unknown field",
			value: `apiVersion: kpro.dev/scaffold-template/v1alpha1
name: company
version: v1.0.0
profile: minimal
unknown: true
files: []
`,
			contain: "field unknown not found",
		},
		{
			name: "unsupported API version",
			value: `apiVersion: kpro.dev/scaffold-template/v2
name: company
version: v1.0.0
profile: minimal
files: []
`,
			contain: "unsupported apiVersion",
		},
		{
			name: "invalid semantic version",
			value: `apiVersion: kpro.dev/scaffold-template/v1alpha1
name: company
version: main
profile: minimal
files: []
`,
			contain: "semantic version",
		},
		{
			name:    "unsafe source",
			value:   localTemplateManifestWithFile("minimal", "../secret", "extra.txt", "add", ""),
			contain: "unsafe source",
		},
		{
			name:    "unsupported action",
			value:   localTemplateManifestWithFile("minimal", "files/value.tmpl", "extra.txt", "delete", ""),
			contain: "unsupported action",
		},
		{
			name:    "unsupported mode",
			value:   localTemplateManifestWithFile("minimal", "files/value.tmpl", "extra.txt", "add", "0600"),
			contain: "unsupported mode",
		},
		{
			name: "minimal condition",
			value: `apiVersion: kpro.dev/scaffold-template/v1alpha1
name: company
version: v1.0.0
profile: minimal
files:
  - source: files/value.tmpl
    target: extra.txt
    action: add
    when:
      apis: [http]
`,
			contain: "minimal profile has no API",
		},
		{
			name: "portable source alias",
			value: `apiVersion: kpro.dev/scaffold-template/v1alpha1
name: company
version: v1.0.0
profile: minimal
files:
  - source: files/Value.tmpl
    target: one.txt
    action: add
  - source: files/value.tmpl
    target: two.txt
    action: add
`,
			contain: "aliases or repeats",
		},
		{
			name: "unicode-normalized source alias",
			value: `apiVersion: kpro.dev/scaffold-template/v1alpha1
name: company
version: v1.0.0
profile: minimal
files:
  - source: files/é.tmpl
    target: one.txt
    action: add
  - source: files/é.tmpl
    target: two.txt
    action: add
`,
			contain: "aliases or repeats",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseLocalScaffoldTemplateManifest([]byte(test.value))
			if err == nil || !strings.Contains(err.Error(), test.contain) {
				t.Fatalf("parse error = %v, want containing %q", err, test.contain)
			}
		})
	}
}

func TestNewLocalScaffoldTemplateRejectsUnsafeOverlayWithoutPublishing(t *testing.T) {
	tests := []struct {
		name       string
		manifest   string
		files      map[string]string
		contain    string
		serviceAPI bool
	}{
		{
			name:     "portable reserved lock alias",
			manifest: localTemplateManifestWithFile("minimal", "files/value.tmpl", "KPRO.SCAFFOLD.LOCK.JSON", "add", ""),
			files:    map[string]string{"files/value.tmpl": "reserved\n"},
			contain:  "reserved lock file",
		},
		{
			name:     "reserved generation lock parent",
			manifest: localTemplateManifestWithFile("minimal", "files/value.tmpl", "KPRO.GEN.LOCK.JSON/child", "add", ""),
			files:    map[string]string{"files/value.tmpl": "reserved\n"},
			contain:  "reserved lock file",
		},
		{
			name:     "add collides with built in",
			manifest: localTemplateManifestWithFile("minimal", "files/value.tmpl", "readme.md", "add", ""),
			files:    map[string]string{"files/value.tmpl": "collision\n"},
			contain:  "collides with",
		},
		{
			name:     "file parent collision",
			manifest: localTemplateManifestWithFile("minimal", "files/value.tmpl", "README.md/child.txt", "add", ""),
			files:    map[string]string{"files/value.tmpl": "collision\n"},
			contain:  "duplicate output path",
		},
		{
			name:     "replace must name built in",
			manifest: localTemplateManifestWithFile("minimal", "files/value.tmpl", "missing.txt", "replace", ""),
			files:    map[string]string{"files/value.tmpl": "missing\n"},
			contain:  "not a selected built-in",
		},
		{
			name:     "unsafe rendered target",
			manifest: localTemplateManifestWithFile("minimal", "files/value.tmpl", "../outside.txt", "add", ""),
			files:    map[string]string{"files/value.tmpl": "unsafe\n"},
			contain:  "unsafe target",
		},
		{
			name:     "missing template key",
			manifest: localTemplateManifestWithFile("minimal", "files/value.tmpl", "{{.Missing}}/value.txt", "add", ""),
			files:    map[string]string{"files/value.tmpl": "missing key\n"},
			contain:  "can't evaluate field Missing",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			parent := t.TempDir()
			templateRoot := filepath.Join(parent, "template")
			writeLocalScaffoldTemplate(t, templateRoot, test.manifest, test.files)
			target := filepath.Join(parent, "target")
			args := []string{"new", "--module", "example.com/target", "--template", templateRoot}
			if test.serviceAPI {
				args = append(args, "--profile", "service", "--api", "http")
			}
			args = append(args, target)
			var output bytes.Buffer
			err := execute(args, &output, &output)
			if err == nil || !strings.Contains(err.Error(), test.contain) {
				t.Fatalf("new error = %v, want containing %q\n%s", err, test.contain, output.String())
			}
			if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
				t.Fatalf("invalid template published target: %v", statErr)
			}
			assertNoScaffoldStages(t, parent)
		})
	}
}

func TestNewLocalScaffoldTemplateRejectsSymlinkSources(t *testing.T) {
	parent := t.TempDir()
	templateRoot := filepath.Join(parent, "template")
	if err := os.MkdirAll(filepath.Join(templateRoot, "files"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := localTemplateManifestWithFile("minimal", "files/link.tmpl", "extra.txt", "add", "")
	if err := os.WriteFile(filepath.Join(templateRoot, localScaffoldTemplateManifestFile), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	realSource := filepath.Join(parent, "real.tmpl")
	if err := os.WriteFile(realSource, []byte("outside\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realSource, filepath.Join(templateRoot, "files", "link.tmpl")); err != nil {
		t.Skipf("symbolic links are unavailable: %v", err)
	}

	target := filepath.Join(parent, "target")
	var output bytes.Buffer
	err := execute([]string{"new", "--module", "example.com/target", "--template", templateRoot, target}, &output, &output)
	if err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("symlink source error = %v\n%s", err, output.String())
	}
	if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
		t.Fatalf("symlink template published target: %v", statErr)
	}
	assertNoScaffoldStages(t, parent)
}

func TestNewLocalScaffoldTemplateGenerationFailureIsAtomic(t *testing.T) {
	parent := t.TempDir()
	templateRoot := filepath.Join(parent, "template")
	writeLocalScaffoldTemplate(t, templateRoot, localTemplateManifestWithFile(
		"service",
		"files/generation.tmpl",
		"kpro.gen.yaml",
		"replace",
		"",
	), map[string]string{
		"files/generation.tmpl": "apiVersion: invalid\n",
	})
	target := filepath.Join(parent, "target")
	var output bytes.Buffer
	err := execute([]string{
		"new",
		"--module", "example.com/target",
		"--profile", "service",
		"--api", "http",
		"--template", templateRoot,
		target,
	}, &output, &output)
	if err == nil || !strings.Contains(err.Error(), "unsupported apiVersion") {
		t.Fatalf("generation failure = %v\n%s", err, output.String())
	}
	if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
		t.Fatalf("failed generation published target: %v", statErr)
	}
	assertNoScaffoldStages(t, parent)
}

func TestLocalScaffoldTemplateDigestIsDeterministicAndContentAddressed(t *testing.T) {
	manifest := []byte("manifest\n")
	left := map[string][]byte{
		"z.tmpl": []byte("z"),
		"a.tmpl": []byte("a"),
	}
	right := map[string][]byte{
		"a.tmpl": []byte("a"),
		"z.tmpl": []byte("z"),
	}
	leftDigest := localScaffoldTemplateDigest(manifest, left)
	if rightDigest := localScaffoldTemplateDigest(manifest, right); rightDigest != leftDigest {
		t.Fatalf("digest depends on map iteration: left=%s right=%s", leftDigest, rightDigest)
	}
	right["z.tmpl"] = []byte("changed")
	if changed := localScaffoldTemplateDigest(manifest, right); changed == leftDigest {
		t.Fatalf("digest did not change with source content: %s", changed)
	}
}

func localTemplateManifestWithFile(profile, source, target, action, mode string) string {
	modeLine := ""
	if mode != "" {
		modeLine = "    mode: \"" + mode + "\"\n"
	}
	return "apiVersion: kpro.dev/scaffold-template/v1alpha1\n" +
		"name: company\n" +
		"version: v1.0.0\n" +
		"profile: " + profile + "\n" +
		"files:\n" +
		"  - source: " + source + "\n" +
		"    target: '" + target + "'\n" +
		"    action: " + action + "\n" +
		modeLine
}

func writeLocalScaffoldTemplate(t *testing.T, root, manifest string, files map[string]string) {
	t.Helper()
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, localScaffoldTemplateManifestFile), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	for relative, content := range files {
		filename := filepath.Join(root, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(filename), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filename, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
