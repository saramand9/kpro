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
	ops.rename = func(oldname, newname string) error {
		if newname == failedTarget {
			return fs.ErrPermission
		}
		return os.Rename(oldname, newname)
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
	ops.rename = func(oldname, newname string) error {
		if newname == failedInstall ||
			(strings.Contains(filepath.Base(oldname), ".kpro-transaction-backup-") && newname == existingPath) {
			return fs.ErrPermission
		}
		return os.Rename(oldname, newname)
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
			name: "file and parent",
			mutations: []FileMutation{
				{Path: "a"},
				{Path: "a/b"},
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
