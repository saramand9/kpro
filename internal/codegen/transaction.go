package codegen

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
)

// ErrTransactionConflict reports that a file no longer has the state the
// caller inspected before preparing a transaction.
var ErrTransactionConflict = errors.New("codegen: file transaction conflict")

// FilePrecondition is the expected state of a transaction target. The zero
// value expects the file not to exist.
type FilePrecondition struct {
	Exists bool
	SHA256 string
}

// ExpectFileMissing returns a precondition for a new file.
func ExpectFileMissing() FilePrecondition {
	return FilePrecondition{}
}

// ExpectFileContent returns a compare-and-swap precondition for an existing
// file. CommitFileTransaction rejects the transaction if the target content
// changed after the caller read it.
func ExpectFileContent(content []byte) FilePrecondition {
	return FilePrecondition{Exists: true, SHA256: hashBytes(content)}
}

// FileMutation creates, replaces, or removes one regular file. Path is relative
// to the transaction root. A zero Mode uses 0644 for a new file and preserves
// the existing permission bits for a replacement. Remove mutations require an
// existing-file precondition and cannot include Content or Mode. ObserveOnly
// mutations add an existing or missing path to the transaction read set
// without writing it, and cannot include Content, Mode, or Remove.
type FileMutation struct {
	Path         string
	Content      []byte
	Mode         fs.FileMode
	Precondition FilePrecondition
	Remove       bool
	ObserveOnly  bool
}

// FileTransactionOptions configures CommitFileTransaction.
type FileTransactionOptions struct {
	Root string
}

// FileTransactionResult describes a successfully committed transaction.
type FileTransactionResult struct {
	Created         []string
	Updated         []string
	Removed         []string
	Unchanged       []string
	CleanupWarnings []string
}

// Changed reports whether the transaction changed at least one target.
func (r FileTransactionResult) Changed() bool {
	return len(r.Created) != 0 || len(r.Updated) != 0 || len(r.Removed) != 0
}

type preparedMutation struct {
	FileMutation
	target      string
	existed     bool
	current     []byte
	currentMode fs.FileMode
	outputMode  fs.FileMode
	outputHash  string
	stage       string
	stageInfo   fs.FileInfo
	backup      string
	backupHash  string
	backupMode  fs.FileMode
	backupInfo  fs.FileInfo
	backupKnown bool
	installed   bool
	modeApplied bool
}

type fileTransactionOps struct {
	rename      func(string, string) error
	link        func(string, string) error
	remove      func(string) error
	chmod       func(string, fs.FileMode) error
	chmodHandle func(*os.File, string, fs.FileMode) error
}

var defaultFileTransactionOps = fileTransactionOps{
	rename: os.Rename,
	link:   os.Link,
	remove: os.Remove,
	chmod:  os.Chmod,
	chmodHandle: func(file *os.File, _ string, mode fs.FileMode) error {
		return file.Chmod(mode)
	},
}

// CommitFileTransaction applies a compare-and-swap batch of file creations,
// replacements, and removals. It validates every target before making
// filesystem changes, stages each output in its target directory, and keeps
// same-directory backups until every mutation is installed. A commit error triggers an inverse,
// best-effort rollback; any backup that cannot be restored is retained and its
// exact path is included in the returned error. Once every target is installed,
// backup cleanup failures do not make a committed transaction fail. They are
// reported in FileTransactionResult.CleanupWarnings with the retained backup's
// absolute path.
//
// The caller must serialize transactions for one project. The preconditions
// detect concurrent content changes, but this function is not a filesystem
// lock or a crash-atomic multi-file primitive.
func CommitFileTransaction(
	mutations []FileMutation,
	options FileTransactionOptions,
) (FileTransactionResult, error) {
	return commitFileTransaction(mutations, options, defaultFileTransactionOps)
}

func commitFileTransaction(
	mutations []FileMutation,
	options FileTransactionOptions,
	ops fileTransactionOps,
) (FileTransactionResult, error) {
	if ops.rename == nil || ops.link == nil || ops.remove == nil || ops.chmod == nil || ops.chmodHandle == nil {
		return FileTransactionResult{}, errors.New("codegen: file transaction operations are incomplete")
	}
	normalized, err := normalizeFileMutations(mutations)
	if err != nil {
		return FileTransactionResult{}, err
	}
	root, err := transactionRoot(options.Root)
	if err != nil {
		return FileTransactionResult{}, err
	}
	prepared, result, err := preflightFileMutations(root, normalized)
	if err != nil {
		return FileTransactionResult{}, err
	}
	if !result.Changed() {
		return result, nil
	}

	createdDirectories, err := createTransactionDirectories(root, prepared)
	if err != nil {
		return FileTransactionResult{}, err
	}
	if err := verifyHardLinkDirectories(prepared, ops); err != nil {
		return FileTransactionResult{}, errors.Join(
			err,
			cleanupCreatedDirectories(createdDirectories),
		)
	}
	if err := stageFileMutations(prepared); err != nil {
		return FileTransactionResult{}, errors.Join(
			err,
			cleanupTransactionArtifacts(prepared, createdDirectories, ops),
		)
	}

	// Recheck the whole read set after staging and before the first target is
	// changed. Individual changed targets are checked once more immediately
	// before their backup or installation.
	if err := verifyAllPreconditions(prepared); err != nil {
		return FileTransactionResult{}, errors.Join(
			err,
			cleanupTransactionArtifacts(prepared, createdDirectories, ops),
		)
	}

	active := changedMutations(prepared)
	for index, mutation := range active {
		if err := verifyMutationPrecondition(mutation); err != nil {
			return FileTransactionResult{}, rollbackFileTransaction(
				err,
				active[:index],
				prepared,
				createdDirectories,
				ops,
			)
		}
		if mutation.existed {
			backup, err := reserveTransactionBackup(filepath.Dir(mutation.target))
			if err != nil {
				return FileTransactionResult{}, rollbackFileTransaction(
					fmt.Errorf("prepare backup for %q: %w", mutation.Path, err),
					active[:index],
					prepared,
					createdDirectories,
					ops,
				)
			}
			mutation.backup = backup
			if err := ops.rename(mutation.target, mutation.backup); err != nil {
				mutation.backup = ""
				return FileTransactionResult{}, rollbackFileTransaction(
					fmt.Errorf("backup %q: %w", mutation.Path, err),
					active[:index],
					prepared,
					createdDirectories,
					ops,
				)
			}
			if err := verifyBackup(mutation); err != nil {
				return FileTransactionResult{}, rollbackFileTransaction(
					err,
					active[:index+1],
					prepared,
					createdDirectories,
					ops,
				)
			}
		}
		if mutation.Remove {
			mutation.installed = true
			continue
		}
		// A hard link installs the same-directory staged inode only when target
		// is still absent. Unlike Rename on POSIX, it cannot overwrite a file
		// which appeared after the final precondition check.
		if err := ops.link(mutation.stage, mutation.target); err != nil {
			installErr := fmt.Errorf("install %q: %w", mutation.Path, err)
			if _, _, exists, inspectErr := inspectTransactionTarget(mutation.target); inspectErr != nil {
				installErr = errors.Join(installErr, inspectErr)
			} else if exists {
				installErr = fmt.Errorf(
					"%w: %q appeared before installation: %v",
					ErrTransactionConflict,
					mutation.Path,
					err,
				)
			}
			return FileTransactionResult{}, rollbackFileTransaction(
				installErr,
				active[:index+1],
				prepared,
				createdDirectories,
				ops,
			)
		}
		mutation.installed = true
		if err := ops.remove(mutation.stage); err != nil {
			return FileTransactionResult{}, rollbackFileTransaction(
				fmt.Errorf("remove staged link for %q: %w", mutation.Path, err),
				active[:index+1],
				prepared,
				createdDirectories,
				ops,
			)
		}
		mutation.stage = ""
		if err := verifyInstalledOutput(mutation, false); err != nil {
			return FileTransactionResult{}, rollbackFileTransaction(
				err,
				active[:index+1],
				prepared,
				createdDirectories,
				ops,
			)
		}
		installedHandle, err := openInstalledOutputHandle(mutation)
		if err != nil {
			return FileTransactionResult{}, rollbackFileTransaction(
				err,
				active[:index+1],
				prepared,
				createdDirectories,
				ops,
			)
		}
		modeErr := ops.chmodHandle(installedHandle, mutation.target, mutation.outputMode.Perm())
		if modeErr == nil {
			mutation.modeApplied = true
		}
		closeErr := installedHandle.Close()
		if modeErr != nil || closeErr != nil {
			installModeErr := closeErr
			if modeErr != nil {
				installModeErr = errors.Join(
					fmt.Errorf("set installed mode for %q: %w", mutation.Path, modeErr),
					closeErr,
				)
			} else {
				installModeErr = fmt.Errorf("close installed staged file for %q: %w", mutation.Path, closeErr)
			}
			return FileTransactionResult{}, rollbackFileTransaction(
				installModeErr,
				active[:index+1],
				prepared,
				createdDirectories,
				ops,
			)
		}
	}

	if err := verifyInstalledMutations(active); err != nil {
		return FileTransactionResult{}, rollbackFileTransaction(
			err,
			active,
			prepared,
			createdDirectories,
			ops,
		)
	}
	if err := verifyUnchangedPreconditions(prepared); err != nil {
		return FileTransactionResult{}, rollbackFileTransaction(
			err,
			active,
			prepared,
			createdDirectories,
			ops,
		)
	}

	for _, mutation := range active {
		if mutation.backup == "" {
			continue
		}
		if err := verifyBackupContent(mutation); err != nil {
			result.CleanupWarnings = append(
				result.CleanupWarnings,
				fmt.Sprintf(
					"transaction committed but retained changed backup %q for %q: %v",
					mutation.backup,
					mutation.Path,
					err,
				),
			)
			continue
		}
		removed, err := removeTransactionFile(mutation.backup, "", 0, ops)
		if removed {
			mutation.backup = ""
		}
		if err != nil {
			result.CleanupWarnings = append(
				result.CleanupWarnings,
				fmt.Sprintf(
					"transaction committed but could not remove backup %q for %q: %v",
					mutation.backup,
					mutation.Path,
					err,
				),
			)
			continue
		}
	}
	return result, nil
}

func normalizeFileMutations(mutations []FileMutation) ([]FileMutation, error) {
	normalized := make([]FileMutation, len(mutations))
	seen := make(map[string]string, len(mutations))
	for index, mutation := range mutations {
		mutation.Content = append([]byte(nil), mutation.Content...)
		portable, err := SafeRelativePath(mutation.Path)
		if err != nil {
			return nil, fmt.Errorf("transaction file %q: %w", mutation.Path, err)
		}
		if mutation.Mode&^fs.ModePerm != 0 {
			return nil, fmt.Errorf("transaction file %q: invalid permission mode %v", portable, mutation.Mode)
		}
		if mutation.Remove && (len(mutation.Content) != 0 || mutation.Mode != 0) {
			return nil, fmt.Errorf("transaction file %q: removal cannot include content or mode", portable)
		}
		if mutation.ObserveOnly && (mutation.Remove || len(mutation.Content) != 0 || mutation.Mode != 0) {
			return nil, fmt.Errorf("transaction file %q: observation cannot include content, mode, or removal", portable)
		}
		if err := validateFilePrecondition(mutation.Precondition); err != nil {
			return nil, fmt.Errorf("transaction file %q: %w", portable, err)
		}
		if mutation.Remove && !mutation.Precondition.Exists {
			return nil, fmt.Errorf("transaction file %q: removal requires an existing-file precondition", portable)
		}
		key, err := PortablePathKey(portable)
		if err != nil {
			return nil, fmt.Errorf("transaction file %q: %w", portable, err)
		}
		if previous, duplicate := seen[key]; duplicate {
			return nil, fmt.Errorf("%w: %q aliases %q", ErrDuplicatePath, portable, previous)
		}
		seen[key] = portable
		mutation.Path = portable
		normalized[index] = mutation
	}
	for _, name := range seen {
		for parent := path.Dir(name); parent != "."; parent = path.Dir(parent) {
			parentKey, err := PortablePathKey(parent)
			if err != nil {
				return nil, err
			}
			if parentName, isFile := seen[parentKey]; isFile {
				return nil, fmt.Errorf("%w: %q is both a file and parent of %q", ErrDuplicatePath, parentName, name)
			}
		}
	}
	sort.Slice(normalized, func(i, j int) bool {
		return normalized[i].Path < normalized[j].Path
	})
	return normalized, nil
}

func validateFilePrecondition(precondition FilePrecondition) error {
	if !precondition.Exists {
		if precondition.SHA256 != "" {
			return errors.New("missing-file precondition cannot include a content hash")
		}
		return nil
	}
	if !isSHA256(precondition.SHA256) {
		return errors.New("existing-file precondition requires a SHA-256 content hash")
	}
	return nil
}

func transactionRoot(configured string) (string, error) {
	root, err := absoluteRoot(configured)
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(root)
	if err != nil {
		return "", fmt.Errorf("inspect file transaction root: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", fmt.Errorf("file transaction root %q is not a directory", configured)
	}
	return root, nil
}

func preflightFileMutations(
	root string,
	mutations []FileMutation,
) ([]*preparedMutation, FileTransactionResult, error) {
	prepared := make([]*preparedMutation, 0, len(mutations))
	result := FileTransactionResult{}
	for _, mutation := range mutations {
		target, err := joinUnderRoot(root, mutation.Path)
		if err != nil {
			return nil, FileTransactionResult{}, err
		}
		if err := ensureParentSafe(root, target); err != nil {
			return nil, FileTransactionResult{}, err
		}
		current, mode, exists, err := inspectTransactionTarget(target)
		if err != nil {
			return nil, FileTransactionResult{}, err
		}
		item := &preparedMutation{
			FileMutation: mutation,
			target:       target,
			existed:      exists,
			current:      current,
			currentMode:  mode,
			outputHash:   hashBytes(mutation.Content),
		}
		item.outputMode = mutation.Mode.Perm()
		if item.outputMode == 0 {
			item.outputMode = 0o644
			if exists {
				item.outputMode = mode.Perm()
			}
		}
		if err := verifyPreparedState(item, current, exists); err != nil {
			return nil, FileTransactionResult{}, err
		}
		switch {
		case mutation.ObserveOnly:
			result.Unchanged = append(result.Unchanged, mutation.Path)
		case mutation.Remove:
			result.Removed = append(result.Removed, mutation.Path)
		case !exists:
			result.Created = append(result.Created, mutation.Path)
		case transactionOutputUnchanged(item):
			result.Unchanged = append(result.Unchanged, mutation.Path)
		default:
			result.Updated = append(result.Updated, mutation.Path)
		}
		prepared = append(prepared, item)
	}
	return prepared, result, nil
}

func inspectTransactionTarget(filename string) ([]byte, fs.FileMode, bool, error) {
	info, err := os.Lstat(filename)
	if os.IsNotExist(err) {
		return nil, 0, false, nil
	}
	if err != nil {
		return nil, 0, false, fmt.Errorf("inspect transaction target %q: %w", filename, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, 0, false, fmt.Errorf("transaction target %q is not a regular file", filename)
	}
	content, err := os.ReadFile(filename)
	if err != nil {
		return nil, 0, false, fmt.Errorf("read transaction target %q: %w", filename, err)
	}
	return content, info.Mode(), true, nil
}

func verifyPreparedState(item *preparedMutation, current []byte, exists bool) error {
	expected := item.Precondition
	if expected.Exists != exists {
		state := "exist"
		if expected.Exists {
			state = "be present"
		} else {
			state = "be absent"
		}
		return fmt.Errorf("%w: %q must %s", ErrTransactionConflict, item.Path, state)
	}
	if exists && hashBytes(current) != expected.SHA256 {
		return fmt.Errorf("%w: %q content changed", ErrTransactionConflict, item.Path)
	}
	return nil
}

func transactionOutputUnchanged(item *preparedMutation) bool {
	if item.ObserveOnly {
		return true
	}
	if item.Remove {
		return !item.existed
	}
	if !item.existed || hashBytes(item.current) != item.outputHash {
		return false
	}
	return item.Mode == 0 || permissionModesEquivalent(item.currentMode, item.Mode)
}

func createTransactionDirectories(root string, prepared []*preparedMutation) ([]string, error) {
	directorySet := make(map[string]struct{})
	for _, mutation := range changedMutations(prepared) {
		for directory := filepath.Dir(mutation.target); directory != root; directory = filepath.Dir(directory) {
			directorySet[directory] = struct{}{}
		}
	}
	directories := make([]string, 0, len(directorySet))
	for directory := range directorySet {
		directories = append(directories, directory)
	}
	sort.Slice(directories, func(i, j int) bool {
		if len(directories[i]) != len(directories[j]) {
			return len(directories[i]) < len(directories[j])
		}
		return directories[i] < directories[j]
	})

	created := make([]string, 0, len(directories))
	for _, directory := range directories {
		info, err := os.Lstat(directory)
		if err == nil {
			if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
				return created, errors.Join(
					fmt.Errorf("transaction output directory %q is not a directory", directory),
					cleanupCreatedDirectories(created),
				)
			}
			continue
		}
		if !os.IsNotExist(err) {
			return created, errors.Join(
				fmt.Errorf("inspect transaction output directory %q: %w", directory, err),
				cleanupCreatedDirectories(created),
			)
		}
		if err := os.Mkdir(directory, 0o755); err != nil {
			return created, errors.Join(
				fmt.Errorf("create transaction output directory %q: %w", directory, err),
				cleanupCreatedDirectories(created),
			)
		}
		created = append(created, directory)
	}
	return created, nil
}

func verifyHardLinkDirectories(prepared []*preparedMutation, ops fileTransactionOps) error {
	directorySet := make(map[string]struct{})
	for _, mutation := range changedMutations(prepared) {
		directorySet[filepath.Dir(mutation.target)] = struct{}{}
	}
	directories := make([]string, 0, len(directorySet))
	for directory := range directorySet {
		directories = append(directories, directory)
	}
	sort.Strings(directories)
	for _, directory := range directories {
		if err := verifyHardLinkDirectory(directory, ops); err != nil {
			return fmt.Errorf("verify hard-link support in transaction directory %q: %w", directory, err)
		}
	}
	return nil
}

func verifyHardLinkDirectory(directory string, ops fileTransactionOps) (err error) {
	source, err := os.CreateTemp(directory, ".kpro-transaction-link-probe-*")
	if err != nil {
		return err
	}
	sourceName := source.Name()
	sourcePresent := true
	linkName := sourceName + ".link"
	linkPresent := false
	defer func() {
		var cleanupErr error
		if linkPresent {
			cleanupErr = errors.Join(cleanupErr, ops.remove(linkName))
		}
		if sourcePresent {
			cleanupErr = errors.Join(cleanupErr, ops.remove(sourceName))
		}
		err = errors.Join(err, cleanupErr)
	}()
	if err := source.Close(); err != nil {
		return err
	}
	if _, err := os.Lstat(linkName); err == nil {
		return fmt.Errorf("hard-link probe target %q already exists", linkName)
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := ops.link(sourceName, linkName); err != nil {
		return err
	}
	linkPresent = true
	if err := ops.remove(linkName); err != nil {
		return err
	}
	linkPresent = false
	if err := ops.remove(sourceName); err != nil {
		return err
	}
	sourcePresent = false
	return nil
}

func stageFileMutations(prepared []*preparedMutation) error {
	for _, mutation := range changedMutations(prepared) {
		if mutation.Remove {
			continue
		}
		temporary, err := os.CreateTemp(filepath.Dir(mutation.target), ".kpro-transaction-stage-*")
		if err != nil {
			return fmt.Errorf("stage %q: %w", mutation.Path, err)
		}
		mutation.stage = temporary.Name()
		// CreateTemp's requested 0600 is still filtered by the process umask on
		// Unix. The transaction must be able to reopen this private inode O_RDWR
		// after linking it into place, regardless of the caller's umask.
		if err := temporary.Chmod(0o600); err != nil {
			temporary.Close()
			return fmt.Errorf("set private staged mode for %q: %w", mutation.Path, err)
		}
		if _, err := temporary.Write(mutation.Content); err != nil {
			temporary.Close()
			return fmt.Errorf("write staged file for %q: %w", mutation.Path, err)
		}
		if err := temporary.Sync(); err != nil {
			temporary.Close()
			return fmt.Errorf("sync staged file for %q: %w", mutation.Path, err)
		}
		info, err := temporary.Stat()
		if err != nil {
			temporary.Close()
			return fmt.Errorf("inspect staged file for %q: %w", mutation.Path, err)
		}
		mutation.stageInfo = info
		if err := temporary.Close(); err != nil {
			return fmt.Errorf("close staged file for %q: %w", mutation.Path, err)
		}
	}
	return nil
}

func openInstalledOutputHandle(mutation *preparedMutation) (*os.File, error) {
	installed, err := os.OpenFile(mutation.target, os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("open installed file for %q: %w", mutation.Path, err)
	}
	info, err := installed.Stat()
	if err != nil {
		installed.Close()
		return nil, fmt.Errorf("inspect installed file for %q: %w", mutation.Path, err)
	}
	if mutation.stageInfo == nil || !os.SameFile(mutation.stageInfo, info) {
		installed.Close()
		return nil, fmt.Errorf("%w: installed file for %q changed", ErrTransactionConflict, mutation.Path)
	}
	return installed, nil
}

func changedMutations(prepared []*preparedMutation) []*preparedMutation {
	changed := make([]*preparedMutation, 0, len(prepared))
	for _, mutation := range prepared {
		if !transactionOutputUnchanged(mutation) {
			changed = append(changed, mutation)
		}
	}
	return changed
}

func verifyAllPreconditions(prepared []*preparedMutation) error {
	for _, mutation := range prepared {
		if err := verifyMutationPrecondition(mutation); err != nil {
			return err
		}
	}
	return nil
}

func verifyMutationPrecondition(mutation *preparedMutation) error {
	current, mode, exists, err := inspectTransactionTarget(mutation.target)
	if err != nil {
		return err
	}
	if err := verifyPreparedState(mutation, current, exists); err != nil {
		return err
	}
	if exists && !permissionModesEquivalent(mode, mutation.currentMode) {
		return fmt.Errorf("%w: %q permission mode changed", ErrTransactionConflict, mutation.Path)
	}
	return nil
}

func reserveTransactionBackup(directory string) (string, error) {
	placeholder, err := os.CreateTemp(directory, ".kpro-transaction-backup-*")
	if err != nil {
		return "", err
	}
	name := placeholder.Name()
	if err := placeholder.Close(); err != nil {
		_ = os.Remove(name)
		return "", err
	}
	if err := os.Remove(name); err != nil {
		return "", err
	}
	return name, nil
}

func verifyBackup(mutation *preparedMutation) error {
	content, mode, info, exists, err := inspectTransactionBackup(mutation.backup)
	if err != nil {
		return fmt.Errorf("verify backup %q for %q: %w", mutation.backup, mutation.Path, err)
	}
	if exists {
		mutation.backupHash = hashBytes(content)
		mutation.backupMode = mode.Perm()
		mutation.backupInfo = info
		mutation.backupKnown = true
	}
	if !exists || mutation.backupHash != mutation.Precondition.SHA256 ||
		!permissionModesEquivalent(mode, mutation.currentMode) {
		return fmt.Errorf(
			"%w: backup %q for %q does not match the expected state",
			ErrTransactionConflict,
			mutation.backup,
			mutation.Path,
		)
	}
	return nil
}

func verifyBackupContent(mutation *preparedMutation) error {
	content, mode, info, exists, err := inspectTransactionBackup(mutation.backup)
	if err != nil {
		return fmt.Errorf("verify backup %q for %q: %w", mutation.backup, mutation.Path, err)
	}
	if !exists {
		return fmt.Errorf(
			"%w: backup %q for %q is missing",
			ErrTransactionConflict,
			mutation.backup,
			mutation.Path,
		)
	}
	actualHash := hashBytes(content)
	if !mutation.backupKnown {
		mutation.backupHash = actualHash
		mutation.backupMode = mode.Perm()
		mutation.backupInfo = info
		mutation.backupKnown = true
	}
	if mutation.backupInfo == nil || !os.SameFile(mutation.backupInfo, info) ||
		actualHash != mutation.backupHash ||
		!permissionModesEquivalent(mode, mutation.backupMode) {
		return fmt.Errorf(
			"%w: backup %q for %q changed after it was moved or verified",
			ErrTransactionConflict,
			mutation.backup,
			mutation.Path,
		)
	}
	return nil
}

func verifyRestoredTarget(mutation *preparedMutation) error {
	if err := verifyBackupContent(mutation); err != nil {
		return err
	}
	content, mode, exists, err := inspectTransactionTarget(mutation.target)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("%w: restored target %q is missing", ErrTransactionConflict, mutation.Path)
	}
	info, err := os.Lstat(mutation.target)
	if err != nil {
		return err
	}
	if mutation.backupInfo == nil || !os.SameFile(mutation.backupInfo, info) ||
		hashBytes(content) != mutation.backupHash ||
		!permissionModesEquivalent(mode, mutation.backupMode) {
		return fmt.Errorf("%w: restored target %q changed before backup cleanup", ErrTransactionConflict, mutation.Path)
	}
	return nil
}

func inspectTransactionBackup(filename string) ([]byte, fs.FileMode, fs.FileInfo, bool, error) {
	before, err := os.Lstat(filename)
	if os.IsNotExist(err) {
		return nil, 0, nil, false, nil
	}
	if err != nil {
		return nil, 0, nil, false, err
	}
	content, mode, exists, err := inspectTransactionTarget(filename)
	if err != nil || !exists {
		return nil, 0, nil, exists, err
	}
	after, err := os.Lstat(filename)
	if err != nil {
		return nil, 0, nil, false, err
	}
	if !os.SameFile(before, after) {
		return nil, 0, nil, false, fmt.Errorf(
			"%w: transaction backup %q changed while it was inspected",
			ErrTransactionConflict,
			filename,
		)
	}
	return content, mode, after, true, nil
}

func verifyInstalledMutations(mutations []*preparedMutation) error {
	for _, mutation := range mutations {
		if mutation.Remove {
			_, _, exists, err := inspectTransactionTarget(mutation.target)
			if err != nil {
				return err
			}
			if exists {
				return fmt.Errorf("%w: removed target %q reappeared", ErrTransactionConflict, mutation.Path)
			}
			continue
		}
		if err := verifyInstalledOutput(mutation, true); err != nil {
			return err
		}
	}
	return nil
}

func verifyUnchangedPreconditions(prepared []*preparedMutation) error {
	for _, mutation := range prepared {
		if !transactionOutputUnchanged(mutation) {
			continue
		}
		if err := verifyMutationPrecondition(mutation); err != nil {
			return err
		}
	}
	return nil
}

func verifyInstalledOutput(mutation *preparedMutation, requireMode bool) error {
	owned, exists, err := installedOutputOwned(mutation, requireMode)
	if err != nil {
		return err
	}
	if !exists || !owned {
		return fmt.Errorf("%w: installed target %q changed", ErrTransactionConflict, mutation.Path)
	}
	return nil
}

func installedOutputOwned(mutation *preparedMutation, requireMode bool) (bool, bool, error) {
	content, mode, exists, err := inspectTransactionTarget(mutation.target)
	if err != nil || !exists {
		return false, exists, err
	}
	info, err := os.Lstat(mutation.target)
	if err != nil {
		return false, false, err
	}
	owned := mutation.stageInfo != nil && os.SameFile(mutation.stageInfo, info) &&
		hashBytes(content) == mutation.outputHash
	if requireMode {
		owned = owned && permissionModesEquivalent(mode, mutation.outputMode)
	}
	return owned, true, nil
}

func permissionModesEquivalent(left, right fs.FileMode) bool {
	if runtime.GOOS == "windows" {
		// Chmod on Windows only represents the owner-write bit. Group, other,
		// and execute permission bits cannot converge through the Go API.
		return left.Perm()&0o200 == right.Perm()&0o200
	}
	return left.Perm() == right.Perm()
}

func removeTransactionFile(
	filename string,
	restoredTarget string,
	restoredMode fs.FileMode,
	ops fileTransactionOps,
) (bool, error) {
	if runtime.GOOS != "windows" {
		if restoredTarget != "" {
			if err := ops.chmod(restoredTarget, restoredMode.Perm()); err != nil {
				return false, fmt.Errorf("restore mode on %q: %w", restoredTarget, err)
			}
		}
		err := ops.remove(filename)
		return err == nil, err
	}

	info, err := os.Lstat(filename)
	if err != nil {
		return false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return false, fmt.Errorf("transaction file %q is not a regular file", filename)
	}
	originalMode := info.Mode().Perm()
	temporarilyWritable := originalMode&0o200 == 0
	if temporarilyWritable {
		if err := ops.chmod(filename, originalMode|0o200); err != nil {
			return false, fmt.Errorf("temporarily make %q writable: %w", filename, err)
		}
	}
	if err := ops.remove(filename); err != nil {
		if temporarilyWritable {
			err = errors.Join(err, ops.chmod(filename, originalMode))
		}
		return false, err
	}
	if restoredTarget != "" {
		if err := ops.chmod(restoredTarget, restoredMode.Perm()); err != nil {
			return true, fmt.Errorf("restore mode on %q: %w", restoredTarget, err)
		}
	}
	return true, nil
}

func rollbackFileTransaction(
	cause error,
	touched []*preparedMutation,
	all []*preparedMutation,
	createdDirectories []string,
	ops fileTransactionOps,
) error {
	var rollbackErr error
	for index := len(touched) - 1; index >= 0; index-- {
		mutation := touched[index]
		if mutation.installed {
			if mutation.Remove {
				_, _, exists, err := inspectTransactionTarget(mutation.target)
				if err != nil {
					rollbackErr = errors.Join(
						rollbackErr,
						fmt.Errorf(
							"inspect installed %q during rollback: %w; %s",
							mutation.Path,
							err,
							rollbackRecoveryDescription(mutation),
						),
					)
					continue
				}
				if exists {
					rollbackErr = errors.Join(
						rollbackErr,
						fmt.Errorf(
							"cannot rollback removal of %q because the target reappeared; %s",
							mutation.Path,
							rollbackRecoveryDescription(mutation),
						),
					)
					continue
				}
				mutation.installed = false
			}

			if !mutation.Remove {
				owned, exists, err := installedOutputOwned(mutation, mutation.modeApplied)
				if err != nil {
					rollbackErr = errors.Join(
						rollbackErr,
						fmt.Errorf(
							"inspect installed %q during rollback: %w; %s",
							mutation.Path,
							err,
							rollbackRecoveryDescription(mutation),
						),
					)
					continue
				}
				if !exists || !owned {
					rollbackErr = errors.Join(
						rollbackErr,
						fmt.Errorf(
							"cannot rollback %q because its installed content or identity changed; %s",
							mutation.Path,
							rollbackRecoveryDescription(mutation),
						),
					)
					continue
				}
				if _, err := removeTransactionFile(mutation.target, "", 0, ops); err != nil {
					rollbackErr = errors.Join(
						rollbackErr,
						fmt.Errorf(
							"remove installed %q during rollback: %w; %s",
							mutation.Path,
							err,
							rollbackRecoveryDescription(mutation),
						),
					)
					continue
				}
				mutation.installed = false
			}
		}
		if mutation.backup != "" {
			// Restore the exact inode state that this transaction moved aside,
			// including a concurrent update which won the race immediately before
			// the backup rename.
			if err := verifyBackupContent(mutation); err != nil {
				rollbackErr = errors.Join(
					rollbackErr,
					fmt.Errorf("cannot restore %q: %w; backup retained at %s", mutation.Path, err, mutation.backup),
				)
				continue
			}
			if _, _, exists, err := inspectTransactionTarget(mutation.target); err != nil || exists {
				if err == nil {
					err = errors.New("target exists")
				}
				rollbackErr = errors.Join(
					rollbackErr,
					fmt.Errorf("cannot restore %q because its target is unavailable: %w; backup retained at %s", mutation.Path, err, mutation.backup),
				)
				continue
			}
			if err := ops.link(mutation.backup, mutation.target); err != nil {
				rollbackErr = errors.Join(
					rollbackErr,
					fmt.Errorf(
						"restore %q from backup %s: %w; backup retained at %s",
						mutation.Path,
						mutation.backup,
						err,
						mutation.backup,
					),
				)
				continue
			}
			if err := verifyRestoredTarget(mutation); err != nil {
				rollbackErr = errors.Join(
					rollbackErr,
					fmt.Errorf(
						"cannot finalize restoration of %q: %w; backup retained at %s",
						mutation.Path,
						err,
						mutation.backup,
					),
				)
				continue
			}
			removed, err := removeTransactionFile(
				mutation.backup,
				mutation.target,
				mutation.backupMode,
				ops,
			)
			if removed {
				mutation.backup = ""
			}
			if err != nil {
				if removed {
					rollbackErr = errors.Join(
						rollbackErr,
						fmt.Errorf("restored %q but could not restore its mode: %w", mutation.Path, err),
					)
					continue
				}
				rollbackErr = errors.Join(
					rollbackErr,
					fmt.Errorf(
						"restored %q but could not remove retained backup %s: %w",
						mutation.Path,
						mutation.backup,
						err,
					),
				)
				continue
			}
		}
	}
	cleanupErr := cleanupTransactionArtifacts(all, createdDirectories, ops)
	return errors.Join(cause, rollbackErr, cleanupErr)
}

func rollbackRecoveryDescription(mutation *preparedMutation) string {
	if mutation.backup != "" {
		return fmt.Sprintf("backup retained at %q", mutation.backup)
	}
	return fmt.Sprintf("current target state preserved at %q; no backup exists for this create", mutation.target)
}

func cleanupTransactionArtifacts(
	prepared []*preparedMutation,
	createdDirectories []string,
	ops fileTransactionOps,
) error {
	var cleanupErr error
	for _, mutation := range prepared {
		if mutation.stage != "" {
			if err := ops.remove(mutation.stage); err != nil && !os.IsNotExist(err) {
				cleanupErr = errors.Join(
					cleanupErr,
					fmt.Errorf("remove staged file %q: %w", mutation.stage, err),
				)
			}
			mutation.stage = ""
		}
	}
	cleanupErr = errors.Join(cleanupErr, cleanupCreatedDirectories(createdDirectories))
	return cleanupErr
}

func cleanupCreatedDirectories(created []string) error {
	var cleanupErr error
	for index := len(created) - 1; index >= 0; index-- {
		if err := os.Remove(created[index]); err != nil && !os.IsNotExist(err) {
			cleanupErr = errors.Join(
				cleanupErr,
				fmt.Errorf("remove transaction directory %q: %w", created[index], err),
			)
		}
	}
	return cleanupErr
}
