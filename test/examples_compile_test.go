// File: test/examples_compile_test.go
// Purpose: Exercises KPro protocol and transport behavior through integration tests.

package test

import (
	"os/exec"
	"testing"
)

func TestExamplesCompile(t *testing.T) {
	cmd := exec.Command("go", "test", "./examples/...")
	cmd.Dir = ".."
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("examples do not compile: %v\n%s", err, out)
	}
}
