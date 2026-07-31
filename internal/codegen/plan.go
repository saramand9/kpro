package codegen

import (
	"fmt"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
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
	seen := make(map[string]string, len(p.Files))
	for i := range p.Files {
		normalized, err := SafeRelativePath(p.Files[i].Path)
		if err != nil {
			return fmt.Errorf("file %q: %w", p.Files[i].Path, err)
		}
		if !validOwnership(p.Files[i].Ownership) {
			return fmt.Errorf("file %q: invalid ownership %q", normalized, p.Files[i].Ownership)
		}
		key, err := PortablePathKey(normalized)
		if err != nil {
			return fmt.Errorf("file %q: %w", p.Files[i].Path, err)
		}
		if previous, ok := seen[key]; ok {
			return fmt.Errorf("%w: %q aliases %q", ErrDuplicatePath, normalized, previous)
		}
		seen[key] = normalized
		p.Files[i].Path = normalized
	}
	for _, name := range seen {
		for parent := path.Dir(name); parent != "."; parent = path.Dir(parent) {
			parentKey, err := PortablePathKey(parent)
			if err != nil {
				return err
			}
			if parentName, isFile := seen[parentKey]; isFile {
				return fmt.Errorf("%w: %q is both a file and parent of %q", ErrDuplicatePath, parentName, name)
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
// slash-separated form. It rejects traversal, absolute paths, and names which
// cannot be represented consistently on supported Windows filesystems, even
// when a plan is produced on another operating system.
func SafeRelativePath(name string) (string, error) {
	if name == "" || !utf8.ValidString(name) || strings.IndexByte(name, 0) >= 0 {
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
		if !safePortableSegment(segment) {
			return "", ErrUnsafePath
		}
	}
	clean := path.Clean(portable)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", ErrUnsafePath
	}
	return clean, nil
}

// PortablePathKey returns the physical-identity key used to compare generated
// paths across supported filesystems. The returned key is slash-separated and
// Unicode canonical-normalized and case-folded so a plan prepared on a
// case-sensitive host cannot create outputs which alias each other on a
// case-insensitive or normalization-insensitive host.
func PortablePathKey(name string) (string, error) {
	normalized, err := SafeRelativePath(name)
	if err != nil {
		return "", err
	}
	normalized = norm.NFC.String(normalized)
	var key strings.Builder
	key.Grow(len(normalized))
	for _, character := range normalized {
		folded := character
		for next := unicode.SimpleFold(character); next != character; next = unicode.SimpleFold(next) {
			if next < folded {
				folded = next
			}
		}
		key.WriteRune(unicode.ToLower(folded))
	}
	return key.String(), nil
}

// PortablePathsCollide reports whether two file paths identify the same
// portable filesystem entry, or whether either file would have to be the other
// path's parent directory.
func PortablePathsCollide(left, right string) (bool, error) {
	leftKey, err := PortablePathKey(left)
	if err != nil {
		return false, err
	}
	rightKey, err := PortablePathKey(right)
	if err != nil {
		return false, err
	}
	return leftKey == rightKey ||
		strings.HasPrefix(leftKey, rightKey+"/") ||
		strings.HasPrefix(rightKey, leftKey+"/"), nil
}

func safePortableSegment(segment string) bool {
	if segment == "" || strings.HasSuffix(segment, " ") || strings.HasSuffix(segment, ".") {
		return false
	}
	for _, character := range segment {
		if character < ' ' || strings.ContainsRune(`<>"|?*:`, character) || unicode.IsControl(character) {
			return false
		}
	}
	base := segment
	if dot := strings.IndexByte(base, '.'); dot >= 0 {
		base = base[:dot]
	}
	base = strings.TrimRight(base, " .")
	switch strings.ToUpper(base) {
	case "CON", "PRN", "AUX", "NUL", "CONIN$", "CONOUT$",
		"COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9",
		"LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9",
		"COM¹", "COM²", "COM³", "LPT¹", "LPT²", "LPT³":
		return false
	default:
		return true
	}
}
