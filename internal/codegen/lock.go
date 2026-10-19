package codegen

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"sort"
	"strings"
	"unicode/utf8"
)

// Lock records the exact generator input and final output for each emitted
// file. It is intentionally content-addressed, rather than timestamp-based.
type Lock struct {
	Version int                  `json:"version"`
	Files   map[string]LockEntry `json:"files"`
}

// LockEntry is one content-addressed lock record.
type LockEntry struct {
	InputSHA256  string    `json:"input_sha256"`
	OutputSHA256 string    `json:"output_sha256"`
	Ownership    Ownership `json:"ownership"`
}

// NewLock returns an empty, current-version lock.
func NewLock() Lock { return Lock{Version: lockVersion, Files: make(map[string]LockEntry)} }

// ReadLock reads and validates a lock. A missing file is represented by a new
// empty lock, making first use explicit but convenient.
func ReadLock(filename string) (Lock, error) {
	data, err := os.ReadFile(filename) // #nosec G304 -- Local CLI reads an operator-selected project or artifact path.
	if os.IsNotExist(err) {
		return NewLock(), nil
	}
	if err != nil {
		return Lock{}, fmt.Errorf("read codegen lock: %w", err)
	}
	return DecodeLock(data)

}

// Validate checks lock data before it is trusted for writes or stale removal.
func (l Lock) Validate() error {
	if l.Version != lockVersion {
		return fmt.Errorf("codegen: unsupported lock version %d", l.Version)
	}
	names := make([]string, 0, len(l.Files))
	for name := range l.Files {
		names = append(names, name)
	}
	sort.Strings(names)
	seen := make(map[string]string, len(names))
	for _, name := range names {
		entry := l.Files[name]
		normalized, err := SafeRelativePath(name)
		if err != nil {
			return fmt.Errorf("lock file %q: %w", name, err)
		}
		if normalized != name {
			return fmt.Errorf("lock file %q is not canonical; use %q", name, normalized)
		}
		key, err := PortablePathKey(name)
		if err != nil {
			return fmt.Errorf("lock file %q: %w", name, err)
		}
		if previous, duplicate := seen[key]; duplicate {
			return fmt.Errorf("%w in lock: %q aliases %q", ErrDuplicatePath, name, previous)
		}
		seen[key] = name
		if !validOwnership(entry.Ownership) || !isSHA256(entry.InputSHA256) || !isSHA256(entry.OutputSHA256) {
			return fmt.Errorf("codegen: invalid lock entry for %q", name)
		}
	}
	for _, name := range names {
		for parent := path.Dir(name); parent != "."; parent = path.Dir(parent) {
			parentKey, err := PortablePathKey(parent)
			if err != nil {
				return err
			}
			if parentName, collision := seen[parentKey]; collision {
				return fmt.Errorf(
					"%w in lock: %q is both a file and parent of %q",
					ErrDuplicatePath,
					parentName,
					name,
				)
			}
		}
	}
	return nil
}

// EncodeLock produces a deterministic, newline-terminated lock document.
func EncodeLock(lock Lock) ([]byte, error) {
	if err := lock.Validate(); err != nil {
		return nil, err
	}
	// encoding/json currently sorts string map keys. Use an ordered map-free
	// document nevertheless so deterministic output remains an API guarantee.
	type item struct {
		Path string `json:"path"`
		LockEntry
	}
	type document struct {
		Version int    `json:"version"`
		Files   []item `json:"files"`
	}
	names := make([]string, 0, len(lock.Files))
	for name := range lock.Files {
		names = append(names, name)
	}
	sort.Strings(names)
	doc := document{Version: lock.Version, Files: make([]item, 0, len(names))}
	for _, name := range names {
		doc.Files = append(doc.Files, item{Path: name, LockEntry: lock.Files[name]})
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode codegen lock: %w", err)
	}
	return append(data, '\n'), nil
}

// DecodeLock accepts the deterministic document emitted by EncodeLock.
func DecodeLock(data []byte) (Lock, error) {
	if !utf8.Valid(data) {
		return Lock{}, fmt.Errorf("decode codegen lock: document is not valid UTF-8")
	}
	type item struct {
		Path string `json:"path"`
		LockEntry
	}
	type document struct {
		Version int    `json:"version"`
		Files   []item `json:"files"`
	}
	var doc document
	if err := json.Unmarshal(data, &doc); err != nil {
		return Lock{}, fmt.Errorf("decode codegen lock: %w", err)
	}
	lock := Lock{Version: doc.Version, Files: make(map[string]LockEntry, len(doc.Files))}
	for _, item := range doc.Files {
		if _, exists := lock.Files[item.Path]; exists {
			return Lock{}, fmt.Errorf("%w in lock: %q", ErrDuplicatePath, item.Path)
		}
		lock.Files[item.Path] = item.LockEntry
	}
	if err := lock.Validate(); err != nil {
		return Lock{}, err
	}
	return lock, nil
}

func hashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func isSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	if value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
