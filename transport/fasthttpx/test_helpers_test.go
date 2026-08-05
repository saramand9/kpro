package fasthttpx

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/valyala/fasthttp"
)

func executeTestRequest(
	t testing.TB,
	endpoint *Endpoint,
	method string,
	target string,
	body []byte,
	headers map[string]string,
) *fasthttp.Response {
	t.Helper()
	var request fasthttp.Request
	request.Header.SetMethod(method)
	request.SetRequestURI(target)
	request.Header.SetHost("example.test")
	request.SetBody(body)
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	var native fasthttp.RequestCtx
	native.Init(&request, &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 43210}, nil)
	endpoint.ServeFastHTTP(&native)
	response := new(fasthttp.Response)
	native.Response.CopyTo(response)
	return response
}

func responseBody(response *fasthttp.Response) string {
	if response == nil {
		return ""
	}
	return string(response.Body())
}

func writeTestSelfSignedCert(t *testing.T) (string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate test key: %v", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatalf("generate test serial: %v", err)
	}
	template := x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	certificate, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create test certificate: %v", err)
	}
	privateKey, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal test key: %v", err)
	}

	directory := t.TempDir()
	certFile := filepath.Join(directory, "cert.pem")
	keyFile := filepath.Join(directory, "key.pem")
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate}), 0600); err != nil {
		t.Fatalf("write test certificate: %v", err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateKey}), 0600); err != nil {
		t.Fatalf("write test key: %v", err)
	}
	return certFile, keyFile
}
