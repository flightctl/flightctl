package lifecycle

import (
	"context"
	"fmt"

	fcrypto "github.com/flightctl/flightctl/internal/crypto"
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
	// local attestation public key hash
	lakPubKeyHash []byte
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
		return nil, err
	}
	tc.lak = lak
	hash, err := fcrypto.HashPublicKey(lak.PublicKey())
	if err != nil {
		return nil, err
	}
	tc.lakPubKeyHash = hash

	return &tc, nil
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

func (tc *TpmClient) TpmAttestationCollector(ctx context.Context) string {
	att, err := tc.tpm.GetAttestation(tc.currNonce, tc.lak)
	if err != nil {
		tc.log.Infof("Unable to get TPM attestation: %v", err)
		return ""
	}
	return att.String()
}

func (tc *TpmClient) UpdateNonce(nonce []byte) error {
	if len(nonce) < tpm.MinNonceLength {
		return fmt.Errorf("nonce does not meet minimum length of %d bytes", tpm.MinNonceLength)
	}
	tc.currNonce = nonce
	return nil
}
