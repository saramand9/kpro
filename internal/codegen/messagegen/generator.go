package messagegen

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/saramand9/kpro/internal/codegen"
	"github.com/saramand9/kpro/internal/codegen/manifest"
)

// Generator loads one manifest message source and returns a virtual output
// plan. Planning reads the contract but never writes into Root.
type Generator struct {
	// Root contains the manifest source paths. An empty Root means the current
	// directory.
	Root string
}

// Plan implements the source-generator seam used by the KPro CLI.
func (generator Generator) Plan(
	value manifest.Manifest,
	source manifest.Source,
) ([]codegen.File, error) {
	if err := value.Validate(); err != nil {
		return nil, fmt.Errorf("message codegen: invalid manifest: %w", err)
	}
	if source.Kind != manifest.KindMessage {
		return nil, fmt.Errorf(
			"message codegen: source %q has kind %q, want %q",
			source.ID,
			source.Kind,
			manifest.KindMessage,
		)
	}
	if !manifestContains(value, source) {
		return nil, fmt.Errorf(
			"message codegen: source %q is not present in the manifest",
			source.ID,
		)
	}
	switch strings.ToLower(path.Ext(source.Path)) {
	case ".yaml", ".yml", ".json":
	default:
		return nil, fmt.Errorf(
			"message codegen: source %q must use a .yaml, .yml, or .json extension",
			source.Path,
		)
	}

	root, err := resolveRoot(generator.Root)
	if err != nil {
		return nil, err
	}
	filename, err := resolveInput(root, source.Path)
	if err != nil {
		return nil, err
	}
	document, err := Load(filename)
	if err != nil {
		return nil, err
	}
	plan, err := BuildPlan(document, Options{
		Module: value.Module,
		Output: source.Package,
	})
	if err != nil {
		return nil, err
	}
	return plan.OrderedFiles()
}

func resolveRoot(configured string) (string, error) {
	if configured == "" {
		configured = "."
	}
	absolute, err := filepath.Abs(configured)
	if err != nil {
		return "", fmt.Errorf("message codegen: resolve root: %w", err)
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return "", fmt.Errorf("message codegen: inspect root: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("message codegen: root %q is not a directory", configured)
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("message codegen: resolve root links: %w", err)
	}
	return resolved, nil
}

func resolveInput(root, portablePath string) (string, error) {
	normalized, err := codegen.SafeRelativePath(portablePath)
	if err != nil {
		return "", fmt.Errorf(
			"message codegen: unsafe source path %q: %w",
			portablePath,
			err,
		)
	}
	filename := filepath.Join(root, filepath.FromSlash(normalized))
	resolved, err := filepath.EvalSymlinks(filename)
	if err != nil {
		return "", fmt.Errorf(
			"message codegen: resolve source %q: %w",
			portablePath,
			err,
		)
	}
	relative, err := filepath.Rel(root, resolved)
	if err != nil ||
		relative == ".." ||
		strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf(
			"message codegen: source %q resolves outside the project root",
			portablePath,
		)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf(
			"message codegen: inspect source %q: %w",
			portablePath,
			err,
		)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf(
			"message codegen: source %q is not a regular file",
			portablePath,
		)
	}
	return resolved, nil
}

func manifestContains(value manifest.Manifest, source manifest.Source) bool {
	for _, candidate := range value.Sources {
		if reflect.DeepEqual(candidate, source) {
			return true
		}
	}
	return false
}
