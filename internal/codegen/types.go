// Package codegen provides the small, file-system safe foundation used by
// KPro generators. It deliberately has no knowledge of a particular schema or
// template language: callers first build a virtual Plan and then execute it.
package codegen

import (
	"errors"
	"io/fs"
)

// Ownership describes who may change an output after it has been created.
type Ownership string

const (
	// Generated files are entirely generator-owned. They are updated and may be
	// removed when no longer present in a plan, but only while their contents
	// still match the previous lock entry.
	Generated Ownership = "generated"
	// User files are created only on their first appearance. Later invocations
	// preserve both edits and deliberate deletion by the user.
	User Ownership = "user"
	// Scaffold files are create-once starter files. They have the same safe
	// preservation behavior as User files, while retaining their distinct
	// provenance in the lock for diagnostics and future generators.
	Scaffold Ownership = "scaffold"
)

// File is one virtual output in a Plan. Path must be relative to the execution
// root. Content is the generator input: Go files are formatted before they are
// written, and the lock records hashes of both Content and the final output.
type File struct {
	Path      string
	Content   []byte
	Ownership Ownership
	// Mode is used for a newly written file. Zero means 0644.
	Mode fs.FileMode
}

// Plan is a deterministic, virtual set of desired files. BuildPlan and
// NewPlan normalize and order Files, but Execute also validates a literal Plan
// so callers cannot bypass the safety checks.
type Plan struct {
	Files []File
}

// Mode controls whether Execute mutates the file system.
type Mode string

const (
	// Apply writes the plan and lock.
	Apply Mode = "apply"
	// Check validates the plan and returns ErrOutOfDate if applying it would
	// change files or the lock.
	Check Mode = "check"
	// DryRun validates and reports changes without writing them.
	DryRun Mode = "dry-run"

	// Verbose aliases make call sites that prefer ModeApply equally clear.
	ModeApply  = Apply
	ModeCheck  = Check
	ModeDryRun = DryRun
)

const (
	// DefaultLockFile is relative to Options.Root and intentionally has no
	// platform-specific path separator.
	DefaultLockFile = ".kpro-codegen.lock.json"
	lockVersion     = 1
)

var (
	// ErrUnsafePath reports an absolute, escaping, or otherwise unsafe path.
	ErrUnsafePath = errors.New("codegen: unsafe relative path")
	// ErrDuplicatePath reports two virtual files targeting one normalized path.
	ErrDuplicatePath = errors.New("codegen: duplicate output path")
	// ErrGeneratedModified reports a generator-owned output changed outside of
	// a successful generator invocation.
	ErrGeneratedModified = errors.New("codegen: generated output was modified")
	// ErrUnmanagedPath reports a generated output which would overwrite a file
	// not recorded as generator-owned.
	ErrUnmanagedPath = errors.New("codegen: refusing to overwrite unmanaged path")
	// ErrOutOfDate is returned by Check when Apply would make a change.
	ErrOutOfDate = errors.New("codegen: generated outputs are out of date")
)

// Options configure Execute.
type Options struct {
	// Root is the directory containing all outputs. An empty root means the
	// current directory.
	Root string
	// LockFile is a safe path relative to Root. Empty uses DefaultLockFile.
	LockFile string
	Mode     Mode
}

// Result lists deterministic, slash-separated paths affected by the selected
// plan. In Check and DryRun these are proposed changes; in Apply they are the
// changes performed. LockChanged includes lock-only updates.
type Result struct {
	Created     []string
	Updated     []string
	Removed     []string
	Unchanged   []string
	LockChanged bool
}

// Changed reports whether applying the plan would change an output or lock.
func (r Result) Changed() bool {
	return r.LockChanged || len(r.Created) != 0 || len(r.Updated) != 0 || len(r.Removed) != 0
}
