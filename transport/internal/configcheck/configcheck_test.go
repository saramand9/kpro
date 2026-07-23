package configcheck

import (
	"crypto/tls"
	"testing"
)

func TestListenAddress(t *testing.T) {
	valid := []string{":0", "localhost:8080", "localhost:http", "[::1]:443", "[fe80::1%lo0]:8443"}
	for _, address := range valid {
		if err := ListenAddress("listen address", address); err != nil {
			t.Errorf("ListenAddress(%q): %v", address, err)
		}
	}

	invalid := []string{"", "localhost", ":", ":-1", ":---", ":70000", ":999999999999999999999", "local host:80", ":bad!service"}
	for _, address := range invalid {
		if err := ListenAddress("listen address", address); err == nil {
			t.Errorf("ListenAddress(%q) accepted invalid address", address)
		}
	}
}

func TestHTTPPathRejectsNonCanonicalAndPatternPaths(t *testing.T) {
	for _, value := range []string{"/events", "/events/"} {
		if err := HTTPPath("path", value); err != nil {
			t.Errorf("HTTPPath(%q): %v", value, err)
		}
	}
	for _, value := range []string{"events", "//", "//events", "/./events", "/a/../events", "/{"} {
		if err := HTTPPath("path", value); err == nil {
			t.Errorf("HTTPPath(%q) accepted invalid path", value)
		}
	}
}

func TestTypedNil(t *testing.T) {
	type item struct{}
	var pointer *item
	if err := TypedNil("item", pointer); err == nil {
		t.Fatal("TypedNil accepted a typed-nil pointer")
	}
	if err := TypedNil("item", nil); err != nil {
		t.Fatalf("TypedNil rejected nil default: %v", err)
	}
	if err := TypedNil("item", &item{}); err != nil {
		t.Fatalf("TypedNil rejected non-nil pointer: %v", err)
	}
}

func TestTLSConfigRequiresCertificateSelection(t *testing.T) {
	if err := TLSConfig("test", &tls.Config{}); err == nil {
		t.Fatal("TLSConfig accepted a configuration without certificates or callbacks")
	}
	if err := TLSConfig("test", &tls.Config{Certificates: []tls.Certificate{{}}}); err == nil {
		t.Fatal("TLSConfig accepted an empty static certificate")
	}
	if err := TLSConfig("test", &tls.Config{GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
		return nil, nil
	}}); err != nil {
		t.Fatalf("TLSConfig rejected dynamic certificate selection: %v", err)
	}
}

func TestALPN(t *testing.T) {
	if err := ALPN("NextProtos", []string{"kpro"}); err != nil {
		t.Fatalf("ALPN rejected valid protocol: %v", err)
	}
	if err := ALPN("NextProtos", []string{""}); err == nil {
		t.Fatal("ALPN accepted an empty protocol")
	}
	if err := ALPN("NextProtos", []string{string(make([]byte, 256))}); err == nil {
		t.Fatal("ALPN accepted a protocol longer than 255 bytes")
	}
}
