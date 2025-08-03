package signer

import (
	"crypto/x509"
	"fmt"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	fccrypto "github.com/flightctl/flightctl/pkg/crypto"
)

type Request struct {
	API  *api.CertificateSigningRequest
	x509 x509.CertificateRequest
}

// X509 returns a copy of the parsed x509 certificate request.
func (r *Request) X509() x509.CertificateRequest {
	return r.x509
}

// NewRequest creates a Request by parsing the CSR from the API object.
func NewRequest(apiCSR *api.CertificateSigningRequest) (*Request, error) {
	x509CSR, err := fccrypto.ParseCSR(apiCSR.Spec.Request)
	if err != nil {
		return nil, fmt.Errorf("invalid CSR: %v", err)
	}

	return &Request{
		API:  apiCSR,
		x509: *x509CSR,
	}, nil
}
