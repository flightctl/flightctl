package auth

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/config"
)

func TestGetTlsConfig_emptyCACertLeavesRootCAsNil(t *testing.T) {
	t.Parallel()
	var cfg config.Config
	if err := json.Unmarshal([]byte(`{"auth":{}}`), &cfg); err != nil {
		t.Fatal(err)
	}
	tc := getTlsConfig(&cfg)
	if tc.RootCAs != nil {
		t.Fatal("expected RootCAs to be nil when caCert is empty")
	}
}

func TestGetTlsConfig_cACertSetsRootCAs(t *testing.T) {
	t.Parallel()
	var cfg config.Config
	if err := json.Unmarshal([]byte(`{"auth":{}}`), &cfg); err != nil {
		t.Fatal(err)
	}
	cfg.Auth.CACert = mustEncodeTestCAPEM(t)
	tc := getTlsConfig(&cfg)
	if tc.RootCAs == nil {
		t.Fatal("expected RootCAs when caCert is set")
	}
}

func mustEncodeTestCAPEM(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign,
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := pem.Encode(&buf, &pem.Block{Type: "CERTIFICATE", Bytes: der}); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}
