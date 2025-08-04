package signer

import (
	"context"
	"crypto/x509"
	"fmt"
)

// SignAndEncodeCertificate verifies, then signs the request and returns the certificate in PEM format.
func SignAndEncodeCertificate(ctx context.Context, ca CA, request SignRequest) ([]byte, error) {
	cert, err := VerifyAndSign(ctx, ca, request)
	if err != nil {
		return nil, err
	}

	return EncodeCertificatePEM(cert)
}

func VerifyRequest(ctx context.Context, ca CA, request SignRequest) error {
	signer, err := getSigner(ca, request)
	if err != nil {
		return err
	}

	return signer.Verify(ctx, request)
}

func VerifyAndSign(ctx context.Context, ca CA, request SignRequest) (*x509.Certificate, error) {
	signer, err := getSigner(ca, request)
	if err != nil {
		return nil, err
	}

	if err := signer.Verify(ctx, request); err != nil {
		return nil, err
	}

	return signer.Sign(ctx, request)
}

// getSigner returns the signer for the request.
func getSigner(ca CA, request SignRequest) (Signer, error) {
	signer := ca.GetSigner(request.SignerName())
	if signer == nil {
		return nil, fmt.Errorf("signer %q not found", request.SignerName())
	}
	return signer, nil
}
