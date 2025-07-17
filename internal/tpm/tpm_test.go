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

	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/google/go-tpm-tools/client"
	"github.com/google/go-tpm-tools/simulator"
	legacy "github.com/google/go-tpm/legacy/tpm2"
	"github.com/google/go-tpm/tpm2"
	"github.com/google/go-tpm/tpm2/transport"
	"github.com/stretchr/testify/require"
)

type TestFixture struct {
	tpm *TPM
}

type TestData struct {
	tpm              *TPM
	srk              *tpm2.NamedHandle
	ldevid           *tpm2.NamedHandle
	lak              *client.Key
	nonce            []byte
	pcrSel           *legacy.PCRSelection
	persistentHandle *tpm2.TPMHandle
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

func setupTestData(t *testing.T) (TestData, func()) {
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

	// Use a test handle
	handle := PersistentHandleMin + 1

	data := TestData{
		tpm:              f.tpm,
		srk:              srk,
		ldevid:           ldevid,
		lak:              lak,
		nonce:            nonce,
		pcrSel:           &selection,
		persistentHandle: &handle,
	}

	cleanup := func() {
		data.tpm.Close()
		data.lak.Close()
	}

	return data, cleanup
}

func createTestReadWriter(t *testing.T) fileio.ReadWriter {
	t.Helper()
	tempDir := t.TempDir()
	return fileio.NewReadWriter(fileio.WithTestRootDir(tempDir))
}

func TestLAK(t *testing.T) {
	data, cleanup := setupTestData(t)
	defer cleanup()

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
	data, cleanup := setupTestData(t)
	defer cleanup()

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
	data, cleanup := setupTestData(t)
	defer cleanup()

	_, err = data.tpm.GetAttestation(data.nonce, data.lak)
	require.NoError(err)
}

func TestReadPCRValues(t *testing.T) {
	require := require.New(t)
	data, cleanup := setupTestData(t)
	defer cleanup()

	measurements := make(map[string]string)

	err := data.tpm.ReadPCRValues(measurements)
	require.NoError(err)
}

func TestPersistLDevID(t *testing.T) {
	require := require.New(t)
	data, cleanup := setupTestData(t)
	defer cleanup()

	err := data.tpm.persistLDevID(data.ldevid, *data.persistentHandle)
	require.NoError(err)

	readPublicCmd := tpm2.ReadPublic{
		ObjectHandle: *data.persistentHandle,
	}
	transportTPM := transport.FromReadWriter(data.tpm.channel)
	_, err = readPublicCmd.Execute(transportTPM)
	require.NoError(err)
}

func TestPersistLDevIDNil(t *testing.T) {
	require := require.New(t)
	data, cleanup := setupTestData(t)
	defer cleanup()

	err := data.tpm.persistLDevID(nil, *data.persistentHandle)
	require.Error(err)
	require.Contains(err.Error(), "cannot persist nil LDevID handle")
}

func TestLoadLDevIDFromPersistentHandle(t *testing.T) {
	require := require.New(t)
	data, cleanup := setupTestData(t)
	defer cleanup()

	err := data.tpm.persistLDevID(data.ldevid, *data.persistentHandle)
	require.NoError(err)

	loadedLDevID, err := data.tpm.loadLDevIDFromPersistentHandle(*data.persistentHandle)
	require.NoError(err)
	require.NotNil(loadedLDevID)
	require.Equal(*data.persistentHandle, loadedLDevID.Handle)
}

func TestLoadLDevIDErrors(t *testing.T) {
	data, cleanup := setupTestData(t)
	defer cleanup()

	tests := []struct {
		name                  string
		testFunc              func() error
		expectedErrorContains string
	}{
		{
			name: "loadLDevIDFromPersistentHandle with non-existent handle",
			testFunc: func() error {
				_, err := data.tpm.loadLDevIDFromPersistentHandle(*data.persistentHandle)
				return err
			},
			expectedErrorContains: "reading public area from persistent handle",
		},
		{
			name: "loadLDevIDFromHandle with non-existent handle",
			testFunc: func() error {
				customHandle := tpm2.TPMHandle(0x81000099)
				_, err := data.tpm.loadLDevIDFromHandle(customHandle)
				return err
			},
			expectedErrorContains: "reading public area from handle",
		},
		{
			name: "loadLDevIDFromBlob with invalid blob",
			testFunc: func() error {
				invalidPublic := tpm2.New2B(tpm2.TPMTPublic{
					Type:    tpm2.TPMAlgRSA,
					NameAlg: tpm2.TPMAlgSHA256,
				})
				invalidPrivate := tpm2.TPM2BPrivate{
					Buffer: []byte{0x00, 0x01, 0x02},
				}
				_, err := data.tpm.loadLDevIDFromBlob(invalidPublic, invalidPrivate)
				return err
			},
			expectedErrorContains: "error loading ldevid key",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.testFunc()
			require.Error(t, err)
			require.Contains(t, err.Error(), tt.expectedErrorContains)
		})
	}
}

func TestLoadLDevIDFromHandle(t *testing.T) {
	require := require.New(t)
	data, cleanup := setupTestData(t)
	defer cleanup()

	err := data.tpm.persistLDevID(data.ldevid, *data.persistentHandle)
	require.NoError(err)

	loadedLDevID, err := data.tpm.loadLDevIDFromHandle(*data.persistentHandle)
	require.NoError(err)
	require.NotNil(loadedLDevID)
	require.Equal(*data.persistentHandle, loadedLDevID.Handle)
}

func TestEnsureLDevIDPersistent(t *testing.T) {
	require := require.New(t)
	data, cleanup := setupTestData(t)
	defer cleanup()

	loadedLDevID, err := data.tpm.ensureLDevIDPersistent(*data.srk, *data.persistentHandle)
	require.NoError(err)
	require.NotNil(loadedLDevID)
	require.Equal(*data.persistentHandle, loadedLDevID.Handle)

	secondCall, err := data.tpm.ensureLDevIDPersistent(*data.srk, *data.persistentHandle)
	require.NoError(err)
	require.NotNil(secondCall)
	require.Equal(*data.persistentHandle, secondCall.Handle)
}

func TestEnsureLDevIDPersistentNilSRK(t *testing.T) {
	require := require.New(t)
	data, cleanup := setupTestData(t)
	defer cleanup()

	_, err := data.tpm.ensureLDevIDPersistent(tpm2.NamedHandle{}, *data.persistentHandle)
	require.Error(err)
	require.Contains(err.Error(), "creating new LDevID")
}

func TestCreateLDevIDWithoutPersist(t *testing.T) {
	require := require.New(t)
	data, cleanup := setupTestData(t)
	defer cleanup()

	ldevid, err := data.tpm.CreateLDevID(*data.srk)
	require.NoError(err)
	require.NotNil(ldevid)

	readPublicCmd := tpm2.ReadPublic{
		ObjectHandle: *data.persistentHandle,
	}
	transportTPM := transport.FromReadWriter(data.tpm.channel)
	_, err = readPublicCmd.Execute(transportTPM)
	require.Error(err)
}

func TestLoadLDevIDFromBlob(t *testing.T) {
	require := require.New(t)
	data, cleanup := setupTestData(t)
	defer cleanup()

	createCmd := tpm2.Create{
		ParentHandle: *data.srk,
		InPublic:     tpm2.New2B(LDevIDTemplate),
	}
	transportTPM := transport.FromReadWriter(data.tpm.channel)
	createRsp, err := createCmd.Execute(transportTPM)
	require.NoError(err)

	loadedLDevID, err := data.tpm.loadLDevIDFromBlob(createRsp.OutPublic, createRsp.OutPrivate)
	require.NoError(err)
	require.NotNil(loadedLDevID)
	require.NotEqual(tpm2.TPMHandle(0), loadedLDevID.Handle)
	require.NotEmpty(loadedLDevID.Name)
}

func TestGetTPMCapabilities(t *testing.T) {
	require := require.New(t)
	data, cleanup := setupTestData(t)
	defer cleanup()

	caps, err := data.tpm.Capabilities()
	require.NoError(err)
	require.NotNil(caps)
	require.Greater(caps.PersistentHandleAvailCount, uint32(0))
	require.Greater(caps.PersistentHandleCount, uint32(0))
}

func TestEnsureLDevIDTransient(t *testing.T) {
	require := require.New(t)
	data, cleanup := setupTestData(t)
	defer cleanup()

	ldevid, err := data.tpm.EnsureLDevID(*data.srk, nil)
	require.NoError(err)
	require.NotNil(ldevid)
	require.NotEqual(tpm2.TPMHandle(0), ldevid.Handle)
}

func TestEnsureLDevIDWithBlobStorage(t *testing.T) {
	require := require.New(t)
	data, cleanup := setupTestData(t)
	defer cleanup()

	readWriter := createTestReadWriter(t)
	blobPath := "ldevid.yaml"

	ldevid1, err := data.tpm.EnsureLDevID(*data.srk, WithBlobStorage(blobPath, readWriter))
	require.NoError(err)
	require.NotNil(ldevid1)
	err = data.tpm.FlushContextForHandle(ldevid1.Handle)
	require.NoError(err)
	ldevid2, err := data.tpm.EnsureLDevID(*data.srk, WithBlobStorage(blobPath, readWriter))
	require.NoError(err)
	require.NotNil(ldevid2)

	require.Equal(ldevid1.Name, ldevid2.Name)
}

func TestEnsureLDevIDOptions(t *testing.T) {
	data, cleanup := setupTestData(t)
	defer cleanup()

	readWriter := createTestReadWriter(t)
	blobPath := "ldevid.yaml"
	handlePath := "handle.txt"

	tests := []struct {
		name     string
		option   EnsureLDevIDOption
		validate func(t *testing.T, opts *ensureLDevIDOptions)
	}{
		{
			name:   "WithPersistentHandle sets persistent handle",
			option: WithPersistentHandle(uint32(*data.persistentHandle)),
			validate: func(t *testing.T, opts *ensureLDevIDOptions) {
				require.IsType(t, &persistentHandleStrategy{}, opts.ensureStrategy)
				strategy := opts.ensureStrategy.(*persistentHandleStrategy)
				require.Equal(t, *data.persistentHandle, strategy.handle)
			},
		},
		{
			name:   "WithBlobStorage sets blob path and readWriter",
			option: WithBlobStorage(blobPath, readWriter),
			validate: func(t *testing.T, opts *ensureLDevIDOptions) {
				require.IsType(t, &blobStorageStrategy{}, opts.ensureStrategy)
				strategy := opts.ensureStrategy.(*blobStorageStrategy)
				require.Equal(t, blobPath, strategy.path)
				require.Equal(t, readWriter, strategy.readWriter)
			},
		},
		{
			name:   "WithPersistentHandlePath sets handle path and readWriter",
			option: WithPersistentHandlePath(handlePath, readWriter),
			validate: func(t *testing.T, opts *ensureLDevIDOptions) {
				require.IsType(t, &persistentPathStrategy{}, opts.ensureStrategy)
				strategy := opts.ensureStrategy.(*persistentPathStrategy)
				require.Equal(t, handlePath, strategy.path)
				require.Equal(t, readWriter, strategy.readWriter)
			},
		},
		{
			name:   "WithTransientKey sets transient option type",
			option: WithTransientKey(),
			validate: func(t *testing.T, opts *ensureLDevIDOptions) {
				require.IsType(t, &transientStrategy{}, opts.ensureStrategy)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := &ensureLDevIDOptions{}
			tt.option(opts)
			tt.validate(t, opts)
		})
	}
}

func TestEnsureLDevIDErrors(t *testing.T) {
	data, cleanup := setupTestData(t)
	defer cleanup()

	readWriter := createTestReadWriter(t)

	tests := []struct {
		name                  string
		setupFunc             func() EnsureLDevIDOption
		expectedErrorContains string
	}{
		{
			name: "invalid handle out of range",
			setupFunc: func() EnsureLDevIDOption {
				invalidHandle := tpm2.TPMHandle(0x80000001)
				return WithPersistentHandle(uint32(invalidHandle))
			},
			expectedErrorContains: "not in valid persistent range",
		},
		{
			name: "empty blob path",
			setupFunc: func() EnsureLDevIDOption {
				return WithBlobStorage("", readWriter)
			},
			expectedErrorContains: "blob path cannot be empty",
		},
		{
			name: "empty persistent handle path",
			setupFunc: func() EnsureLDevIDOption {
				return WithPersistentHandlePath("", readWriter)
			},
			expectedErrorContains: "persistent handle path cannot be empty",
		},
		{
			name: "corrupted handle file",
			setupFunc: func() EnsureLDevIDOption {
				handlePath := "handle.txt"
				// Write invalid handle data
				err := readWriter.WriteFile(handlePath, []byte("invalid_handle_data"), 0600)
				require.NoError(t, err)
				return WithPersistentHandlePath(handlePath, readWriter)
			},
			expectedErrorContains: "invalid handle format",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			option := tt.setupFunc()
			_, err := data.tpm.EnsureLDevID(*data.srk, option)
			require.Error(t, err)
			require.Contains(t, err.Error(), tt.expectedErrorContains)
		})
	}
}

func TestFlushContextAfterPersistence(t *testing.T) {
	require := require.New(t)
	data, cleanup := setupTestData(t)
	defer cleanup()

	// Create a transient LDevID first
	ldevid, err := data.tpm.CreateLDevID(*data.srk)
	require.NoError(err)
	require.NotNil(ldevid)

	// Store original handle for comparison
	originalHandle := ldevid.Handle

	err = data.tpm.FlushContextForHandle(originalHandle)
	require.NoError(err)

	// Ensure it's a transient handle (not persistent)
	require.True(originalHandle < PersistentHandleMin || originalHandle > PersistentHandleMax)

	// Persist the LDevID using ensureLDevIDPersistent
	persistedLDevID, err := data.tpm.ensureLDevIDPersistent(*data.srk, *data.persistentHandle)
	require.NoError(err)
	require.NotNil(persistedLDevID)

	// Verify the persisted handle is different from the original transient handle
	require.NotEqual(originalHandle, persistedLDevID.Handle)
	require.Equal(*data.persistentHandle, persistedLDevID.Handle)

	// Verify it's now a persistent handle
	require.True(persistedLDevID.Handle >= PersistentHandleMin && persistedLDevID.Handle <= PersistentHandleMax)
}

func TestFlushContextDirectCall(t *testing.T) {
	require := require.New(t)
	data, cleanup := setupTestData(t)
	defer cleanup()

	// Create a transient LDevID first
	ldevid, err := data.tpm.CreateLDevID(*data.srk)
	require.NoError(err)
	require.NotNil(ldevid)

	// Verify TPM struct has the ldevid set
	require.NotNil(data.tpm.ldevid)
	require.Equal(ldevid.Handle, data.tpm.ldevid.Handle)

	// Call flushContext directly
	err = data.tpm.flushContext()
	require.NoError(err)

	// Verify the ldevid field is now nil
	require.Nil(data.tpm.ldevid)
}

func TestWithTransientKey(t *testing.T) {
	require := require.New(t)

	opts := &ensureLDevIDOptions{}
	option := WithTransientKey()
	option(opts)

	// Verify that the option function sets the transient strategy
	require.IsType(&transientStrategy{}, opts.ensureStrategy)
}

func TestEnsureLDevIDWithTransientKey(t *testing.T) {
	require := require.New(t)
	data, cleanup := setupTestData(t)
	defer cleanup()

	ldevid, err := data.tpm.EnsureLDevID(*data.srk, WithTransientKey())
	require.NoError(err)
	require.NotNil(ldevid)
	require.NotEqual(tpm2.TPMHandle(0), ldevid.Handle)

	// Verify it's a transient handle (not persistent)
	require.True(ldevid.Handle < PersistentHandleMin || ldevid.Handle > PersistentHandleMax)
}

func TestEnsureLDevIDTransientKeyVsNil(t *testing.T) {
	require := require.New(t)
	data, cleanup := setupTestData(t)
	defer cleanup()

	// Create LDevID with nil option (existing behavior)
	ldevid1, err := data.tpm.EnsureLDevID(*data.srk, nil)
	require.NoError(err)
	require.NotNil(ldevid1)

	// Flush the context to clean up
	err = data.tpm.FlushContextForHandle(ldevid1.Handle)
	require.NoError(err)

	// Create LDevID with WithTransientKey option (new behavior)
	ldevid2, err := data.tpm.EnsureLDevID(*data.srk, WithTransientKey())
	require.NoError(err)
	require.NotNil(ldevid2)

	// Both should be transient handles
	require.True(ldevid1.Handle < PersistentHandleMin || ldevid1.Handle > PersistentHandleMax)
	require.True(ldevid2.Handle < PersistentHandleMin || ldevid2.Handle > PersistentHandleMax)

	// Both should have valid names
	require.NotEmpty(ldevid1.Name)
	require.NotEmpty(ldevid2.Name)
}

func TestFlushContextForHandleCases(t *testing.T) {
	data, cleanup := setupTestData(t)
	defer cleanup()

	// Create a transient LDevID for testing
	ldevid, err := data.tpm.CreateLDevID(*data.srk)
	require.NoError(t, err)
	require.NotNil(t, ldevid)

	tests := []struct {
		name        string
		handle      tpm2.TPMHandle
		shouldError bool
		description string
	}{
		{
			name:        "flush transient handle",
			handle:      ldevid.Handle,
			shouldError: false,
			description: "transient handle should flush successfully",
		},
		{
			name:        "flush persistent handle (no-op)",
			handle:      PersistentHandleMin,
			shouldError: false,
			description: "persistent handle should be a no-op and not error",
		},
		{
			name:        "flush another persistent handle",
			handle:      PersistentHandleMin + 1,
			shouldError: false,
			description: "another persistent handle should also be a no-op",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := data.tpm.FlushContextForHandle(tt.handle)
			if tt.shouldError {
				require.Error(t, err, tt.description)
			} else {
				require.NoError(t, err, tt.description)
			}
		})
	}
}

func TestCloseFlushesHandles(t *testing.T) {
	require := require.New(t)
	f, err := setupTestFixture(t)
	require.NoError(err)

	// Generate SRK and LDevID
	srk, err := f.tpm.GenerateSRKPrimary()
	require.NoError(err)
	require.NotNil(srk)

	ldevid, err := f.tpm.CreateLDevID(*srk)
	require.NoError(err)
	require.NotNil(ldevid)

	// Verify handles are set
	require.NotNil(f.tpm.srk)
	require.NotNil(f.tpm.ldevid)

	// Close should flush handles and set them to nil
	err = f.tpm.Close()
	require.NoError(err)

	// Verify handles are cleared
	require.Nil(f.tpm.srk)
	require.Nil(f.tpm.ldevid)
}
