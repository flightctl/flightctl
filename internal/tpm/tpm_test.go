//go:build amd64 || arm64

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
	"github.com/google/go-tpm-tools/simulator"
	legacy "github.com/google/go-tpm/legacy/tpm2"
	"github.com/google/go-tpm/tpm2"
	"github.com/stretchr/testify/require"
)

type TestFixture struct {
	tpm *TPM
}

type TestData struct {
	tpm    *TPM
	srk    *tpm2.NamedHandle
	ldevid *tpm2.NamedHandle
	lak    *client.Key
	nonce  []byte
	pcrSel *legacy.PCRSelection
}

func openTPMSimulator(t *testing.T) (*TPM, error) {
	t.Helper()
	require := require.New(t)

	simulator, err := simulator.Get()
	require.NoError(err)

	return &TPM{channel: simulator}, nil
}

func setupTestFixture(t *testing.T) (*TestFixture, error) {
	t.Helper()

	tpm, err := openTPMSimulator(t)
	if err != nil {
		return nil, fmt.Errorf("unable to open tpm simulator")
	}

	return &TestFixture{tpm: tpm}, nil
}

func setupTestData(t *testing.T) TestData {
	t.Helper()
	require := require.New(t)

	f, err := setupTestFixture(t)
	require.NoError(err)

	srk, err := f.tpm.GenerateSRKPrimary()
	require.NoError(err)

	lak, err := f.tpm.CreateLAK()
	require.NoError(err)

	ldevid, err := f.tpm.CreateLDevID(*srk)
	require.NoError(err)

	nonce := make([]byte, 8)
	_, err = io.ReadFull(rand.Reader, nonce)
	require.NoError(err)

	selection := client.FullPcrSel(legacy.AlgSHA256)

	data := TestData{
		tpm:    f.tpm,
		srk:    srk,
		ldevid: ldevid,
		lak:    lak,
		nonce:  nonce,
		pcrSel: &selection,
	}

	return data
}

func TestLAK(t *testing.T) {
	data := setupTestData(t)
	defer func() {
		data.tpm.Close()
		data.lak.Close()
	}()

	// This template is based on that used for AK ECC key creation in go-tpm-tools, see:
	// https://github.com/google/go-tpm-tools/blob/3e063ade7f302972d7b893ca080a75efa3db5506/client/template.go#L108
	//
	// For more template options, see https://pkg.go.dev/github.com/google/go-tpm/legacy/tpm2#Public
	params := legacy.ECCParams{
		Symmetric: nil,
		CurveID:   legacy.CurveNISTP256,
		Point: legacy.ECPoint{
			XRaw: make([]byte, 32),
			YRaw: make([]byte, 32),
		},
	}
	params.Sign = &legacy.SigScheme{
		Alg:  legacy.AlgECDSA,
		Hash: legacy.AlgSHA256,
	}
	template := legacy.Public{
		Type:          legacy.AlgECC,
		NameAlg:       legacy.AlgSHA256,
		Attributes:    legacy.FlagSignerDefault,
		ECCParameters: &params,
	}

	pub := data.lak.PublicArea()
	if !pub.MatchesTemplate(template) {
		t.Errorf("local attestation key does not match template")
	}
}

func TestGetQuote(t *testing.T) {
	require := require.New(t)
	data := setupTestData(t)
	defer func() {
		data.tpm.Close()
		data.lak.Close()
	}()

	_, err := data.tpm.GetQuote(data.nonce, data.lak, data.pcrSel)
	require.NoError(err)
}

func TestGetAttestation(t *testing.T) {
	// Skip this test when running in a CI environment where the event log file is not available
	_, err := os.ReadFile("/sys/kernel/security/tpm0/binary_bios_measurements")
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, fs.ErrPermission) {
		t.Skip("Skipping test: TCG Event Log not available")
	}

	require := require.New(t)
	data := setupTestData(t)
	defer func() {
		data.tpm.Close()
		data.lak.Close()
	}()

	_, err = data.tpm.GetAttestation(data.nonce, data.lak)
	require.NoError(err)
}

func TestGetPCRValues(t *testing.T) {
	require := require.New(t)
	data := setupTestData(t)
	defer func() {
		data.tpm.Close()
		data.lak.Close()
	}()

	measurements := make(map[string]string)

	err := data.tpm.GetPCRValues(measurements)
	require.NoError(err)
}
