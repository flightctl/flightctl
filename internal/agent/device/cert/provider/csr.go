package provider

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/x509"
	"encoding/base32"
	"encoding/pem"
	"fmt"
	"strings"
	"time"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/client"
	fccrypto "github.com/flightctl/flightctl/pkg/crypto"
)

type CSRProvisioner struct {
	deviceName       string
	managementClient client.Management

	CommonName string
	SignerName string

	csrName    string
	privateKey crypto.Signer
}

func NewCSRProvisioner(deviceName string, managementClient client.Management, commonName, signerName string) (*CSRProvisioner, error) {
	return &CSRProvisioner{
		deviceName:       deviceName,
		managementClient: managementClient,
		CommonName:       commonName,
		SignerName:       signerName,
	}, nil
}

// generateSuffix returns a lowercase alphanumeric string like Kubernetes' generateName suffixes
func generateSuffix(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	// Use base32, remove padding, lowercase
	return strings.ToLower(base32.HexEncoding.WithPadding(base32.NoPadding).EncodeToString(b))[:n], nil
}

func (p *CSRProvisioner) Provision(ctx context.Context) error {
	suffix, err := generateSuffix(4)
	if err != nil {
		return fmt.Errorf("generate suffix: %w", err)
	}

	usename := fmt.Sprintf("%s-%s-%s", p.CommonName, suffix, p.deviceName)

	key, csr, err := generateKeyAndCSR(usename)
	if err != nil {
		return fmt.Errorf("generate csr: %w", err)
	}

	p.privateKey = key

	p.csrName = usename

	req := v1alpha1.CertificateSigningRequest{
		ApiVersion: "v1alpha1",
		Kind:       "CertificateSigningRequest",
		Metadata: v1alpha1.ObjectMeta{
			Name: &p.csrName,
		},
		Spec: v1alpha1.CertificateSigningRequestSpec{
			Request:    csr,
			SignerName: p.SignerName,
			Usages:     &[]string{"clientAuth", "CA:false"},
		},
	}

	resp, err := p.managementClient.CreateCertificateSigningRequest(ctx, req)
	if err != nil {
		return fmt.Errorf("submit csr: %w", err)
	}

	//TODO: NIL RESP

	fmt.Printf("%v", resp)

	return nil
}

func generateKeyAndCSR(commonName string) (crypto.Signer, []byte, error) {
	_, priv, err := fccrypto.NewKeyPair()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to generate key pair: %w", err)
	}

	signer, ok := priv.(crypto.Signer)
	if !ok {
		return nil, nil, fmt.Errorf("expected crypto.Signer, got %T", priv)
	}

	csr, err := fccrypto.MakeCSR(signer, commonName)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create CSR: %w", err)
	}

	return signer, csr, nil
}

func (p *CSRProvisioner) Result(ctx context.Context, w StorageWriter) error {
	if p.csrName == "" {
		return fmt.Errorf("no CSR name recorded")
	}
	if p.privateKey == nil {
		return fmt.Errorf("no private key generated")
	}

	// Poll for up to 30 seconds with 2s intervals
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	timeout := time.After(30 * time.Hour)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timeout:
			return fmt.Errorf("timed out waiting for CSR approval: %s", p.csrName)
		case <-ticker.C:
			resp, _ := p.managementClient.GetCertificateSigningRequest(ctx, p.csrName)
			if v1alpha1.IsStatusConditionTrue(resp.Status.Conditions, v1alpha1.CertificateSigningRequestDenied) {
				panic("CertificateSigningRequestDenied")
			}

			if !v1alpha1.IsStatusConditionTrue(resp.Status.Conditions, v1alpha1.CertificateSigningRequestApproved) {
				continue
			}

			if resp.Status.Certificate == nil {
				if v1alpha1.IsStatusConditionTrue(resp.Status.Conditions, v1alpha1.CertificateSigningRequestFailed) {
					panic("CertificateSigningRequestFailed")
				} else {
					continue
				}
			}

			certPEM := *resp.Status.Certificate
			keyPEM, err := encodePrivateKey(p.privateKey)
			if err != nil {
				return fmt.Errorf("encode private key: %w", err)
			}

			if err := w.Write(certPEM, keyPEM); err != nil {
				return fmt.Errorf("write to storage: %w", err)
			}

			return nil
		}
	}
}

func encodePrivateKey(key crypto.Signer) ([]byte, error) {
	privKey, ok := key.(*ecdsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("unsupported private key type")
	}
	der, err := x509.MarshalECPrivateKey(privKey)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{
		Type:  "EC PRIVATE KEY",
		Bytes: der,
	}), nil
}
