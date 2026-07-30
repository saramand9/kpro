package codegen

import (
	"fmt"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// NewPlan is an alias for BuildPlan.
func NewPlan(files ...File) (Plan, error) { return BuildPlan(files...) }

// BuildPlan copies, normalizes, validates, and deterministically orders files.
func BuildPlan(files ...File) (Plan, error) {
	p := Plan{Files: append([]File(nil), files...)}
	if err := p.normalizeAndValidate(); err != nil {
		return Plan{}, err
	}
	return p, nil
}

// Validate checks Plan without changing the caller's slice.
func (p Plan) Validate() error {
	copyPlan := Plan{Files: append([]File(nil), p.Files...)}
	return copyPlan.normalizeAndValidate()
}

// OrderedFiles returns a normalized, deterministic copy of the plan files.
func (p Plan) OrderedFiles() ([]File, error) {
	copyPlan := Plan{Files: append([]File(nil), p.Files...)}
	if err := copyPlan.normalizeAndValidate(); err != nil {
		return nil, err
	}
	for i := range copyPlan.Files {
		copyPlan.Files[i].Content = append([]byte(nil), copyPlan.Files[i].Content...)
	}
	return copyPlan.Files, nil
}

func (p *Plan) normalizeAndValidate() error {
	seen := make(map[string]struct{}, len(p.Files))
	for i := range p.Files {
		normalized, err := SafeRelativePath(p.Files[i].Path)
		if err != nil {
			return fmt.Errorf("file %q: %w", p.Files[i].Path, err)
		}
		if !validOwnership(p.Files[i].Ownership) {
			return fmt.Errorf("file %q: invalid ownership %q", normalized, p.Files[i].Ownership)
		}
		if _, ok := seen[normalized]; ok {
			return fmt.Errorf("%w: %q", ErrDuplicatePath, normalized)
		}
		seen[normalized] = struct{}{}
		p.Files[i].Path = normalized
	}
	for name := range seen {
		for parent := path.Dir(name); parent != "."; parent = path.Dir(parent) {
			if _, isFile := seen[parent]; isFile {
				return fmt.Errorf("%w: %q is both a file and parent of %q", ErrDuplicatePath, parent, name)
			}
		}
	}
	sort.Slice(p.Files, func(i, j int) bool { return p.Files[i].Path < p.Files[j].Path })
	return nil
}

func validOwnership(ownership Ownership) bool {
	return ownership == Generated || ownership == User || ownership == Scaffold
}

// SafeRelativePath converts a portable relative path to its canonical,
// slash-separated form. It rejects traversal and both Unix and Windows absolute
// paths even when a plan is produced on a different operating system.
func SafeRelativePath(name string) (string, error) {
	if name == "" || strings.IndexByte(name, 0) >= 0 {
		return "", ErrUnsafePath
	}
	if filepath.IsAbs(name) || path.IsAbs(name) || filepath.VolumeName(name) != "" {
		return "", ErrUnsafePath
	}
	portable := strings.ReplaceAll(name, `\`, "/")
	if strings.HasPrefix(portable, "/") || strings.HasPrefix(portable, "//") ||
		(len(portable) >= 2 && portable[1] == ':') {
		return "", ErrUnsafePath
	}
	for _, segment := range strings.Split(portable, "/") {
		if segment == "." || segment == ".." {
			return "", ErrUnsafePath
		}
	}
	clean := path.Clean(portable)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", ErrUnsafePath
	}
	return clean, nil
}
