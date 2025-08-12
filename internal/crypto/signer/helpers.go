package signer

import (
	"context"
	"crypto/x509"
	"fmt"

	"github.com/flightctl/flightctl/pkg/crypto"
)

// Verify verifies the request using the requested signer.
func Verify(ctx context.Context, ca CA, req SignRequest) error {
	signer, err := signerFor(ca, req)
	if err != nil {
		return err
	}
	return signer.Verify(ctx, req)
}

// Sign signs the request using the requested signer (without verification) and returns the signed certificate.
func Sign(ctx context.Context, ca CA, req SignRequest) (*x509.Certificate, error) {
	signer, err := signerFor(ca, req)
	if err != nil {
		return nil, err
	}
	return signer.Sign(ctx, req)
}

// SignVerified verifies the request and then signs it, returning the signed certificate.
func SignVerified(ctx context.Context, ca CA, req SignRequest) (*x509.Certificate, error) {
	signer, err := signerFor(ca, req)
	if err != nil {
		return nil, err
	}
	if err := signer.Verify(ctx, req); err != nil {
		return nil, err
	}
	return signer.Sign(ctx, req)
}

// SignAsPEM signs the request and returns the signed certificate in PEM format.
func SignAsPEM(ctx context.Context, ca CA, req SignRequest) ([]byte, error) {
	cert, err := Sign(ctx, ca, req)
	if err != nil {
		return nil, err
	}
	return crypto.EncodeCertificatePEM(cert)
}

// SignVerifiedAsPEM verifies, signs, and returns the signed certificate in PEM format.
func SignVerifiedAsPEM(ctx context.Context, ca CA, req SignRequest) ([]byte, error) {
	cert, err := SignVerified(ctx, ca, req)
	if err != nil {
		return nil, err
	}
	return crypto.EncodeCertificatePEM(cert)
}

// signerFor retrieves the signer from the CA based on the request's signer name.
func signerFor(ca CA, req SignRequest) (Signer, error) {
	name := req.SignerName()
	signer := ca.GetSigner(name)
	if signer == nil {
		return nil, fmt.Errorf("signer %q not found", name)
	}
	return signer, nil
}
