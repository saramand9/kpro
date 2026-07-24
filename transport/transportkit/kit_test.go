package transportkit

import (
	"bytes"
	"testing"
)

func TestPublicHelpersMatchBuiltInContracts(t *testing.T) {
	if err := ListenAddress("Addr", "127.0.0.1:0"); err != nil {
		t.Fatalf("ListenAddress: %v", err)
	}
	if err := HTTPPath("Path", "/events"); err != nil {
		t.Fatalf("HTTPPath: %v", err)
	}
	var output bytes.Buffer
	if err := WriteFull(&output, []byte("kpro")); err != nil {
		t.Fatalf("WriteFull: %v", err)
	}
	if output.String() != "kpro" {
		t.Fatalf("output = %q", output.String())
	}
}
