package lifecycle

import (
	"bytes"
	"context"
	"crypto"
	"fmt"

	"github.com/flightctl/flightctl/internal/tpm"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/google/go-tpm-tools/client"
)

type TpmClient struct {
	// path where the tpm will be found, if it exists
	tpmSysPath string
	// handle to the open TPM
	tpm *tpm.TPM
	// local attestation public key
	lak *client.Key
	// meant to be updated for each attestation
	currNonce []byte
	log       *log.PrefixLogger
}

func NewTpmClient(log *log.PrefixLogger) (*TpmClient, error) {
	tc := TpmClient{
		tpmSysPath: tpm.TpmSystemPath,
		log:        log,
	}

	if err := tc.OpenTPM(); err != nil {
		return nil, err
	}

	lak, err := tc.tpm.CreateLAK()
	if err != nil {
		_ = tc.CloseTPM()
		return nil, err
	}
	tc.lak = lak

	return &tc, nil
}

func (tc *TpmClient) GetPath() string {
	return tc.tpmSysPath
}

func (tc *TpmClient) GetLocalAttestationPubKey() crypto.PublicKey {
	return tc.lak.PublicKey()
}

func (tc *TpmClient) GetSigner() (crypto.Signer, error) {
	return tc.lak.GetSigner()
}

func (tc *TpmClient) OpenTPM() error {
	if err := tpm.ValidateTpmVersion2(); err != nil {
		return err
	}
	tpm, err := tpm.OpenTPM(tc.tpmSysPath)
	if err != nil {
		return err
	}
	tc.tpm = tpm
	return nil
}

func (tc *TpmClient) CloseTPM() error {
	if tc.tpm != nil {
		err := tc.tpm.Close()
		tc.tpm = nil
		return err
	}
	return nil
}

func (tc *TpmClient) TpmVendorInfoCollector(ctx context.Context) string {
	if tc == nil {
		tc.log.Errorf("cannot get TPM vendor info: nil receiver TpmClient")
		return ""
	}
	if tc.tpm == nil {
		tc.log.Errorf("cannot get TPM vendor info: TPM is unavailable in TpmClient")
		return ""
	}
	info, err := tc.tpm.GetTpmVendorInfo()
	if err != nil {
		tc.log.Errorf("Unable to get TPM vendor info: %v", err)
		return "Unavailable"
	}
	return string(info)
}

func (tc *TpmClient) TpmAttestationCollector(ctx context.Context) string {
	if tc == nil {
		tc.log.Errorf("cannot get TPM attestation: nil receiver TpmClient")
		return ""
	}
	if tc.tpm == nil {
		tc.log.Errorf("cannot get TPM attestation: TPM is unavailable in TpmClient")
		return ""
	}

	att, err := tc.tpm.GetAttestation(tc.currNonce, tc.lak)
	if err != nil {
		tc.log.Errorf("Unable to get TPM attestation: %v", err)
		return "Unavailable"
	}
	return att.String()
}

func (tc *TpmClient) UpdateNonce(nonce []byte) error {
	if len(nonce) < tpm.MinNonceLength {
		return fmt.Errorf("nonce does not meet minimum length of %d bytes", tpm.MinNonceLength)
	}
	if bytes.Equal(tc.currNonce, nonce) {
		return fmt.Errorf("cannot update nonce to same value as current nonce")
	}

	tc.currNonce = nonce
	return nil
}
