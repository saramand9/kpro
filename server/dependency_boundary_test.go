package server

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestServerSourceDependsOnlyOnTransportContracts(t *testing.T) {
	directory, err := os.Getwd()
	if err != nil {
		t.Fatalf("resolve server source directory: %v", err)
	}
	modulePath := dependencyBoundaryModulePath(t, filepath.Join(directory, "..", "go.mod"))
	allowed := map[string]struct{}{
		modulePath + "/core":      {},
		modulePath + "/transport": {},
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatalf("read server source directory: %v", err)
	}

	filesChecked := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		filesChecked++
		path := filepath.Join(directory, name)
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			t.Errorf("parse %s: %v", name, err)
			continue
		}
		for _, spec := range file.Imports {
			importPath, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				t.Errorf("unquote import in %s: %v", name, err)
				continue
			}
			if _, ok := allowed[importPath]; ok {
				continue
			}
			firstSegment := strings.SplitN(importPath, "/", 2)[0]
			if strings.Contains(firstSegment, ".") {
				t.Errorf("%s imports non-standard package %q; server may depend only on core and root transport contracts", name, importPath)
			}
		}
	}
	if filesChecked == 0 {
		t.Fatal("no non-test server source files checked")
	}
}

func dependencyBoundaryModulePath(t *testing.T, goModPath string) string {
	t.Helper()
	contents, err := os.ReadFile(goModPath)
	if err != nil {
		t.Fatalf("read %s: %v", goModPath, err)
	}
	for _, line := range strings.Split(string(contents), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "module" {
			return fields[1]
		}
	}
	t.Fatalf("module directive not found in %s", goModPath)
	return ""
}
