package examplecert

import (
	"crypto/tls"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestClientTrust(t *testing.T) {
	serverTLS, err := SelfSignedServer()
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	server.TLS = serverTLS
	server.StartTLS()
	defer server.Close()
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: serverTLS.Certificates[0].Certificate[0]}), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name      string
		ca        string
		insecure  bool
		wantError bool
	}{
		{name: "system trust rejects self signed", wantError: true},
		{name: "explicit CA", ca: caFile},
		{name: "explicit local insecure opt in", insecure: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config, err := Client(tc.ca, tc.insecure)
			if err != nil {
				t.Fatal(err)
			}
			if config.MinVersion != tls.VersionTLS12 || config.InsecureSkipVerify != tc.insecure {
				t.Fatalf("unexpected TLS policy: %+v", config)
			}
			transport := &http.Transport{TLSClientConfig: config}
			defer transport.CloseIdleConnections()
			response, err := (&http.Client{Transport: transport}).Get(server.URL)
			if (err != nil) != tc.wantError {
				t.Fatalf("TLS request error = %v, want error %v", err, tc.wantError)
			}
			if response != nil {
				_ = response.Body.Close()
			}
		})
	}
}

func TestClientRejectsInvalidCA(t *testing.T) {
	file := filepath.Join(t.TempDir(), "invalid.pem")
	if err := os.WriteFile(file, []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Client(file, false); err == nil {
		t.Fatal("invalid CA accepted")
	}
}
