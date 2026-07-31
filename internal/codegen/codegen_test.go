package codegen

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPlanNormalizesAndRejectsUnsafeOrDuplicatePaths(t *testing.T) {
	invalidUTF8Path := string([]byte{'b', 'a', 'd', 0xff, '.', 'g', 'o'})
	plan, err := BuildPlan(
		File{Path: `z\file.txt`, Content: []byte("z"), Ownership: Generated},
		File{Path: "a/file.txt", Content: []byte("a"), Ownership: User},
	)
	if err != nil {
		t.Fatal(err)
	}
	if got := []string{plan.Files[0].Path, plan.Files[1].Path}; strings.Join(got, ",") != "a/file.txt,z/file.txt" {
		t.Fatalf("plan order = %v", got)
	}
	for _, unsafe := range []string{
		"../escape", "/absolute", `C:\escape`, `\\server\share`, "a/../b", ".",
		"NUL.go", "dir/con.txt", "con .txt", "COM¹.log", "trailing. ", "trailing.", "stream:name", "bad?.go",
		invalidUTF8Path,
	} {
		if _, err := BuildPlan(File{Path: unsafe, Ownership: Generated}); !errors.Is(err, ErrUnsafePath) {
			t.Fatalf("BuildPlan(%q) error = %v, want ErrUnsafePath", unsafe, err)
		}
	}
	if _, err := BuildPlan(
		File{Path: "same", Ownership: Generated},
		File{Path: `same`, Ownership: User},
	); !errors.Is(err, ErrDuplicatePath) {
		t.Fatalf("duplicate error = %v", err)
	}
	if _, err := BuildPlan(
		File{Path: "a", Ownership: Generated},
		File{Path: "a/b", Ownership: Generated},
	); !errors.Is(err, ErrDuplicatePath) {
		t.Fatalf("parent collision error = %v", err)
	}
	if _, err := BuildPlan(
		File{Path: "Output.go", Ownership: Generated},
		File{Path: "output.go", Ownership: Generated},
	); !errors.Is(err, ErrDuplicatePath) {
		t.Fatalf("case alias error = %v", err)
	}
	if _, err := BuildPlan(
		File{Path: "Parent", Ownership: Generated},
		File{Path: "parent/child.go", Ownership: Generated},
	); !errors.Is(err, ErrDuplicatePath) {
		t.Fatalf("case-folded parent collision error = %v", err)
	}
	if key, err := PortablePathKey(`Dir\Output.GO`); err != nil || key != "dir/output.go" {
		t.Fatalf("PortablePathKey() = %q, %v", key, err)
	}
	left, leftErr := PortablePathKey("S.go")
	right, rightErr := PortablePathKey("ſ.go")
	if leftErr != nil || rightErr != nil || left != right {
		t.Fatal("PortablePathKey() does not apply Unicode simple case folding")
	}
	composed, composedErr := PortablePathKey("\u00e9.go")
	decomposed, decomposedErr := PortablePathKey("e\u0301.go")
	if composedErr != nil || decomposedErr != nil || composed != decomposed {
		t.Fatalf(
			"PortablePathKey() canonical aliases = %q, %v and %q, %v",
			composed,
			composedErr,
			decomposed,
			decomposedErr,
		)
	}
	if _, err := BuildPlan(
		File{Path: "\u00e9.go", Ownership: Generated},
		File{Path: "e\u0301.go", Ownership: Generated},
	); !errors.Is(err, ErrDuplicatePath) {
		t.Fatalf("canonical alias error = %v", err)
	}
	if _, err := PortablePathKey(invalidUTF8Path); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("PortablePathKey(invalid UTF-8) error = %v, want ErrUnsafePath", err)
	}
	if collides, err := PortablePathsCollide("Parent", "parent/child.go"); err != nil || !collides {
		t.Fatalf("PortablePathsCollide() = %v, %v", collides, err)
	}
}

func TestLockRoundTripIsDeterministic(t *testing.T) {
	lock := NewLock()
	lock.Files["z.txt"] = LockEntry{InputSHA256: hashBytes([]byte("z")), OutputSHA256: hashBytes([]byte("z")), Ownership: Generated}
	lock.Files["a.txt"] = LockEntry{InputSHA256: hashBytes([]byte("a")), OutputSHA256: hashBytes([]byte("a")), Ownership: User}
	first, err := EncodeLock(lock)
	if err != nil {
		t.Fatal(err)
	}
	second, err := EncodeLock(lock)
	if err != nil || string(first) != string(second) {
		t.Fatalf("lock not deterministic: %v\n%s\n%s", err, first, second)
	}
	decoded, err := DecodeLock(first)
	if err != nil || len(decoded.Files) != 2 || decoded.Files["a.txt"].Ownership != User {
		t.Fatalf("DecodeLock() = %#v, %v", decoded, err)
	}
	filename := filepath.Join(t.TempDir(), "lock.json")
	if err := os.WriteFile(filename, first, 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := ReadLock(filename); err != nil || len(got.Files) != 2 {
		t.Fatalf("ReadLock() = %#v, %v", got, err)
	}
}

func TestLockRejectsNonCanonicalAliasesAndNonCanonicalHashes(t *testing.T) {
	entry := LockEntry{
		InputSHA256:  hashBytes([]byte("input")),
		OutputSHA256: hashBytes([]byte("output")),
		Ownership:    Generated,
	}
	tests := []struct {
		name string
		lock Lock
		want error
	}{
		{
			name: "non canonical separator",
			lock: Lock{Version: lockVersion, Files: map[string]LockEntry{`a\b.go`: entry}},
		},
		{
			name: "case alias",
			lock: Lock{Version: lockVersion, Files: map[string]LockEntry{"A.go": entry, "a.go": entry}},
			want: ErrDuplicatePath,
		},
		{
			name: "case folded parent",
			lock: Lock{Version: lockVersion, Files: map[string]LockEntry{"A": entry, "a/b.go": entry}},
			want: ErrDuplicatePath,
		},
		{
			name: "uppercase hash",
			lock: Lock{Version: lockVersion, Files: map[string]LockEntry{
				"a.go": {
					InputSHA256:  strings.ToUpper(entry.InputSHA256),
					OutputSHA256: entry.OutputSHA256,
					Ownership:    Generated,
				},
			}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.lock.Validate()
			if err == nil || test.want != nil && !errors.Is(err, test.want) {
				t.Fatalf("Validate() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestDecodeLockRejectsNonCanonicalPath(t *testing.T) {
	hash := hashBytes([]byte("content"))
	data := []byte(fmt.Sprintf(
		`{"version":1,"files":[{"path":"a\\b.go","input_sha256":%q,"output_sha256":%q,"ownership":"generated"}]}`,
		hash,
		hash,
	))
	if _, err := DecodeLock(data); err == nil || !strings.Contains(err.Error(), "not canonical") {
		t.Fatalf("DecodeLock() error = %v, want non-canonical path rejection", err)
	}
}

func TestDecodeLockRejectsInvalidUTF8(t *testing.T) {
	data := append(
		[]byte(`{"version":1,"files":[{"path":"bad`),
		0xff,
	)
	data = append(data, []byte(`.go"}]}`)...)
	if _, err := DecodeLock(data); err == nil || !strings.Contains(err.Error(), "not valid UTF-8") {
		t.Fatalf("DecodeLock(invalid UTF-8) error = %v", err)
	}
}

func TestExecuteRejectsPortableLockOutputAlias(t *testing.T) {
	for _, output := range []string{"KPRO.GEN.LOCK.JSON", "Lock/child.json", "parent"} {
		plan, err := BuildPlan(File{
			Path:      output,
			Content:   []byte("output"),
			Ownership: Generated,
		})
		if err != nil {
			t.Fatal(err)
		}
		lockFile := DefaultLockFile
		if output == "Lock/child.json" {
			lockFile = "lock"
		} else if output == "parent" {
			lockFile = "PARENT/lock.json"
		}
		if _, err := Execute(plan, Options{Root: t.TempDir(), LockFile: lockFile, Mode: DryRun}); err == nil ||
			!strings.Contains(err.Error(), "lock file conflicts") {
			t.Fatalf("Execute(%q, lock %q) error = %v, want lock conflict", output, lockFile, err)
		}
	}
}

func TestExecuteApplyCheckAndDryRun(t *testing.T) {
	root := t.TempDir()
	plan, err := BuildPlan(File{Path: "gen/main.go", Content: []byte("package gen\nfunc  F( ) { }\n"), Ownership: Generated})
	if err != nil {
		t.Fatal(err)
	}
	result, err := Execute(plan, Options{Root: root})
	if err != nil || strings.Join(result.Created, ",") != "gen/main.go" || !result.LockChanged {
		t.Fatalf("apply = %#v, %v", result, err)
	}
	got, err := os.ReadFile(filepath.Join(root, "gen", "main.go"))
	if err != nil || string(got) != "package gen\n\nfunc F() {}\n" {
		t.Fatalf("gofmt output = %q, %v", got, err)
	}
	result, err = Execute(plan, Options{Root: root, Mode: Check})
	if err != nil || result.Changed() {
		t.Fatalf("check clean = %#v, %v", result, err)
	}
	changed, err := BuildPlan(File{Path: "gen/main.go", Content: []byte("package gen\nfunc G(){}\n"), Ownership: Generated})
	if err != nil {
		t.Fatal(err)
	}
	result, err = Execute(changed, Options{Root: root, Mode: DryRun})
	if err != nil || strings.Join(result.Updated, ",") != "gen/main.go" {
		t.Fatalf("dry run = %#v, %v", result, err)
	}
	if still, _ := os.ReadFile(filepath.Join(root, "gen", "main.go")); string(still) != string(got) {
		t.Fatalf("dry run wrote output: %q", still)
	}
	result, err = Execute(changed, Options{Root: root, Mode: Check})
	if !errors.Is(err, ErrOutOfDate) || strings.Join(result.Updated, ",") != "gen/main.go" {
		t.Fatalf("check stale = %#v, %v", result, err)
	}
	result, err = Execute(changed, Options{Root: root, Mode: Apply})
	if err != nil || strings.Join(result.Updated, ",") != "gen/main.go" {
		t.Fatalf("apply update = %#v, %v", result, err)
	}
	if updated, readErr := os.ReadFile(filepath.Join(root, "gen", "main.go")); readErr != nil || string(updated) != "package gen\n\nfunc G() {}\n" {
		t.Fatalf("updated output = %q, %v", updated, readErr)
	}
	result, err = Execute(changed, Options{Root: root, Mode: Check})
	if err != nil || result.Changed() {
		t.Fatalf("check after update = %#v, %v", result, err)
	}
}

func TestExecuteProtectsGeneratedAndPreservesCreateOnceFiles(t *testing.T) {
	root := t.TempDir()
	generated, _ := BuildPlan(File{Path: "generated.txt", Content: []byte("one"), Ownership: Generated})
	if _, err := Execute(generated, Options{Root: root}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "generated.txt"), []byte("edited"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Execute(generated, Options{Root: root}); !errors.Is(err, ErrGeneratedModified) {
		t.Fatalf("modified generated error = %v", err)
	}

	root = t.TempDir()
	user, _ := BuildPlan(File{Path: "user.txt", Content: []byte("starter"), Ownership: User})
	if _, err := Execute(user, Options{Root: root}); err != nil {
		t.Fatal(err)
	}
	userPath := filepath.Join(root, "user.txt")
	if err := os.WriteFile(userPath, []byte("custom"), 0o644); err != nil {
		t.Fatal(err)
	}
	userChanged, _ := BuildPlan(File{Path: "user.txt", Content: []byte("new starter"), Ownership: User})
	if _, err := Execute(userChanged, Options{Root: root}); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(userPath); string(got) != "custom" {
		t.Fatalf("user file overwritten: %q", got)
	}
	if err := os.Remove(userPath); err != nil {
		t.Fatal(err)
	}
	if _, err := Execute(userChanged, Options{Root: root}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(userPath); !os.IsNotExist(err) {
		t.Fatalf("deleted user file recreated: %v", err)
	}
}

func TestExecuteRemovesOnlyLockedUnmodifiedStaleGeneratedFiles(t *testing.T) {
	root := t.TempDir()
	plan, _ := BuildPlan(File{Path: "stale.txt", Content: []byte("generated"), Ownership: Generated})
	if _, err := Execute(plan, Options{Root: root}); err != nil {
		t.Fatal(err)
	}
	empty, _ := BuildPlan()
	result, err := Execute(empty, Options{Root: root})
	if err != nil || strings.Join(result.Removed, ",") != "stale.txt" {
		t.Fatalf("remove stale = %#v, %v", result, err)
	}
	if _, err := os.Stat(filepath.Join(root, "stale.txt")); !os.IsNotExist(err) {
		t.Fatalf("stale remains: %v", err)
	}
	plan, _ = BuildPlan(File{Path: "edited.txt", Content: []byte("generated"), Ownership: Generated})
	if _, err := Execute(plan, Options{Root: root}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "edited.txt"), []byte("human"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Execute(empty, Options{Root: root}); !errors.Is(err, ErrGeneratedModified) {
		t.Fatalf("modified stale error = %v", err)
	}
}

func TestExecuteApplyRollsBackOutputsRemovalAndLockTogether(t *testing.T) {
	root := t.TempDir()
	original, err := BuildPlan(
		File{Path: "a.txt", Content: []byte("a-old"), Ownership: Generated},
		File{Path: "b.txt", Content: []byte("b-old"), Ownership: Generated},
		File{Path: "z.txt", Content: []byte("z-old"), Ownership: Generated},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Execute(original, Options{Root: root}); err != nil {
		t.Fatal(err)
	}
	before := make(map[string][]byte)
	for _, name := range []string{"a.txt", "b.txt", "z.txt", DefaultLockFile} {
		before[name], err = os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
	}

	changed, err := BuildPlan(
		File{Path: "a.txt", Content: []byte("a-new"), Ownership: Generated},
		File{Path: "z.txt", Content: []byte("z-new"), Ownership: Generated},
	)
	if err != nil {
		t.Fatal(err)
	}
	ops := defaultFileTransactionOps
	failedTarget := filepath.Join(root, "z.txt")
	ops.link = func(oldname, newname string) error {
		if newname == failedTarget && strings.HasPrefix(filepath.Base(oldname), ".kpro-transaction-stage-") {
			return os.ErrPermission
		}
		return os.Link(oldname, newname)
	}
	if _, err := execute(changed, Options{Root: root}, ops); err == nil {
		t.Fatal("execute() error = nil, want injected transaction failure")
	}
	for name, want := range before {
		got, readErr := os.ReadFile(filepath.Join(root, name))
		if readErr != nil || string(got) != string(want) {
			t.Fatalf("%s after rollback = %q, %v; want %q", name, got, readErr, want)
		}
	}
	if result, err := Execute(original, Options{Root: root, Mode: Check}); err != nil || result.Changed() {
		t.Fatalf("check after rollback = %#v, %v", result, err)
	}
	assertNoTransactionArtifacts(t, root)
}

func TestExecuteCASChecksUnchangedLockWhenOnlyOutputChanges(t *testing.T) {
	root := t.TempDir()
	plan, err := BuildPlan(File{
		Path:      "generated.txt",
		Content:   []byte("generated"),
		Ownership: Generated,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Execute(plan, Options{Root: root}); err != nil {
		t.Fatal(err)
	}
	outputPath := filepath.Join(root, "generated.txt")
	lockPath := filepath.Join(root, DefaultLockFile)
	if err := os.Remove(outputPath); err != nil {
		t.Fatal(err)
	}
	lockSnapshot, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	externalLock := append(append([]byte(nil), lockSnapshot...), ' ', '\n')
	ops := defaultFileTransactionOps
	ops.chmodHandle = func(file *os.File, filename string, mode os.FileMode) error {
		if err := file.Chmod(mode); err != nil {
			return err
		}
		if filename == outputPath {
			return os.WriteFile(lockPath, externalLock, 0o644)
		}
		return nil
	}

	if _, err := execute(plan, Options{Root: root}, ops); !errors.Is(err, ErrTransactionConflict) {
		t.Fatalf("execute() error = %v, want ErrTransactionConflict", err)
	}
	if _, statErr := os.Stat(outputPath); !os.IsNotExist(statErr) {
		t.Fatalf("output after lock conflict stat = %v, want missing", statErr)
	}
	gotLock, err := os.ReadFile(lockPath)
	if err != nil || string(gotLock) != string(externalLock) {
		t.Fatalf("lock after conflict = %q, %v; want external snapshot %q", gotLock, err, externalLock)
	}
	assertNoTransactionArtifacts(t, root)
}

func TestExecuteCASChecksUnchangedPlannedFiles(t *testing.T) {
	root := t.TempDir()
	original, err := BuildPlan(
		File{Path: "a.txt", Content: []byte("a-old"), Ownership: Generated},
		File{Path: "z.txt", Content: []byte("z-old"), Ownership: Generated},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Execute(original, Options{Root: root}); err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(root, DefaultLockFile)
	lockBefore, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	aPath := filepath.Join(root, "a.txt")
	zPath := filepath.Join(root, "z.txt")
	changed, err := BuildPlan(
		File{Path: "a.txt", Content: []byte("a-old"), Ownership: Generated},
		File{Path: "z.txt", Content: []byte("z-new"), Ownership: Generated},
	)
	if err != nil {
		t.Fatal(err)
	}
	ops := defaultFileTransactionOps
	changedA := false
	ops.chmodHandle = func(file *os.File, filename string, mode os.FileMode) error {
		if err := file.Chmod(mode); err != nil {
			return err
		}
		if filename == zPath && !changedA {
			changedA = true
			return os.WriteFile(aPath, []byte("a-concurrent"), 0o600)
		}
		return nil
	}

	if _, err := execute(changed, Options{Root: root}, ops); !errors.Is(err, ErrTransactionConflict) {
		t.Fatalf("execute() error = %v, want ErrTransactionConflict", err)
	}
	assertFileContent(t, aPath, "a-concurrent")
	assertFileContent(t, zPath, "z-old")
	lockAfter, err := os.ReadFile(lockPath)
	if err != nil || string(lockAfter) != string(lockBefore) {
		t.Fatalf("lock after rollback = %q, %v; want %q", lockAfter, err, lockBefore)
	}
	assertNoTransactionArtifacts(t, root)
}

func TestExecuteCommitsDespiteBackupCleanupWarning(t *testing.T) {
	root := t.TempDir()
	original, err := BuildPlan(File{Path: "generated.txt", Content: []byte("old"), Ownership: Generated})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Execute(original, Options{Root: root}); err != nil {
		t.Fatal(err)
	}
	changed, err := BuildPlan(File{Path: "generated.txt", Content: []byte("new"), Ownership: Generated})
	if err != nil {
		t.Fatal(err)
	}
	ops := defaultFileTransactionOps
	ops.remove = func(filename string) error {
		if strings.HasPrefix(filepath.Base(filename), ".kpro-transaction-backup-") {
			return os.ErrPermission
		}
		return os.Remove(filename)
	}
	result, err := execute(changed, Options{Root: root}, ops)
	if err != nil {
		t.Fatalf("execute() error = %v", err)
	}
	if len(result.CleanupWarnings) == 0 {
		t.Fatal("CleanupWarnings is empty")
	}
	if got, err := os.ReadFile(filepath.Join(root, "generated.txt")); err != nil || string(got) != "new" {
		t.Fatalf("generated output = %q, %v", got, err)
	}
	if check, err := Execute(changed, Options{Root: root, Mode: Check}); err != nil || check.Changed() {
		t.Fatalf("check after cleanup warning = %#v, %v", check, err)
	}
}
