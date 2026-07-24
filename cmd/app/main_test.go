package main

import (
	"path/filepath"
	"testing"
)

func TestDefaultAppLogFileIsOutsideWorkingDirectory(t *testing.T) {
	path := defaultAppLogFile()
	if !filepath.IsAbs(path) {
		t.Fatalf("default log file = %q, want absolute user-cache or temp path", path)
	}
}
