package codegen

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCommitFileTransactionCreatesUpdatesAndPreservesModes(t *testing.T) {
	root := t.TempDir()
	existingPath := filepath.Join(root, "config", "service.yaml")
	if err := os.MkdirAll(filepath.Dir(existingPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(existingPath, []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	unchangedPath := filepath.Join(root, "README.md")
	if err := os.WriteFile(unchangedPath, []byte("keep\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := CommitFileTransaction(
		[]FileMutation{
			{
				Path:         "internal/grpcapi/service.go",
				Content:      []byte("package grpcapi\n"),
				Precondition: ExpectFileMissing(),
			},
			{
				Path:         `config\service.yaml`,
				Content:      []byte("new\n"),
				Precondition: ExpectFileContent([]byte("old\n")),
			},
			{
				Path:         "README.md",
				Content:      []byte("keep\n"),
				Precondition: ExpectFileContent([]byte("keep\n")),
			},
		},
		FileTransactionOptions{Root: root},
	)
	if err != nil {
		t.Fatalf("CommitFileTransaction() error = %v", err)
	}
	if got := strings.Join(result.Created, ","); got != "internal/grpcapi/service.go" {
		t.Fatalf("Created = %q", got)
	}
	if got := strings.Join(result.Updated, ","); got != "config/service.yaml" {
		t.Fatalf("Updated = %q", got)
	}
	if got := strings.Join(result.Unchanged, ","); got != "README.md" {
		t.Fatalf("Unchanged = %q", got)
	}
	if !result.Changed() {
		t.Fatal("Changed() = false")
	}
	assertFileContent(t, existingPath, "new\n")
	assertFileContent(t, filepath.Join(root, "internal", "grpcapi", "service.go"), "package grpcapi\n")
	if runtime.GOOS != "windows" {
		info, statErr := os.Stat(existingPath)
		if statErr != nil {
			t.Fatal(statErr)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Fatalf("updated mode = %v, want 0600", got)
		}
	}
	assertNoTransactionArtifacts(t, root)
}

func TestCommitFileTransactionPreflightConflictWritesNothing(t *testing.T) {
	root := t.TempDir()
	existingPath := filepath.Join(root, "a.txt")
	if err := os.WriteFile(existingPath, []byte("current"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := CommitFileTransaction(
		[]FileMutation{
			{
				Path:         "new/deep/file.txt",
				Content:      []byte("new"),
				Precondition: ExpectFileMissing(),
			},
			{
				Path:         "a.txt",
				Content:      []byte("replacement"),
				Precondition: ExpectFileContent([]byte("stale")),
			},
		},
		FileTransactionOptions{Root: root},
	)
	if !errors.Is(err, ErrTransactionConflict) {
		t.Fatalf("error = %v, want ErrTransactionConflict", err)
	}
	assertFileContent(t, existingPath, "current")
	if _, statErr := os.Stat(filepath.Join(root, "new")); !os.IsNotExist(statErr) {
		t.Fatalf("preflight created a directory: %v", statErr)
	}
	assertNoTransactionArtifacts(t, root)
}

func TestCommitFileTransactionRollsBackMidCommitFailure(t *testing.T) {
	root := t.TempDir()
	existingPath := filepath.Join(root, "a.txt")
	if err := os.WriteFile(existingPath, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	ops := defaultFileTransactionOps
	failedTarget := filepath.Join(root, "new", "b.txt")
	ops.link = func(oldname, newname string) error {
		if newname == failedTarget {
			return fs.ErrPermission
		}
		return os.Link(oldname, newname)
	}

	_, err := commitFileTransaction(
		[]FileMutation{
			{
				Path:         "a.txt",
				Content:      []byte("updated"),
				Precondition: ExpectFileContent([]byte("old")),
			},
			{
				Path:         "new/b.txt",
				Content:      []byte("created"),
				Precondition: ExpectFileMissing(),
			},
		},
		FileTransactionOptions{Root: root},
		ops,
	)
	if err == nil || !strings.Contains(err.Error(), `install "new/b.txt"`) {
		t.Fatalf("error = %v, want injected install failure", err)
	}
	assertFileContent(t, existingPath, "old")
	if _, statErr := os.Stat(filepath.Join(root, "new", "b.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("new file remains after rollback: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(root, "new")); !os.IsNotExist(statErr) {
		t.Fatalf("new directory remains after rollback: %v", statErr)
	}
	assertNoTransactionArtifacts(t, root)
}

func TestCommitFileTransactionReportsBackupCleanupWarningAfterCommit(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "a.txt")
	if err := os.WriteFile(target, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	ops := defaultFileTransactionOps
	ops.remove = func(filename string) error {
		if strings.Contains(filepath.Base(filename), ".kpro-transaction-backup-") {
			return fs.ErrPermission
		}
		return os.Remove(filename)
	}

	result, err := commitFileTransaction(
		[]FileMutation{{
			Path:         "a.txt",
			Content:      []byte("updated"),
			Precondition: ExpectFileContent([]byte("old")),
		}},
		FileTransactionOptions{Root: root},
		ops,
	)
	if err != nil {
		t.Fatalf("commitFileTransaction() error = %v", err)
	}
	if !result.Changed() {
		t.Fatal("Changed() = false")
	}
	assertFileContent(t, target, "updated")

	backups, globErr := filepath.Glob(filepath.Join(root, ".kpro-transaction-backup-*"))
	if globErr != nil {
		t.Fatal(globErr)
	}
	if len(backups) != 1 {
		t.Fatalf("backup files = %v, want one", backups)
	}
	assertFileContent(t, backups[0], "old")
	wantWarning := fmt.Sprintf(
		"transaction committed but could not remove backup %q for %q: %v",
		backups[0],
		"a.txt",
		fs.ErrPermission,
	)
	if len(result.CleanupWarnings) != 1 || result.CleanupWarnings[0] != wantWarning {
		t.Fatalf("CleanupWarnings = %q, want [%q]", result.CleanupWarnings, wantWarning)
	}
}
func TestCommitFileTransactionRetainsBackupWhenRollbackCannotRestore(t *testing.T) {
	root := t.TempDir()
	existingPath := filepath.Join(root, "a.txt")
	if err := os.WriteFile(existingPath, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	ops := defaultFileTransactionOps
	failedInstall := filepath.Join(root, "b.txt")
	ops.link = func(oldname, newname string) error {
		if newname == failedInstall ||
			(strings.Contains(filepath.Base(oldname), ".kpro-transaction-backup-") && newname == existingPath) {
			return fs.ErrPermission
		}
		return os.Link(oldname, newname)
	}

	_, err := commitFileTransaction(
		[]FileMutation{
			{
				Path:         "a.txt",
				Content:      []byte("updated"),
				Precondition: ExpectFileContent([]byte("old")),
			},
			{
				Path:         "b.txt",
				Content:      []byte("created"),
				Precondition: ExpectFileMissing(),
			},
		},
		FileTransactionOptions{Root: root},
		ops,
	)
	if err == nil || !strings.Contains(err.Error(), "backup retained at") {
		t.Fatalf("error = %v, want retained-backup diagnostic", err)
	}
	if _, statErr := os.Stat(existingPath); !os.IsNotExist(statErr) {
		t.Fatalf("target should be absent after injected restore failure: %v", statErr)
	}
	backups, globErr := filepath.Glob(filepath.Join(root, ".kpro-transaction-backup-*"))
	if globErr != nil {
		t.Fatal(globErr)
	}
	if len(backups) != 1 {
		t.Fatalf("backup files = %v, want one", backups)
	}
	assertFileContent(t, backups[0], "old")
	if !strings.Contains(err.Error(), backups[0]) {
		t.Fatalf("error does not identify recovery file %q: %v", backups[0], err)
	}
}

func TestCommitFileTransactionRejectsUnsafeDuplicateAndSymlinkPaths(t *testing.T) {
	root := t.TempDir()
	tests := []struct {
		name      string
		mutations []FileMutation
		want      error
	}{
		{
			name: "unsafe",
			mutations: []FileMutation{{
				Path: "../escape",
			}},
			want: ErrUnsafePath,
		},
		{
			name: "duplicate",
			mutations: []FileMutation{
				{Path: "same"},
				{Path: `same`},
			},
			want: ErrDuplicatePath,
		},
		{
			name: "portable alias",
			mutations: []FileMutation{
				{Path: "Same.go"},
				{Path: "same.go"},
			},
			want: ErrDuplicatePath,
		},
		{
			name: "file and parent",
			mutations: []FileMutation{
				{Path: "a"},
				{Path: "a/b"},
			},
			want: ErrDuplicatePath,
		},
		{
			name: "portable file and parent",
			mutations: []FileMutation{
				{Path: "Parent"},
				{Path: "parent/child"},
			},
			want: ErrDuplicatePath,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := CommitFileTransaction(test.mutations, FileTransactionOptions{Root: root})
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}

	link := filepath.Join(root, "linked")
	if err := os.Symlink(t.TempDir(), link); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	_, err := CommitFileTransaction(
		[]FileMutation{{Path: "linked/file.txt", Content: []byte("unsafe")}},
		FileTransactionOptions{Root: root},
	)
	if err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("symlink parent error = %v", err)
	}
}

func TestCommitFileTransactionRequiresExistingDirectoryRoot(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	_, err := CommitFileTransaction(
		[]FileMutation{{Path: "file.txt"}},
		FileTransactionOptions{Root: missing},
	)
	if err == nil || !strings.Contains(err.Error(), "file transaction root") {
		t.Fatalf("missing root error = %v", err)
	}
}

func TestCommitFileTransactionRemovesFile(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "obsolete.txt")
	if err := os.WriteFile(target, []byte("obsolete"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := CommitFileTransaction(
		[]FileMutation{{
			Path:         "obsolete.txt",
			Precondition: ExpectFileContent([]byte("obsolete")),
			Remove:       true,
		}},
		FileTransactionOptions{Root: root},
	)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(result.Removed, ","); got != "obsolete.txt" {
		t.Fatalf("Removed = %q", got)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("removed target still exists: %v", err)
	}
	assertNoTransactionArtifacts(t, root)
}

func TestCommitFileTransactionDoesNotClobberConcurrentCreation(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "new.txt")
	ops := defaultFileTransactionOps
	ops.link = func(oldname, newname string) error {
		if newname == target && strings.HasPrefix(filepath.Base(oldname), ".kpro-transaction-stage-") {
			if err := os.WriteFile(target, []byte("concurrent"), 0o600); err != nil {
				return err
			}
		}
		return os.Link(oldname, newname)
	}
	_, err := commitFileTransaction(
		[]FileMutation{{
			Path:         "new.txt",
			Content:      []byte("generated"),
			Precondition: ExpectFileMissing(),
		}},
		FileTransactionOptions{Root: root},
		ops,
	)
	if !errors.Is(err, ErrTransactionConflict) {
		t.Fatalf("commitFileTransaction() error = %v, want ErrTransactionConflict", err)
	}
	assertFileContent(t, target, "concurrent")
	assertNoTransactionArtifacts(t, root)
}

func TestCommitFileTransactionDoesNotClobberConcurrentReplacement(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "existing.txt")
	if err := os.WriteFile(target, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	ops := defaultFileTransactionOps
	ops.link = func(oldname, newname string) error {
		if newname == target && strings.HasPrefix(filepath.Base(oldname), ".kpro-transaction-stage-") {
			if err := os.WriteFile(target, []byte("concurrent"), 0o600); err != nil {
				return err
			}
		}
		return os.Link(oldname, newname)
	}
	_, err := commitFileTransaction(
		[]FileMutation{{
			Path:         "existing.txt",
			Content:      []byte("generated"),
			Precondition: ExpectFileContent([]byte("old")),
		}},
		FileTransactionOptions{Root: root},
		ops,
	)
	if !errors.Is(err, ErrTransactionConflict) || !strings.Contains(err.Error(), "backup retained at") {
		t.Fatalf("commitFileTransaction() error = %v, want conflict with retained backup", err)
	}
	assertFileContent(t, target, "concurrent")
	backups, globErr := filepath.Glob(filepath.Join(root, ".kpro-transaction-backup-*"))
	if globErr != nil {
		t.Fatal(globErr)
	}
	if len(backups) != 1 {
		t.Fatalf("backups = %v, want one", backups)
	}
	assertFileContent(t, backups[0], "old")
	stages, globErr := filepath.Glob(filepath.Join(root, ".kpro-transaction-stage-*"))
	if globErr != nil || len(stages) != 0 {
		t.Fatalf("stages = %v, %v", stages, globErr)
	}
}

func TestCommitFileTransactionExplicitModeConverges(t *testing.T) {
	root := t.TempDir()
	content := []byte("same")
	mutation := FileMutation{
		Path:         "mode.txt",
		Content:      content,
		Mode:         0o600,
		Precondition: ExpectFileMissing(),
	}
	if _, err := CommitFileTransaction([]FileMutation{mutation}, FileTransactionOptions{Root: root}); err != nil {
		t.Fatal(err)
	}
	mutation.Precondition = ExpectFileContent(content)
	result, err := CommitFileTransaction([]FileMutation{mutation}, FileTransactionOptions{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if result.Changed() || strings.Join(result.Unchanged, ",") != "mode.txt" {
		t.Fatalf("second transaction = %#v, want unchanged", result)
	}
}

func TestCommitFileTransactionDetectsConcurrentModeChange(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows exposes only the owner-write permission bit")
	}
	root := t.TempDir()
	target := filepath.Join(root, "mode.txt")
	if err := os.WriteFile(target, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	ops := defaultFileTransactionOps
	ops.rename = func(oldname, newname string) error {
		if oldname == target && strings.HasPrefix(filepath.Base(newname), ".kpro-transaction-backup-") {
			if err := os.Chmod(oldname, 0o644); err != nil {
				return err
			}
		}
		return os.Rename(oldname, newname)
	}
	_, err := commitFileTransaction(
		[]FileMutation{{
			Path:         "mode.txt",
			Content:      []byte("new"),
			Precondition: ExpectFileContent([]byte("old")),
		}},
		FileTransactionOptions{Root: root},
		ops,
	)
	if !errors.Is(err, ErrTransactionConflict) {
		t.Fatalf("error = %v, want ErrTransactionConflict", err)
	}
	assertFileContent(t, target, "old")
	info, statErr := os.Stat(target)
	if statErr != nil {
		t.Fatal(statErr)
	}
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("restored concurrent mode = %v, want 0644", info.Mode().Perm())
	}
	assertNoTransactionArtifacts(t, root)
}

func TestCommitFileTransactionHardLinkPreflightFailsBeforeTargetRename(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "obsolete.txt")
	if err := os.WriteFile(target, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	ops := defaultFileTransactionOps
	targetRenamed := false
	ops.rename = func(oldname, newname string) error {
		if oldname == target {
			targetRenamed = true
		}
		return os.Rename(oldname, newname)
	}
	ops.link = func(string, string) error {
		return fs.ErrInvalid
	}

	_, err := commitFileTransaction(
		[]FileMutation{{
			Path:         "obsolete.txt",
			Precondition: ExpectFileContent([]byte("keep")),
			Remove:       true,
		}},
		FileTransactionOptions{Root: root},
		ops,
	)
	if err == nil || !strings.Contains(err.Error(), "verify hard-link support") {
		t.Fatalf("commitFileTransaction() error = %v, want hard-link preflight failure", err)
	}
	if targetRenamed {
		t.Fatal("hard-link preflight failure renamed a transaction target")
	}
	assertFileContent(t, target, "keep")
	assertNoTransactionArtifacts(t, root)
}

func TestCommitFileTransactionRestoresRemovalWhenLaterInstallFails(t *testing.T) {
	root := t.TempDir()
	removedTarget := filepath.Join(root, "a.txt")
	if err := os.WriteFile(removedTarget, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	failedTarget := filepath.Join(root, "z.txt")
	ops := defaultFileTransactionOps
	ops.link = func(oldname, newname string) error {
		if newname == failedTarget && strings.HasPrefix(filepath.Base(oldname), ".kpro-transaction-stage-") {
			return fs.ErrPermission
		}
		return os.Link(oldname, newname)
	}

	_, err := commitFileTransaction(
		[]FileMutation{
			{Path: "a.txt", Precondition: ExpectFileContent([]byte("old")), Remove: true},
			{Path: "z.txt", Content: []byte("fail"), Precondition: ExpectFileMissing()},
		},
		FileTransactionOptions{Root: root},
		ops,
	)
	if err == nil || !strings.Contains(err.Error(), `install "z.txt"`) {
		t.Fatalf("commitFileTransaction() error = %v, want injected install failure", err)
	}
	assertFileContent(t, removedTarget, "old")
	if _, statErr := os.Stat(failedTarget); !os.IsNotExist(statErr) {
		t.Fatalf("failed target stat = %v, want missing", statErr)
	}
	assertNoTransactionArtifacts(t, root)
}

func TestCommitFileTransactionObserveOnlyDoesNotCreateDirectories(t *testing.T) {
	root := t.TempDir()
	result, err := CommitFileTransaction(
		[]FileMutation{
			{Path: "created.txt", Content: []byte("created"), Precondition: ExpectFileMissing()},
			{Path: "nested/missing.txt", Precondition: ExpectFileMissing(), ObserveOnly: true},
		},
		FileTransactionOptions{Root: root},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed() || strings.Join(result.Unchanged, ",") != "nested/missing.txt" {
		t.Fatalf("result = %#v", result)
	}
	if _, statErr := os.Stat(filepath.Join(root, "nested")); !os.IsNotExist(statErr) {
		t.Fatalf("observed parent directory stat = %v, want missing", statErr)
	}
	assertNoTransactionArtifacts(t, root)
}

func TestCommitFileTransactionObserveOnlyDetectsMissingPathAppearing(t *testing.T) {
	root := t.TempDir()
	createdTarget := filepath.Join(root, "created.txt")
	observedTarget := filepath.Join(root, "nested", "missing.txt")
	ops := defaultFileTransactionOps
	ops.chmodHandle = func(file *os.File, filename string, mode fs.FileMode) error {
		if err := file.Chmod(mode); err != nil {
			return err
		}
		if filename != createdTarget {
			return nil
		}
		if err := os.MkdirAll(filepath.Dir(observedTarget), 0o755); err != nil {
			return err
		}
		return os.WriteFile(observedTarget, []byte("external"), 0o600)
	}

	_, err := commitFileTransaction(
		[]FileMutation{
			{Path: "created.txt", Content: []byte("created"), Precondition: ExpectFileMissing()},
			{Path: "nested/missing.txt", Precondition: ExpectFileMissing(), ObserveOnly: true},
		},
		FileTransactionOptions{Root: root},
		ops,
	)
	if !errors.Is(err, ErrTransactionConflict) {
		t.Fatalf("commitFileTransaction() error = %v, want ErrTransactionConflict", err)
	}
	if _, statErr := os.Stat(createdTarget); !os.IsNotExist(statErr) {
		t.Fatalf("created output stat = %v, want rollback removal", statErr)
	}
	assertFileContent(t, observedTarget, "external")
	assertNoTransactionArtifacts(t, root)
}

func TestCommitFileTransactionCreatesReadOnlyOutput(t *testing.T) {
	root := t.TempDir()
	makeReadOnlyCleanupSafe(t, root)
	target := filepath.Join(root, "readonly.txt")
	_, err := CommitFileTransaction(
		[]FileMutation{{
			Path:         "readonly.txt",
			Content:      []byte("generated"),
			Mode:         0o444,
			Precondition: ExpectFileMissing(),
		}},
		FileTransactionOptions{Root: root},
	)
	if err != nil {
		t.Fatalf("CommitFileTransaction() error = %v", err)
	}
	assertFileContent(t, target, "generated")
	assertFileModeEquivalent(t, target, 0o444)
	assertNoTransactionArtifacts(t, root)
}

func TestCommitFileTransactionUpdatesReadOnlyOriginal(t *testing.T) {
	root := t.TempDir()
	makeReadOnlyCleanupSafe(t, root)
	target := filepath.Join(root, "readonly.txt")
	if err := os.WriteFile(target, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(target, 0o444); err != nil {
		t.Fatal(err)
	}

	_, err := CommitFileTransaction(
		[]FileMutation{{
			Path:         "readonly.txt",
			Content:      []byte("updated"),
			Precondition: ExpectFileContent([]byte("old")),
		}},
		FileTransactionOptions{Root: root},
	)
	if err != nil {
		t.Fatalf("CommitFileTransaction() error = %v", err)
	}
	assertFileContent(t, target, "updated")
	assertFileModeEquivalent(t, target, 0o444)
	assertNoTransactionArtifacts(t, root)
}

func TestCommitFileTransactionRollsBackReadOnlyInstalledAndBackupFiles(t *testing.T) {
	root := t.TempDir()
	makeReadOnlyCleanupSafe(t, root)
	target := filepath.Join(root, "a.txt")
	if err := os.WriteFile(target, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(target, 0o444); err != nil {
		t.Fatal(err)
	}
	failedTarget := filepath.Join(root, "z.txt")
	ops := defaultFileTransactionOps
	ops.link = func(oldname, newname string) error {
		if newname == failedTarget && strings.HasPrefix(filepath.Base(oldname), ".kpro-transaction-stage-") {
			return fs.ErrPermission
		}
		return os.Link(oldname, newname)
	}

	_, err := commitFileTransaction(
		[]FileMutation{
			{
				Path:         "a.txt",
				Content:      []byte("updated"),
				Precondition: ExpectFileContent([]byte("old")),
			},
			{
				Path:         "z.txt",
				Content:      []byte("fail"),
				Precondition: ExpectFileMissing(),
			},
		},
		FileTransactionOptions{Root: root},
		ops,
	)
	if err == nil || !strings.Contains(err.Error(), `install "z.txt"`) {
		t.Fatalf("commitFileTransaction() error = %v, want injected install failure", err)
	}
	assertFileContent(t, target, "old")
	assertFileModeEquivalent(t, target, 0o444)
	if _, statErr := os.Stat(failedTarget); !os.IsNotExist(statErr) {
		t.Fatalf("failed target remains: %v", statErr)
	}
	assertNoTransactionArtifacts(t, root)
}

func TestCommitFileTransactionRollsBackWhenInstalledChmodFails(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "readonly.txt")
	if err := os.WriteFile(target, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	ops := defaultFileTransactionOps
	ops.chmodHandle = func(file *os.File, filename string, mode fs.FileMode) error {
		if filename == target && mode.Perm() == 0o444 {
			return fs.ErrPermission
		}
		return file.Chmod(mode)
	}

	_, err := commitFileTransaction(
		[]FileMutation{{
			Path:         "readonly.txt",
			Content:      []byte("generated"),
			Mode:         0o444,
			Precondition: ExpectFileContent([]byte("old")),
		}},
		FileTransactionOptions{Root: root},
		ops,
	)
	if err == nil || !strings.Contains(err.Error(), "set installed mode") {
		t.Fatalf("commitFileTransaction() error = %v, want chmod failure", err)
	}
	assertFileContent(t, target, "old")
	assertFileModeEquivalent(t, target, 0o600)
	assertNoTransactionArtifacts(t, root)
}

func TestCommitFileTransactionRestoresConcurrentReplacementMovedToBackup(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "existing.txt")
	if err := os.WriteFile(target, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	ops := defaultFileTransactionOps
	ops.rename = func(oldname, newname string) error {
		if oldname == target && strings.HasPrefix(filepath.Base(newname), ".kpro-transaction-backup-") {
			if err := os.WriteFile(oldname, []byte("concurrent"), 0o600); err != nil {
				return err
			}
		}
		return os.Rename(oldname, newname)
	}

	_, err := commitFileTransaction(
		[]FileMutation{{
			Path:         "existing.txt",
			Content:      []byte("generated"),
			Precondition: ExpectFileContent([]byte("old")),
		}},
		FileTransactionOptions{Root: root},
		ops,
	)
	if !errors.Is(err, ErrTransactionConflict) {
		t.Fatalf("commitFileTransaction() error = %v, want ErrTransactionConflict", err)
	}
	assertFileContent(t, target, "concurrent")
	assertNoTransactionArtifacts(t, root)
}

func TestCommitFileTransactionDoesNotDeleteForeignBackupPathAfterRenameFailure(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "existing.txt")
	if err := os.WriteFile(target, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	var foreignBackup string
	ops := defaultFileTransactionOps
	ops.rename = func(oldname, newname string) error {
		if oldname == target && strings.HasPrefix(filepath.Base(newname), ".kpro-transaction-backup-") {
			foreignBackup = newname
			if err := os.WriteFile(newname, []byte("foreign"), 0o600); err != nil {
				return err
			}
			return fs.ErrExist
		}
		return os.Rename(oldname, newname)
	}

	_, err := commitFileTransaction(
		[]FileMutation{{
			Path:         "existing.txt",
			Content:      []byte("generated"),
			Precondition: ExpectFileContent([]byte("old")),
		}},
		FileTransactionOptions{Root: root},
		ops,
	)
	if err == nil || !strings.Contains(err.Error(), `backup "existing.txt"`) {
		t.Fatalf("commitFileTransaction() error = %v, want backup failure", err)
	}
	assertFileContent(t, target, "old")
	if foreignBackup == "" {
		t.Fatal("rename hook did not create a foreign backup path")
	}
	assertFileContent(t, foreignBackup, "foreign")
	stages, globErr := filepath.Glob(filepath.Join(root, ".kpro-transaction-stage-*"))
	if globErr != nil || len(stages) != 0 {
		t.Fatalf("staged files after rename failure = %v, %v", stages, globErr)
	}
}

func TestCommitFileTransactionRetainsBackupChangedBeforeCleanup(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "existing.txt")
	if err := os.WriteFile(target, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	var changedBackup string
	ops := defaultFileTransactionOps
	ops.chmodHandle = func(file *os.File, filename string, mode fs.FileMode) error {
		if err := file.Chmod(mode); err != nil {
			return err
		}
		if filename != target {
			return nil
		}
		backups, err := filepath.Glob(filepath.Join(root, ".kpro-transaction-backup-*"))
		if err != nil || len(backups) != 1 {
			return fmt.Errorf("find transaction backup: %v, matches %v", err, backups)
		}
		changedBackup = backups[0]
		return os.WriteFile(changedBackup, []byte("concurrent"), 0o600)
	}

	result, err := commitFileTransaction(
		[]FileMutation{{
			Path:         "existing.txt",
			Content:      []byte("generated"),
			Precondition: ExpectFileContent([]byte("old")),
		}},
		FileTransactionOptions{Root: root},
		ops,
	)
	if err != nil {
		t.Fatalf("commitFileTransaction() error = %v", err)
	}
	assertFileContent(t, target, "generated")
	assertFileContent(t, changedBackup, "concurrent")
	if len(result.CleanupWarnings) != 1 || !strings.Contains(result.CleanupWarnings[0], filepath.Base(changedBackup)) ||
		!strings.Contains(result.CleanupWarnings[0], "retained changed backup") {
		t.Fatalf("CleanupWarnings = %v", result.CleanupWarnings)
	}
}

func TestCommitFileTransactionRetainsReplacedBackupWithSameState(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "existing.txt")
	if err := os.WriteFile(target, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	var replacedBackup string
	ops := defaultFileTransactionOps
	ops.chmodHandle = func(file *os.File, filename string, mode fs.FileMode) error {
		if err := file.Chmod(mode); err != nil {
			return err
		}
		if filename != target {
			return nil
		}
		backups, err := filepath.Glob(filepath.Join(root, ".kpro-transaction-backup-*"))
		if err != nil || len(backups) != 1 {
			return fmt.Errorf("find transaction backup: %v, matches %v", err, backups)
		}
		replacedBackup = backups[0]
		if err := os.Remove(replacedBackup); err != nil {
			return err
		}
		return os.WriteFile(replacedBackup, []byte("old"), 0o600)
	}

	result, err := commitFileTransaction(
		[]FileMutation{{
			Path:         "existing.txt",
			Content:      []byte("generated"),
			Precondition: ExpectFileContent([]byte("old")),
		}},
		FileTransactionOptions{Root: root},
		ops,
	)
	if err != nil {
		t.Fatalf("commitFileTransaction() error = %v", err)
	}
	assertFileContent(t, target, "generated")
	assertFileContent(t, replacedBackup, "old")
	if len(result.CleanupWarnings) != 1 || !strings.Contains(result.CleanupWarnings[0], filepath.Base(replacedBackup)) {
		t.Fatalf("CleanupWarnings = %v", result.CleanupWarnings)
	}
}

func TestCommitFileTransactionReportsWindowsRestoreModeFailureAccurately(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows removes a read-only hard link only after changing its shared mode")
	}
	root := t.TempDir()
	makeReadOnlyCleanupSafe(t, root)
	target := filepath.Join(root, "a.txt")
	if err := os.WriteFile(target, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(target, 0o444); err != nil {
		t.Fatal(err)
	}
	failedTarget := filepath.Join(root, "z.txt")
	var backup string
	ops := defaultFileTransactionOps
	ops.rename = func(oldname, newname string) error {
		if oldname == target {
			backup = newname
		}
		return os.Rename(oldname, newname)
	}
	ops.link = func(oldname, newname string) error {
		if newname == failedTarget && strings.HasPrefix(filepath.Base(oldname), ".kpro-transaction-stage-") {
			return fs.ErrPermission
		}
		return os.Link(oldname, newname)
	}
	ops.chmod = func(filename string, mode fs.FileMode) error {
		if filename == target && mode.Perm()&0o200 == 0 {
			return fs.ErrPermission
		}
		return os.Chmod(filename, mode)
	}

	_, err := commitFileTransaction(
		[]FileMutation{
			{
				Path:         "a.txt",
				Content:      []byte("updated"),
				Mode:         0o600,
				Precondition: ExpectFileContent([]byte("old")),
			},
			{
				Path:         "z.txt",
				Content:      []byte("fail"),
				Precondition: ExpectFileMissing(),
			},
		},
		FileTransactionOptions{Root: root},
		ops,
	)
	if err == nil || !strings.Contains(err.Error(), "could not restore its mode") {
		t.Fatalf("commitFileTransaction() error = %v, want restore-mode failure", err)
	}
	assertFileContent(t, target, "old")
	if backup == "" {
		t.Fatal("transaction did not reserve a backup")
	}
	if _, statErr := os.Lstat(backup); !os.IsNotExist(statErr) {
		t.Fatalf("diagnostic claimed no retained backup, stat = %v", statErr)
	}
	if strings.Contains(err.Error(), "backup retained at "+backup) {
		t.Fatalf("error incorrectly claims a retained backup: %v", err)
	}
}

func TestCommitFileTransactionDoesNotChmodTargetReplacedDuringModeApplication(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "existing.txt")
	if err := os.WriteFile(target, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	var backup string
	var replacementErr error
	ops := defaultFileTransactionOps
	ops.rename = func(oldname, newname string) error {
		if oldname == target {
			backup = newname
		}
		return os.Rename(oldname, newname)
	}
	ops.chmodHandle = func(file *os.File, filename string, mode fs.FileMode) error {
		if filename != target {
			return file.Chmod(mode)
		}
		replacementErr = os.Remove(target)
		if replacementErr != nil {
			return file.Chmod(mode)
		}
		if err := os.WriteFile(target, []byte("foreign"), 0o600); err != nil {
			return err
		}
		return file.Chmod(mode)
	}

	_, err := commitFileTransaction(
		[]FileMutation{{
			Path:         "existing.txt",
			Content:      []byte("generated"),
			Mode:         0o444,
			Precondition: ExpectFileContent([]byte("old")),
		}},
		FileTransactionOptions{Root: root},
		ops,
	)
	if runtime.GOOS == "windows" {
		if err != nil {
			t.Fatalf("commitFileTransaction() error = %v", err)
		}
		if replacementErr == nil {
			t.Fatal("open installed inode did not block replacement on Windows")
		}
		assertFileContent(t, target, "generated")
		assertFileModeEquivalent(t, target, 0o444)
		assertNoTransactionArtifacts(t, root)
		return
	}
	if !errors.Is(err, ErrTransactionConflict) {
		t.Fatalf("commitFileTransaction() error = %v, want ErrTransactionConflict", err)
	}
	assertFileContent(t, target, "foreign")
	assertFileModeEquivalent(t, target, 0o600)
	assertFileContent(t, backup, "old")
}

func TestCommitFileTransactionPreservesChangedCreatedTargetWithoutFakeBackup(t *testing.T) {
	root := t.TempDir()
	created := filepath.Join(root, "a.txt")
	failedTarget := filepath.Join(root, "z.txt")
	ops := defaultFileTransactionOps
	ops.link = func(oldname, newname string) error {
		if newname == failedTarget && strings.HasPrefix(filepath.Base(oldname), ".kpro-transaction-stage-") {
			if err := os.WriteFile(created, []byte("concurrent"), 0o600); err != nil {
				return err
			}
			return fs.ErrPermission
		}
		return os.Link(oldname, newname)
	}

	_, err := commitFileTransaction(
		[]FileMutation{
			{Path: "a.txt", Content: []byte("generated"), Precondition: ExpectFileMissing()},
			{Path: "z.txt", Content: []byte("fail"), Precondition: ExpectFileMissing()},
		},
		FileTransactionOptions{Root: root},
		ops,
	)
	if err == nil || !strings.Contains(err.Error(), "no backup exists for this create") {
		t.Fatalf("commitFileTransaction() error = %v, want create recovery diagnostic", err)
	}
	if strings.Contains(err.Error(), `backup retained at ""`) {
		t.Fatalf("error contains a fake empty backup path: %v", err)
	}
	assertFileContent(t, created, "concurrent")
	assertNoTransactionArtifacts(t, root)
}

func TestCommitFileTransactionReportsBackupWhenInstalledTargetCannotBeInspected(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "a.txt")
	if err := os.WriteFile(target, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	failedTarget := filepath.Join(root, "z.txt")
	var backup string
	ops := defaultFileTransactionOps
	ops.rename = func(oldname, newname string) error {
		if oldname == target {
			backup = newname
		}
		return os.Rename(oldname, newname)
	}
	ops.link = func(oldname, newname string) error {
		if newname == failedTarget && strings.HasPrefix(filepath.Base(oldname), ".kpro-transaction-stage-") {
			if err := os.Remove(target); err != nil {
				return err
			}
			if err := os.Mkdir(target, 0o755); err != nil {
				return err
			}
			return fs.ErrPermission
		}
		return os.Link(oldname, newname)
	}

	_, err := commitFileTransaction(
		[]FileMutation{
			{Path: "a.txt", Content: []byte("generated"), Precondition: ExpectFileContent([]byte("old"))},
			{Path: "z.txt", Content: []byte("fail"), Precondition: ExpectFileMissing()},
		},
		FileTransactionOptions{Root: root},
		ops,
	)
	if err == nil || !strings.Contains(err.Error(), filepath.Base(backup)) ||
		!strings.Contains(err.Error(), "backup retained") {
		t.Fatalf("commitFileTransaction() error = %v, want retained backup path", err)
	}
	assertFileContent(t, backup, "old")
	if info, statErr := os.Stat(target); statErr != nil || !info.IsDir() {
		t.Fatalf("concurrent target state = %v, %v; want directory", info, statErr)
	}
}

func TestCommitFileTransactionRetainsBackupWhenRestoredTargetIsReplaced(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "a.txt")
	if err := os.WriteFile(target, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	failedTarget := filepath.Join(root, "z.txt")
	var backup string
	ops := defaultFileTransactionOps
	ops.rename = func(oldname, newname string) error {
		if oldname == target {
			backup = newname
		}
		return os.Rename(oldname, newname)
	}
	ops.link = func(oldname, newname string) error {
		switch {
		case newname == failedTarget && strings.HasPrefix(filepath.Base(oldname), ".kpro-transaction-stage-"):
			return fs.ErrPermission
		case oldname == backup && newname == target:
			if err := os.Link(oldname, newname); err != nil {
				return err
			}
			if err := os.Remove(newname); err != nil {
				return err
			}
			if err := os.WriteFile(newname, []byte("foreign"), 0o600); err != nil {
				return err
			}
			return nil
		default:
			return os.Link(oldname, newname)
		}
	}

	_, err := commitFileTransaction(
		[]FileMutation{
			{Path: "a.txt", Content: []byte("generated"), Precondition: ExpectFileContent([]byte("old"))},
			{Path: "z.txt", Content: []byte("fail"), Precondition: ExpectFileMissing()},
		},
		FileTransactionOptions{Root: root},
		ops,
	)
	if !errors.Is(err, ErrTransactionConflict) || !strings.Contains(err.Error(), "cannot finalize restoration") {
		t.Fatalf("commitFileTransaction() error = %v, want restored-target conflict", err)
	}
	assertFileContent(t, target, "foreign")
	assertFileContent(t, backup, "old")
}

func assertFileContent(t *testing.T, filename, want string) {
	t.Helper()
	content, err := os.ReadFile(filename)
	if err != nil {
		t.Fatalf("read %q: %v", filename, err)
	}
	if got := string(content); got != want {
		t.Fatalf("%q content = %q, want %q", filename, got, want)
	}
}

func assertFileModeEquivalent(t *testing.T, filename string, want fs.FileMode) {
	t.Helper()
	info, err := os.Stat(filename)
	if err != nil {
		t.Fatalf("stat %q: %v", filename, err)
	}
	if !permissionModesEquivalent(info.Mode(), want) {
		t.Fatalf("%q mode = %v, want %v", filename, info.Mode().Perm(), want.Perm())
	}
}

func makeReadOnlyCleanupSafe(t *testing.T, root string) {
	t.Helper()
	t.Cleanup(func() {
		_ = filepath.WalkDir(root, func(filename string, entry fs.DirEntry, walkErr error) error {
			if walkErr == nil && entry.Type().IsRegular() {
				_ = os.Chmod(filename, 0o644)
			}
			return nil
		})
	})
}

func assertNoTransactionArtifacts(t *testing.T, root string) {
	t.Helper()
	err := filepath.WalkDir(root, func(filename string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if strings.HasPrefix(entry.Name(), ".kpro-transaction-") {
			t.Errorf("transaction artifact remains: %s", filename)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
