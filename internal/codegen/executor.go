package codegen

import (
	"bytes"
	"errors"
	"fmt"
	"go/format"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
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
		if file.Path == lockName {
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
	lock, err := readLockDocument(lockPath)
	if err != nil {
		return Result{}, err
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
	result := Result{}
	for _, file := range prepared {
		planned[file.Path] = struct{}{}
		target, _ := joinUnderRoot(root, file.Path)
		current, exists, err := readRegularFile(target)
		if err != nil {
			return Result{}, err
		}
		previous, known := lock.Files[file.Path]
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
			ops[name] = opRemove
			result.Removed = append(result.Removed, name)
		}
	}
	oldData, lockExists, err := existingBytes(lockPath)
	if err != nil {
		return Result{}, err
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
	// All inputs, current outputs, and stale entries were checked above.
	for _, file := range prepared {
		if ops[file.Path] == opCreate || ops[file.Path] == opUpdate {
			target, _ := joinUnderRoot(root, file.Path)
			if err := atomicWrite(target, file.output, file.Mode); err != nil {
				return Result{}, fmt.Errorf("write %s: %w", file.Path, err)
			}
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
		target, _ := joinUnderRoot(root, name)
		if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
			return Result{}, fmt.Errorf("remove stale %s: %w", name, err)
		}
	}
	if result.LockChanged {
		if err := atomicWrite(lockPath, nextData, 0o644); err != nil {
			return Result{}, fmt.Errorf("write codegen lock: %w", err)
		}
	}
	return result, nil
}

func readLockDocument(filename string) (Lock, error) {
	data, err := os.ReadFile(filename)
	if os.IsNotExist(err) {
		return NewLock(), nil
	}
	if err != nil {
		return Lock{}, fmt.Errorf("read codegen lock: %w", err)
	}
	return DecodeLock(data)
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

func atomicWrite(filename string, data []byte, mode fs.FileMode) error {
	if mode == 0 {
		mode = 0o644
	}
	if err := os.MkdirAll(filepath.Dir(filename), 0o755); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(filename), ".kpro-codegen-*")
	if err != nil {
		return err
	}
	tempName := temporary.Name()
	defer os.Remove(tempName)
	if err := temporary.Chmod(mode.Perm()); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempName, filename); err == nil {
		return nil
	} else if runtime.GOOS != "windows" {
		return err
	}

	// Windows does not allow Rename to replace an existing destination. Move
	// the destination aside in the same directory, then restore it if the
	// replacement move fails. The backup keeps a failed update recoverable.
	backup, err := os.CreateTemp(filepath.Dir(filename), ".kpro-codegen-backup-*")
	if err != nil {
		return err
	}
	backupName := backup.Name()
	if err := backup.Close(); err != nil {
		return err
	}
	if err := os.Remove(backupName); err != nil {
		return err
	}
	if err := os.Rename(filename, backupName); err != nil {
		return err
	}
	if err := os.Rename(tempName, filename); err != nil {
		if restoreErr := os.Rename(backupName, filename); restoreErr != nil {
			return errors.Join(err, fmt.Errorf("restore original output: %w", restoreErr))
		}
		return err
	}
	if err := os.Remove(backupName); err != nil {
		return fmt.Errorf("remove replacement backup: %w", err)
	}
	return nil
}

func sortResult(result *Result) {
	sort.Strings(result.Created)
	sort.Strings(result.Updated)
	sort.Strings(result.Removed)
	sort.Strings(result.Unchanged)
}
