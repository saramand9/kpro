package codegen

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
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

// FileMutation creates or replaces one regular file. Path is relative to the
// transaction root. A zero Mode uses 0644 for a new file and preserves the
// existing permission bits for a replacement.
type FileMutation struct {
	Path         string
	Content      []byte
	Mode         fs.FileMode
	Precondition FilePrecondition
}

// FileTransactionOptions configures CommitFileTransaction.
type FileTransactionOptions struct {
	Root string
}

// FileTransactionResult describes a successfully committed transaction.
type FileTransactionResult struct {
	Created         []string
	Updated         []string
	Unchanged       []string
	CleanupWarnings []string
}

// Changed reports whether the transaction changed at least one target.
func (r FileTransactionResult) Changed() bool {
	return len(r.Created) != 0 || len(r.Updated) != 0
}

type preparedMutation struct {
	FileMutation
	target      string
	existed     bool
	current     []byte
	currentMode fs.FileMode
	outputHash  string
	stage       string
	backup      string
	installed   bool
}

type fileTransactionOps struct {
	rename func(string, string) error
	remove func(string) error
}

var defaultFileTransactionOps = fileTransactionOps{
	rename: os.Rename,
	remove: os.Remove,
}

// CommitFileTransaction applies a compare-and-swap batch of file creations and
// replacements. It validates every target before making filesystem changes,
// stages each output in its target directory, and keeps same-directory backups
// until every replacement is installed. A commit error triggers an inverse,
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
	if ops.rename == nil || ops.remove == nil {
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
				_ = os.Remove(backup)
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
		if err := ops.rename(mutation.stage, mutation.target); err != nil {
			return FileTransactionResult{}, rollbackFileTransaction(
				fmt.Errorf("install %q: %w", mutation.Path, err),
				active[:index+1],
				prepared,
				createdDirectories,
				ops,
			)
		}
		mutation.installed = true
		mutation.stage = ""
	}

	for _, mutation := range active {
		if mutation.backup == "" {
			continue
		}
		if err := ops.remove(mutation.backup); err != nil {
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
		mutation.backup = ""
	}
	return result, nil
}

func normalizeFileMutations(mutations []FileMutation) ([]FileMutation, error) {
	normalized := make([]FileMutation, len(mutations))
	seen := make(map[string]struct{}, len(mutations))
	for index, mutation := range mutations {
		mutation.Content = append([]byte(nil), mutation.Content...)
		portable, err := SafeRelativePath(mutation.Path)
		if err != nil {
			return nil, fmt.Errorf("transaction file %q: %w", mutation.Path, err)
		}
		if mutation.Mode&^fs.ModePerm != 0 {
			return nil, fmt.Errorf("transaction file %q: invalid permission mode %v", portable, mutation.Mode)
		}
		if err := validateFilePrecondition(mutation.Precondition); err != nil {
			return nil, fmt.Errorf("transaction file %q: %w", portable, err)
		}
		if _, duplicate := seen[portable]; duplicate {
			return nil, fmt.Errorf("%w: %q", ErrDuplicatePath, portable)
		}
		seen[portable] = struct{}{}
		mutation.Path = portable
		normalized[index] = mutation
	}
	for name := range seen {
		for parent := path.Dir(name); parent != "."; parent = path.Dir(parent) {
			if _, isFile := seen[parent]; isFile {
				return nil, fmt.Errorf("%w: %q is both a file and parent of %q", ErrDuplicatePath, parent, name)
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
		if err := verifyPreparedState(item, current, exists); err != nil {
			return nil, FileTransactionResult{}, err
		}
		switch {
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
	if !item.existed || hashBytes(item.current) != item.outputHash {
		return false
	}
	return item.Mode == 0 || item.currentMode.Perm() == item.Mode.Perm()
}

func createTransactionDirectories(root string, prepared []*preparedMutation) ([]string, error) {
	directorySet := make(map[string]struct{})
	for _, mutation := range prepared {
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

func stageFileMutations(prepared []*preparedMutation) error {
	for _, mutation := range changedMutations(prepared) {
		mode := mutation.Mode
		if mode == 0 {
			mode = 0o644
			if mutation.existed {
				mode = mutation.currentMode.Perm()
			}
		}
		temporary, err := os.CreateTemp(filepath.Dir(mutation.target), ".kpro-transaction-stage-*")
		if err != nil {
			return fmt.Errorf("stage %q: %w", mutation.Path, err)
		}
		mutation.stage = temporary.Name()
		if err := temporary.Chmod(mode.Perm()); err != nil {
			temporary.Close()
			return fmt.Errorf("set staged mode for %q: %w", mutation.Path, err)
		}
		if _, err := temporary.Write(mutation.Content); err != nil {
			temporary.Close()
			return fmt.Errorf("write staged file for %q: %w", mutation.Path, err)
		}
		if err := temporary.Sync(); err != nil {
			temporary.Close()
			return fmt.Errorf("sync staged file for %q: %w", mutation.Path, err)
		}
		if err := temporary.Close(); err != nil {
			return fmt.Errorf("close staged file for %q: %w", mutation.Path, err)
		}
	}
	return nil
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
	current, _, exists, err := inspectTransactionTarget(mutation.target)
	if err != nil {
		return err
	}
	return verifyPreparedState(mutation, current, exists)
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
	content, _, exists, err := inspectTransactionTarget(mutation.backup)
	if err != nil {
		return fmt.Errorf("verify backup %q for %q: %w", mutation.backup, mutation.Path, err)
	}
	if !exists || hashBytes(content) != mutation.Precondition.SHA256 {
		return fmt.Errorf(
			"%w: backup %q for %q does not match the expected content",
			ErrTransactionConflict,
			mutation.backup,
			mutation.Path,
		)
	}
	return nil
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
			current, _, exists, err := inspectTransactionTarget(mutation.target)
			if err != nil {
				rollbackErr = errors.Join(
					rollbackErr,
					fmt.Errorf("inspect installed %q during rollback: %w", mutation.Path, err),
				)
				continue
			}
			if !exists || hashBytes(current) != mutation.outputHash {
				rollbackErr = errors.Join(
					rollbackErr,
					fmt.Errorf(
						"cannot rollback %q because its installed content changed; backup retained at %q",
						mutation.Path,
						mutation.backup,
					),
				)
				continue
			}
			if err := ops.remove(mutation.target); err != nil {
				rollbackErr = errors.Join(
					rollbackErr,
					fmt.Errorf(
						"remove installed %q during rollback: %w; backup retained at %q",
						mutation.Path,
						err,
						mutation.backup,
					),
				)
				continue
			}
			mutation.installed = false
		}
		if mutation.backup != "" {
			if err := ops.rename(mutation.backup, mutation.target); err != nil {
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
			mutation.backup = ""
		}
	}
	cleanupErr := cleanupTransactionArtifacts(all, createdDirectories, ops)
	return errors.Join(cause, rollbackErr, cleanupErr)
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
