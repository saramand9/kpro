package codegen

import (
	"bytes"
	"fmt"
	"go/format"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type preparedFile struct {
	File
	inputHash, outputHash string
	output                []byte
}

type operation uint8

const (
	opNone operation = iota
	opCreate
	opUpdate
	opRemove
)

// Execute validates the complete Plan before changing the filesystem. Apply
// writes it, Check returns ErrOutOfDate for pending changes, and DryRun only
// reports the changes. Every mode rejects externally modified generated files.
func Execute(plan Plan, options Options) (Result, error) {
	return execute(plan, options, defaultFileTransactionOps)
}

func execute(plan Plan, options Options, transactionOps fileTransactionOps) (Result, error) {
	mode := options.Mode
	if mode == "" {
		mode = Apply
	}
	if mode != Apply && mode != Check && mode != DryRun {
		return Result{}, fmt.Errorf("codegen: unknown execution mode %q", mode)
	}
	files, err := plan.OrderedFiles()
	if err != nil {
		return Result{}, err
	}
	root, err := absoluteRoot(options.Root)
	if err != nil {
		return Result{}, err
	}
	lockName := options.LockFile
	if lockName == "" {
		lockName = DefaultLockFile
	}
	lockName, err = SafeRelativePath(lockName)
	if err != nil {
		return Result{}, fmt.Errorf("lock file: %w", err)
	}
	for _, file := range files {
		conflicts, conflictErr := PortablePathsCollide(file.Path, lockName)
		if conflictErr != nil {
			return Result{}, conflictErr
		}
		if conflicts {
			return Result{}, fmt.Errorf("codegen: lock file conflicts with output %q", file.Path)
		}
	}
	if err := ensureRootSafe(root); err != nil {
		return Result{}, err
	}
	lockPath, err := joinUnderRoot(root, lockName)
	if err != nil {
		return Result{}, err
	}
	if err := ensureParentSafe(root, lockPath); err != nil {
		return Result{}, err
	}
	if err := ensureNoSymlink(lockPath); err != nil {
		return Result{}, err
	}
	// Decode and later compare-and-swap the exact same lock snapshot. Reading
	// again after planning could accept an external lock update as the write
	// precondition for a plan derived from stale state.
	oldData, lockExists, err := existingBytes(lockPath)
	if err != nil {
		return Result{}, err
	}
	lock := NewLock()
	if lockExists {
		lock, err = DecodeLock(oldData)
		if err != nil {
			return Result{}, err
		}
	}
	prepared, err := prepare(files)
	if err != nil {
		return Result{}, err
	}
	for _, file := range prepared {
		target, _ := joinUnderRoot(root, file.Path)
		if err := ensureParentSafe(root, target); err != nil {
			return Result{}, err
		}
		if err := ensureNoSymlink(target); err != nil {
			return Result{}, err
		}
	}

	next := NewLock()
	planned := make(map[string]struct{}, len(prepared))
	ops := make(map[string]operation, len(prepared))
	preconditions := make(map[string]FilePrecondition, len(prepared)+1)
	var missingStaleGenerated []string
	result := Result{}
	for _, file := range prepared {
		planned[file.Path] = struct{}{}
		target, _ := joinUnderRoot(root, file.Path)
		current, exists, err := readRegularFile(target)
		if err != nil {
			return Result{}, err
		}
		previous, known := lock.Files[file.Path]
		preconditions[file.Path] = ExpectFileMissing()
		if exists {
			preconditions[file.Path] = ExpectFileContent(current)
		}
		if known && previous.Ownership == Generated && exists && hashBytes(current) != previous.OutputSHA256 {
			return Result{}, fmt.Errorf("%w: %s", ErrGeneratedModified, file.Path)
		}
		entry := LockEntry{InputSHA256: file.inputHash, Ownership: file.Ownership}
		switch file.Ownership {
		case Generated:
			entry.OutputSHA256 = file.outputHash
			if !exists {
				ops[file.Path] = opCreate
				result.Created = append(result.Created, file.Path)
			} else if !known || previous.Ownership != Generated {
				return Result{}, fmt.Errorf("%w: %s", ErrUnmanagedPath, file.Path)
			} else if !bytes.Equal(current, file.output) {
				ops[file.Path] = opUpdate
				result.Updated = append(result.Updated, file.Path)
			} else {
				result.Unchanged = append(result.Unchanged, file.Path)
			}
		case User, Scaffold:
			if exists {
				entry.OutputSHA256 = hashBytes(current)
				result.Unchanged = append(result.Unchanged, file.Path)
			} else if known && (previous.Ownership == User || previous.Ownership == Scaffold) {
				// A missing create-once file is a deliberate user deletion.
				entry.OutputSHA256 = previous.OutputSHA256
				result.Unchanged = append(result.Unchanged, file.Path)
			} else {
				entry.OutputSHA256 = file.outputHash
				ops[file.Path] = opCreate
				result.Created = append(result.Created, file.Path)
			}
		}
		next.Files[file.Path] = entry
	}
	for name, old := range lock.Files {
		if _, ok := planned[name]; ok {
			continue
		}
		if old.Ownership != Generated {
			next.Files[name] = old
			continue
		}
		target, err := joinUnderRoot(root, name)
		if err != nil {
			return Result{}, fmt.Errorf("stale lock path %q: %w", name, err)
		}
		if err := ensureParentSafe(root, target); err != nil {
			return Result{}, err
		}
		if err := ensureNoSymlink(target); err != nil {
			return Result{}, err
		}
		current, exists, err := readRegularFile(target)
		if err != nil {
			return Result{}, err
		}
		if exists && hashBytes(current) != old.OutputSHA256 {
			return Result{}, fmt.Errorf("%w: stale %s", ErrGeneratedModified, name)
		}
		if exists {
			preconditions[name] = ExpectFileContent(current)
			ops[name] = opRemove
			result.Removed = append(result.Removed, name)
		} else {
			preconditions[name] = ExpectFileMissing()
			missingStaleGenerated = append(missingStaleGenerated, name)
		}
	}
	nextData, err := EncodeLock(next)
	if err != nil {
		return Result{}, err
	}
	result.LockChanged = !lockExists || !bytes.Equal(oldData, nextData)
	sortResult(&result)
	if mode == Check && result.Changed() {
		return result, ErrOutOfDate
	}
	if mode == DryRun || !result.Changed() {
		return result, nil
	}
	// All writes, stale removals, and the lock update commit as one rollback-capable
	// compare-and-swap batch. A later failure therefore cannot leave successfully
	// replaced outputs paired with the old lock document.
	mutations := make([]FileMutation, 0, len(ops)+1)
	for _, file := range prepared {
		if ops[file.Path] == opCreate || ops[file.Path] == opUpdate {
			fileMode := file.Mode
			if ops[file.Path] == opUpdate {
				// File.Mode is a creation mode. Preserve an existing output's mode.
				fileMode = 0
			}
			mutations = append(mutations, FileMutation{
				Path:         file.Path,
				Content:      file.output,
				Mode:         fileMode,
				Precondition: preconditions[file.Path],
			})
		} else {
			mutations = append(mutations, FileMutation{
				Path:         file.Path,
				Precondition: preconditions[file.Path],
				ObserveOnly:  true,
			})
		}
	}
	removals := make([]string, 0)
	for name, op := range ops {
		if op == opRemove {
			removals = append(removals, name)
		}
	}
	sort.Strings(removals)
	for _, name := range removals {
		mutations = append(mutations, FileMutation{
			Path:         name,
			Precondition: preconditions[name],
			Remove:       true,
		})
	}
	sort.Strings(missingStaleGenerated)
	for _, name := range missingStaleGenerated {
		mutations = append(mutations, FileMutation{
			Path:         name,
			Precondition: preconditions[name],
			ObserveOnly:  true,
		})
	}
	if result.LockChanged || len(mutations) != 0 {
		lockPrecondition := ExpectFileMissing()
		if lockExists {
			lockPrecondition = ExpectFileContent(oldData)
		}
		var lockMode fs.FileMode
		if result.LockChanged {
			lockMode = 0o644
		}
		mutations = append(mutations, FileMutation{
			Path:         lockName,
			Content:      nextData,
			Mode:         lockMode,
			Precondition: lockPrecondition,
		})
	}
	if _, statErr := os.Stat(root); os.IsNotExist(statErr) {
		if mkdirErr := os.MkdirAll(root, 0o755); mkdirErr != nil {
			return Result{}, fmt.Errorf("create codegen root: %w", mkdirErr)
		}
	} else if statErr != nil {
		return Result{}, fmt.Errorf("inspect codegen root: %w", statErr)
	}
	transactionResult, err := commitFileTransaction(
		mutations,
		FileTransactionOptions{Root: root},
		transactionOps,
	)
	if err != nil {
		return Result{}, fmt.Errorf("apply codegen transaction: %w", err)
	}
	result.CleanupWarnings = append(result.CleanupWarnings, transactionResult.CleanupWarnings...)
	return result, nil
}

func prepare(files []File) ([]preparedFile, error) {
	prepared := make([]preparedFile, 0, len(files))
	for _, file := range files {
		output := append([]byte(nil), file.Content...)
		if strings.EqualFold(filepath.Ext(file.Path), ".go") {
			formatted, err := format.Source(output)
			if err != nil {
				return nil, fmt.Errorf("gofmt %s: %w", file.Path, err)
			}
			output = formatted
		}
		prepared = append(prepared, preparedFile{File: file, inputHash: hashBytes(file.Content), output: output, outputHash: hashBytes(output)})
	}
	return prepared, nil
}

func absoluteRoot(root string) (string, error) {
	if root == "" {
		root = "."
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve codegen root: %w", err)
	}
	return filepath.Clean(abs), nil
}

func joinUnderRoot(root, name string) (string, error) {
	name, err := SafeRelativePath(name)
	if err != nil {
		return "", err
	}
	target := filepath.Join(root, filepath.FromSlash(name))
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", ErrUnsafePath
	}
	return target, nil
}

func ensureRootSafe(root string) error {
	info, err := os.Lstat(root)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect codegen root: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("codegen root %q is not a directory", root)
	}
	return nil
}

func ensureParentSafe(root, target string) error {
	for dir := filepath.Dir(target); ; dir = filepath.Dir(dir) {
		if dir == root {
			return nil
		}
		info, err := os.Lstat(dir)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return fmt.Errorf("inspect output directory %q: %w", dir, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("codegen output directory %q is not a directory", dir)
		}
		if dir == filepath.Dir(dir) {
			return ErrUnsafePath
		}
	}
}

func ensureNoSymlink(filename string) error {
	info, err := os.Lstat(filename)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect output %q: %w", filename, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("codegen refuses symlink output %q", filename)
	}
	return nil
}

func readRegularFile(filename string) ([]byte, bool, error) {
	info, err := os.Stat(filename)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if !info.Mode().IsRegular() {
		return nil, false, fmt.Errorf("codegen output %q is not a regular file", filename)
	}
	data, err := os.ReadFile(filename)
	return data, true, err
}

func existingBytes(filename string) ([]byte, bool, error) {
	data, err := os.ReadFile(filename)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	return data, err == nil, err
}

func sortResult(result *Result) {
	sort.Strings(result.Created)
	sort.Strings(result.Updated)
	sort.Strings(result.Removed)
	sort.Strings(result.Unchanged)
}
