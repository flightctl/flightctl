package tpm

import (
	"crypto/rand"
	"io"
	"testing"

	"github.com/google/go-tpm-tools/client"
	"github.com/google/go-tpm/legacy/tpm2"
)

// These unit tests all use the tpm simulator from go-tpm-tools.

type TestFixture struct {
	tpm *TPM
}

func setupTestFixture(t *testing.T) *TestFixture {
	t.Helper()

	tpm, err := OpenTPMSimulator()
	if err != nil {
		t.Errorf("unable to open tpm simulator")
	}

	return &TestFixture{tpm: tpm}
}

func setupTestData (t *testing.T) (*TPM, *client.Key, []byte, *tpm2.PCRSelection) {
        t.Helper()

	f := setupTestFixture(t)

	key, err := f.tpm.CreateLAK()
	if err != nil {
		t.Errorf("unable to create local attestation key")
	}

	nonce := make([]byte, 8)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		t.Errorf("failed to create nonce: %v", err)
	}

	selection := client.FullPcrSel(tpm2.AlgSHA256)

	return f.tpm, key, nonce, &selection
}

func TestLAK(t *testing.T) {
	tpm, key, _, _ := setupTestData(t)
	defer tpm.Close()
	defer key.Close()

	// this template is based on that used for AK ECC key creation in go-tpm-tools, see:
	// https://github.com/google/go-tpm-tools/blob/3e063ade7f302972d7b893ca080a75efa3db5506/client/template.go#L108
	//
	// for more template options, see https://pkg.go.dev/github.com/google/go-tpm/legacy/tpm2#Public
	params := tpm2.ECCParams{
		Symmetric: nil,
		CurveID:   tpm2.CurveNISTP256,
		Point: tpm2.ECPoint{
			XRaw: make([]byte, 32),
			YRaw: make([]byte, 32),
		},
	}
	params.Sign = &tpm2.SigScheme{
		Alg: tpm2.AlgECDSA,
		Hash: tpm2.AlgSHA256,
	}
	template := tpm2.Public {
		Type: tpm2.AlgECC,
		NameAlg: tpm2.AlgSHA256,
		Attributes: tpm2.FlagSignerDefault,
		ECCParameters: &params,
	}

	pub := key.PublicArea()
	if !pub.MatchesTemplate(template) {
		t.Errorf("local attestation key does not match template")
	}
}

func TestGetQuote(t *testing.T) {
	tpm, key, nonce, selection := setupTestData(t)
	defer tpm.Close()
	defer key.Close()

	q, err := tpm.GetQuote(nonce, key, selection)
	if err != nil {
		t.Errorf("failed to get quote: %v", err)
	}

	t.Logf("quote: %s", q)
}

func TestGetAttestation(t *testing.T) {
	tpm, key, nonce, _ := setupTestData(t)
	defer tpm.Close()
	defer key.Close()

	_, err := tpm.GetAttestation(nonce, key)
	if err != nil {
		t.Errorf("failed to get attestation: %v", err)
	}
}

func TestGetPCRValues(t *testing.T) {
	tpm, key, _, _ := setupTestData(t)
	defer tpm.Close()
	defer key.Close()

	measurements := make(map[string]string)

	err := tpm.GetPCRValues(measurements)
	if err != nil {
		t.Errorf("failed to read pcr values: %v", err)
	}
}
