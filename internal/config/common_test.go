package config

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

func initTmpCerts(t *testing.T, certStore string, fileName string) {
	certFile := filepath.Join(certStore, fileName+".crt")
	certKey := filepath.Join(certStore, fileName+".key")

	// Created files must be valid - generate a valid ECDSA key pair
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic(err)
	}

	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			CommonName: "test-cert",
		},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, &template, &template, &privateKey.PublicKey, privateKey)
	if err != nil {
		panic(err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: certDER,
	})

	keyBytes, err := x509.MarshalECPrivateKey(privateKey)
	if err != nil {
		panic(err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "EC PRIVATE KEY",
		Bytes: keyBytes,
	})

	err = os.WriteFile(certFile, certPEM, 0600)
	if err != nil {
		t.Fatalf("failed to write certificate file: %v", err)
	}
	err = os.WriteFile(certKey, keyPEM, 0600)
	if err != nil {
		t.Fatalf("failed to write key file: %v", err)
	}
}

func TestServerCertificates(t *testing.T) {
	testCases := []struct {
		name        string
		setup       func(t *testing.T, cfg *Config)
		expectedErr error
	}{
		{
			name: "provided cert files do not exist",
			setup: func(t *testing.T, cfg *Config) {
				initTmpCerts(t, cfg.Service.CertStore, "provided")
				cfg.Service.SrvCertFile = filepath.Join(cfg.Service.CertStore, "does_not_exist.crt")
				cfg.Service.SrvKeyFile = filepath.Join(cfg.Service.CertStore, "does_not_exist.key")
			},
			expectedErr: ErrServerCertsNotFound,
		},
		{
			name: "provided cert key does not exist",
			setup: func(t *testing.T, cfg *Config) {
				initTmpCerts(t, cfg.Service.CertStore, "provided")
				cfg.Service.SrvKeyFile = filepath.Join(cfg.Service.CertStore, "does_not_exist.key")
			},
			expectedErr: ErrServerCertsNotFound,
		},
		{
			name: "provided cert file is invalid",
			setup: func(t *testing.T, cfg *Config) {
				initTmpCerts(t, cfg.Service.CertStore, "provided")
				err := os.WriteFile(filepath.Join(cfg.Service.CertStore, "invalid.crt"), []byte("invalid"), 0600)
				if err != nil {
					t.Fatalf("failed to write certificate file: %v", err)
				}
				cfg.Service.SrvCertFile = filepath.Join(cfg.Service.CertStore, "invalid.crt")
				cfg.Service.SrvKeyFile = filepath.Join(cfg.Service.CertStore, "provided.key")
			},
			expectedErr: ErrInvalidServerCerts,
		},
		{
			name: "provided certs are valid",
			setup: func(t *testing.T, cfg *Config) {
				initTmpCerts(t, cfg.Service.CertStore, "provided")
				cfg.Service.SrvCertFile = filepath.Join(cfg.Service.CertStore, "provided.crt")
				cfg.Service.SrvKeyFile = filepath.Join(cfg.Service.CertStore, "provided.key")
			},
			expectedErr: nil,
		},
		{
			name: "default certs are invalid",
			setup: func(t *testing.T, cfg *Config) {
				initTmpCerts(t, cfg.Service.CertStore, cfg.Service.ServerCertName)
				err := os.WriteFile(filepath.Join(cfg.Service.CertStore, cfg.Service.ServerCertName+".crt"), []byte("invalid"), 0600)
				if err != nil {
					t.Fatalf("failed to write certificate file: %v", err)
				}
			},
			expectedErr: ErrInvalidServerCerts,
		},
		{
			name: "default certs are valid",
			setup: func(t *testing.T, cfg *Config) {
				initTmpCerts(t, cfg.Service.CertStore, cfg.Service.ServerCertName)
			},
			expectedErr: nil,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			log := logrus.New()

			cfg := NewDefault()
			// Create a unique temporary directory for the cert store
			// that is pruned between each test case run
			cfg.Service.CertStore = t.TempDir()
			tc.setup(t, cfg)

			serverCerts, err := LoadServerCertificates(cfg, log)
			if tc.expectedErr != nil {
				require.Error(t, err)
				require.ErrorIs(t, err, tc.expectedErr)
			} else {
				require.NoError(t, err)
				require.NotNil(t, serverCerts)
			}
		})
	}
}
