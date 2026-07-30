package messagegen

import (
	"os"
	"os/exec"
	"testing"
)

func runGoTest(t *testing.T, root string) {
	t.Helper()
	goTool, err := exec.LookPath("go")
	if err != nil {
		t.Skipf("go tool is unavailable: %v", err)
	}
	command := exec.Command(goTool, "test", "-mod=mod", "./...", "-count=1")
	command.Dir = root
	command.Env = append(os.Environ(), "GOWORK=off")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("generated module does not compile or pass behavior tests: %v\n%s", err, output)
	}
}
