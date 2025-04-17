package tpm

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"testing"

	"github.com/google/go-tpm-tools/client"
	"github.com/google/go-tpm/legacy/tpm2"
	"github.com/stretchr/testify/require"
)

// These unit tests all use the tpm simulator from go-tpm-tools.

type TestFixture struct {
	tpm *TPM
}

func setupTestFixture(t *testing.T) (*TestFixture, error) {
	t.Helper()

	tpm, err := OpenTPMSimulator()
	if err != nil {
		return nil, fmt.Errorf("unable to open tpm simulator")
	}

	return &TestFixture{tpm: tpm}, nil
}

func setupTestData(t *testing.T) (*TPM, *client.Key, []byte, *tpm2.PCRSelection) {
	t.Helper()
	require := require.New(t)

	f, err := setupTestFixture(t)
	require.NoError(err)

	key, err := f.tpm.CreateLAK()
	require.NoError(err)

	nonce := make([]byte, 8)
	_, err = io.ReadFull(rand.Reader, nonce)
	require.NoError(err)

	selection := client.FullPcrSel(tpm2.AlgSHA256)

	return f.tpm, key, nonce, &selection
}

func TestLAK(t *testing.T) {
	tpm, key, _, _ := setupTestData(t)
	defer func() {
		tpm.Close()
		key.Close()
	}()

	// This template is based on that used for AK ECC key creation in go-tpm-tools, see:
	// https://github.com/google/go-tpm-tools/blob/3e063ade7f302972d7b893ca080a75efa3db5506/client/template.go#L108
	//
	// For more template options, see https://pkg.go.dev/github.com/google/go-tpm/legacy/tpm2#Public
	params := tpm2.ECCParams{
		Symmetric: nil,
		CurveID:   tpm2.CurveNISTP256,
		Point: tpm2.ECPoint{
			XRaw: make([]byte, 32),
			YRaw: make([]byte, 32),
		},
	}
	params.Sign = &tpm2.SigScheme{
		Alg:  tpm2.AlgECDSA,
		Hash: tpm2.AlgSHA256,
	}
	template := tpm2.Public{
		Type:          tpm2.AlgECC,
		NameAlg:       tpm2.AlgSHA256,
		Attributes:    tpm2.FlagSignerDefault,
		ECCParameters: &params,
	}

	pub := key.PublicArea()
	if !pub.MatchesTemplate(template) {
		t.Errorf("local attestation key does not match template")
	}
}

func TestGetQuote(t *testing.T) {
	require := require.New(t)
	tpm, key, nonce, selection := setupTestData(t)
	defer func() {
		tpm.Close()
		key.Close()
	}()

	_, err := tpm.GetQuote(nonce, key, selection)
	require.NoError(err)
}

func TestGetAttestation(t *testing.T) {
	// Skip this test when running in a CI environment where the event log file is not available
	_, err := os.ReadFile("/sys/kernel/security/tpm0/binary_bios_measurements")
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, fs.ErrPermission) {
		t.Skip("Skipping test: TCG Event Log not available")
	}

	require := require.New(t)
	tpm, key, nonce, _ := setupTestData(t)
	defer func() {
		tpm.Close()
		key.Close()
	}()

	_, err = tpm.GetAttestation(nonce, key)
	require.NoError(err)
}

func TestGetPCRValues(t *testing.T) {
	require := require.New(t)
	tpm, key, _, _ := setupTestData(t)
	defer func() {
		tpm.Close()
		key.Close()
	}()

	measurements := make(map[string]string)

	err := tpm.GetPCRValues(measurements)
	require.NoError(err)
}
