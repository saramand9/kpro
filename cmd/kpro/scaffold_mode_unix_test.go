//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/saramand9/kpro/internal/codegen"
)

func TestWriteScaffoldFilesHonorsDeclaredModeUnderRestrictiveUmask(t *testing.T) {
	const (
		helperEnvironment = "KPRO_SCAFFOLD_UMASK_HELPER"
		rootEnvironment   = "KPRO_SCAFFOLD_UMASK_ROOT"
	)
	if os.Getenv(helperEnvironment) == "1" {
		previous := syscall.Umask(0o077)
		defer syscall.Umask(previous)
		root := os.Getenv(rootEnvironment)
		if err := writeScaffoldFiles(root, []codegen.File{{
			Path:    "scripts/run.sh",
			Content: []byte("#!/bin/sh\n"),
			Mode:    0o755,
		}}); err != nil {
			t.Fatal(err)
		}
		return
	}

	root := t.TempDir()
	command := exec.Command(os.Args[0], "-test.run=^TestWriteScaffoldFilesHonorsDeclaredModeUnderRestrictiveUmask$")
	command.Env = append(os.Environ(), helperEnvironment+"=1", rootEnvironment+"="+root)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("umask helper: %v\n%s", err, output)
	}
	info, err := os.Stat(filepath.Join(root, "scripts", "run.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o755 {
		t.Fatalf("scaffold file mode = %#o, want 0755", got)
	}
}
