package codegen

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPlanNormalizesAndRejectsUnsafeOrDuplicatePaths(t *testing.T) {
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
	for _, unsafe := range []string{"../escape", "/absolute", `C:\escape`, `\\server\share`, "a/../b", "."} {
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
