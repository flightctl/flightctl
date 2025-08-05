package tpm

import (
	"encoding/base64"
	"errors"
	"testing"

	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/google/go-tpm/tpm2"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestFileStorageGetKey(t *testing.T) {
	tests := []struct {
		name          string
		keyType       KeyType
		setupTestData func(t *testing.T, rw fileio.ReadWriter, storagePath string)
		wantPub       bool
		wantPriv      bool
		wantErr       error
	}{
		{
			name:    "successful get LDevID key",
			keyType: LDevID,
			setupTestData: func(t *testing.T, rw fileio.ReadWriter, storagePath string) {
				// Create valid TPM2B structures
				pub := tpm2.New2B(tpm2.TPMTPublic{
					Type:    tpm2.TPMAlgECC,
					NameAlg: tpm2.TPMAlgSHA256,
				})
				pubData := tpm2.Marshal(pub)

				priv := tpm2.TPM2BPrivate{
					Buffer: []byte("test-private-data"),
				}
				privData := tpm2.Marshal(priv)

				testData := &storageData{
					LDevID: &keyData{
						PublicBlob:  base64.StdEncoding.EncodeToString(pubData),
						PrivateBlob: base64.StdEncoding.EncodeToString(privData),
					},
				}
				data, err := yaml.Marshal(testData)
				require.NoError(t, err)
				err = rw.WriteFile(storagePath, data, 0600)
				require.NoError(t, err)
			},
			wantPub:  true,
			wantPriv: true,
			wantErr:  nil,
		},
		{
			name:    "successful get LAK key",
			keyType: LAK,
			setupTestData: func(t *testing.T, rw fileio.ReadWriter, storagePath string) {
				// Create valid TPM2B structures
				pub := tpm2.New2B(tpm2.TPMTPublic{
					Type:    tpm2.TPMAlgECC,
					NameAlg: tpm2.TPMAlgSHA256,
				})
				pubData := tpm2.Marshal(pub)

				priv := tpm2.TPM2BPrivate{
					Buffer: []byte("test-private-data"),
				}
				privData := tpm2.Marshal(priv)

				testData := &storageData{
					LAK: &keyData{
						PublicBlob:  base64.StdEncoding.EncodeToString(pubData),
						PrivateBlob: base64.StdEncoding.EncodeToString(privData),
					},
				}
				data, err := yaml.Marshal(testData)
				require.NoError(t, err)
				err = rw.WriteFile(storagePath, data, 0600)
				require.NoError(t, err)
			},
			wantPub:  true,
			wantPriv: true,
			wantErr:  nil,
		},
		{
			name:    "key not found - returns ErrNotFound",
			keyType: LDevID,
			setupTestData: func(t *testing.T, rw fileio.ReadWriter, storagePath string) {
				testData := &storageData{}
				data, err := yaml.Marshal(testData)
				require.NoError(t, err)
				err = rw.WriteFile(storagePath, data, 0600)
				require.NoError(t, err)
			},
			wantPub:  false,
			wantPriv: false,
			wantErr:  ErrNotFound,
		},
		{
			name:    "file does not exist - returns ErrNotFound",
			keyType: LDevID,
			setupTestData: func(t *testing.T, rw fileio.ReadWriter, storagePath string) {
				// Don't create any file
			},
			wantPub:  false,
			wantPriv: false,
			wantErr:  ErrNotFound,
		},
		{
			name:    "invalid yaml",
			keyType: LDevID,
			setupTestData: func(t *testing.T, rw fileio.ReadWriter, storagePath string) {
				err := rw.WriteFile(storagePath, []byte("invalid yaml: ["), 0600)
				require.NoError(t, err)
			},
			wantPub:  false,
			wantPriv: false,
			wantErr:  errors.New("unmarshaling YAML from file"),
		},
		{
			name:    "empty public blob",
			keyType: LDevID,
			setupTestData: func(t *testing.T, rw fileio.ReadWriter, storagePath string) {
				testData := &storageData{
					LDevID: &keyData{
						PublicBlob:  "",
						PrivateBlob: base64.StdEncoding.EncodeToString([]byte("test-private")),
					},
				}
				data, err := yaml.Marshal(testData)
				require.NoError(t, err)
				err = rw.WriteFile(storagePath, data, 0600)
				require.NoError(t, err)
			},
			wantPub:  false,
			wantPriv: false,
			wantErr:  errors.New("failed to load public key for ldevid from storage: public blob is empty"),
		},
		{
			name:    "empty private blob",
			keyType: LDevID,
			setupTestData: func(t *testing.T, rw fileio.ReadWriter, storagePath string) {
				testData := &storageData{
					LDevID: &keyData{
						PublicBlob:  base64.StdEncoding.EncodeToString([]byte("test-public")),
						PrivateBlob: "",
					},
				}
				data, err := yaml.Marshal(testData)
				require.NoError(t, err)
				err = rw.WriteFile(storagePath, data, 0600)
				require.NoError(t, err)
			},
			wantPub:  false,
			wantPriv: false,
			wantErr:  errors.New("failed to load public key for ldevid from storage: unmarshal public key as TPM2BPublic: unexpected EOF"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			rw := fileio.NewReadWriter()
			rw.SetRootdir(tmpDir)

			storagePath := "tpm-storage.yaml"
			logger := log.NewPrefixLogger("test")

			if tt.setupTestData != nil {
				tt.setupTestData(t, rw, storagePath)
			}

			s := NewFileStorage(rw, storagePath, logger)

			pub, priv, err := s.GetKey(tt.keyType)

			if tt.wantErr != nil {
				require.Error(t, err)
				if errors.Is(tt.wantErr, ErrNotFound) {
					require.True(t, errors.Is(err, ErrNotFound))
				} else {
					require.Contains(t, err.Error(), tt.wantErr.Error())
				}
			} else {
				require.NoError(t, err)
			}

			if tt.wantPub {
				require.NotNil(t, pub)
			} else {
				require.Nil(t, pub)
			}

			if tt.wantPriv {
				require.NotNil(t, priv)
			} else {
				require.Nil(t, priv)
			}
		})
	}
}

func TestFileStorageStoreKey(t *testing.T) {
	// create valid TPM2B structures for testing
	validPublic := tpm2.New2B(tpm2.TPMTPublic{
		Type:    tpm2.TPMAlgRSA,
		NameAlg: tpm2.TPMAlgSHA256,
	})
	validPrivate := tpm2.TPM2BPrivate{
		Buffer: []byte("test-private-data"),
	}

	tests := []struct {
		name          string
		keyType       KeyType
		public        tpm2.TPM2BPublic
		private       tpm2.TPM2BPrivate
		setupTestData func(t *testing.T, rw fileio.ReadWriter, storagePath string)
		wantErr       error
		validateData  func(t *testing.T, rw fileio.ReadWriter, storagePath string)
	}{
		{
			name:    "successful store new LDevID key",
			keyType: LDevID,
			public:  validPublic,
			private: validPrivate,
			setupTestData: func(t *testing.T, rw fileio.ReadWriter, storagePath string) {
				// no file
			},
			wantErr: nil,
			validateData: func(t *testing.T, rw fileio.ReadWriter, storagePath string) {
				data, err := rw.ReadFile(storagePath)
				require.NoError(t, err)

				var stored storageData
				err = yaml.Unmarshal(data, &stored)
				require.NoError(t, err)

				require.NotNil(t, stored.LDevID)
				require.NotEmpty(t, stored.LDevID.PublicBlob)
				require.NotEmpty(t, stored.LDevID.PrivateBlob)
			},
		},
		{
			name:    "successful store new LAK key",
			keyType: LAK,
			public:  validPublic,
			private: validPrivate,
			setupTestData: func(t *testing.T, rw fileio.ReadWriter, storagePath string) {
				// no file
			},
			wantErr: nil,
			validateData: func(t *testing.T, rw fileio.ReadWriter, storagePath string) {
				data, err := rw.ReadFile(storagePath)
				require.NoError(t, err)

				var stored storageData
				err = yaml.Unmarshal(data, &stored)
				require.NoError(t, err)

				require.NotNil(t, stored.LAK)
				require.NotEmpty(t, stored.LAK.PublicBlob)
				require.NotEmpty(t, stored.LAK.PrivateBlob)
			},
		},
		{
			name:    "update existing key",
			keyType: LDevID,
			public:  validPublic,
			private: validPrivate,
			setupTestData: func(t *testing.T, rw fileio.ReadWriter, storagePath string) {
				// create existing data
				existingData := &storageData{
					LDevID: &keyData{
						PublicBlob:  "old-public",
						PrivateBlob: "old-private",
					},
					LAK: &keyData{
						PublicBlob:  "lak-public",
						PrivateBlob: "lak-private",
					},
				}
				data, err := yaml.Marshal(existingData)
				require.NoError(t, err)
				err = rw.WriteFile(storagePath, data, 0600)
				require.NoError(t, err)
			},
			wantErr: nil,
			validateData: func(t *testing.T, rw fileio.ReadWriter, storagePath string) {
				data, err := rw.ReadFile(storagePath)
				require.NoError(t, err)

				var stored storageData
				err = yaml.Unmarshal(data, &stored)
				require.NoError(t, err)

				// Verify LDevID was updated
				require.NotNil(t, stored.LDevID)
				require.NotEqual(t, "old-public", stored.LDevID.PublicBlob)
				require.NotEqual(t, "old-private", stored.LDevID.PrivateBlob)

				// Verify LAK was preserved
				require.NotNil(t, stored.LAK)
				require.Equal(t, "lak-public", stored.LAK.PublicBlob)
				require.Equal(t, "lak-private", stored.LAK.PrivateBlob)
			},
		},
		{
			name:    "unsupported key type",
			keyType: KeyType("unsupported"),
			public:  validPublic,
			private: validPrivate,
			setupTestData: func(t *testing.T, rw fileio.ReadWriter, storagePath string) {
				// no setup needed
			},
			wantErr: errors.New("ensuring key structure: unsupported key type: unsupported"),
		},
		{
			name:    "validation fails - corrupted file after write",
			keyType: LDevID,
			public:  validPublic,
			private: validPrivate,
			setupTestData: func(t *testing.T, rw fileio.ReadWriter, storagePath string) {
				// simulates a case where the file gets corrupted after write
			},
			wantErr: errors.New("validation failed for stored key ldevid"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			rw := fileio.NewReadWriter()
			rw.SetRootdir(tmpDir)

			storagePath := "tpm-storage.yaml"
			logger := log.NewPrefixLogger("test")

			if tt.setupTestData != nil {
				tt.setupTestData(t, rw, storagePath)
			}

			s := NewFileStorage(rw, storagePath, logger)

			// for the corruption test case, we need to corrupt the file after the initial write
			if tt.name == "validation fails - corrupted file after write" {
				// create wrapper that corrupts the file on the second read
				originalRW := s.(*fileStorage).rw
				readCount := 0
				s.(*fileStorage).rw = &corruptingReadWriter{
					ReadWriter: originalRW,
					corruptOnRead: func() bool {
						readCount++
						return readCount == 2 // corrupt on the second read (validation)
					},
				}
			}

			err := s.StoreKey(tt.keyType, tt.public, tt.private)

			if tt.wantErr != nil {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.wantErr.Error())
			} else {
				require.NoError(t, err)
				if tt.validateData != nil {
					tt.validateData(t, rw, storagePath)
				}
			}
		})
	}
}

func TestFileStorageGetPassword(t *testing.T) {
	tests := []struct {
		name          string
		setupTestData func(t *testing.T, rw fileio.ReadWriter, storagePath string)
		want          []byte
		wantErr       error
	}{
		{
			name: "successful get password",
			setupTestData: func(t *testing.T, rw fileio.ReadWriter, storagePath string) {
				testPassword := []byte("test-password")
				testData := &storageData{
					SealedPassword: &passwordData{
						EncodedPassword: base64.StdEncoding.EncodeToString(testPassword),
					},
				}
				data, err := yaml.Marshal(testData)
				require.NoError(t, err)
				err = rw.WriteFile(storagePath, data, 0600)
				require.NoError(t, err)
			},
			want:    []byte("test-password"),
			wantErr: nil,
		},
		{
			name: "password not found",
			setupTestData: func(t *testing.T, rw fileio.ReadWriter, storagePath string) {
				testData := &storageData{}
				data, err := yaml.Marshal(testData)
				require.NoError(t, err)
				err = rw.WriteFile(storagePath, data, 0600)
				require.NoError(t, err)
			},
			want:    nil,
			wantErr: ErrNotFound,
		},
		{
			name: "empty password in storage",
			setupTestData: func(t *testing.T, rw fileio.ReadWriter, storagePath string) {
				testData := &storageData{
					SealedPassword: &passwordData{
						EncodedPassword: "",
					},
				}
				data, err := yaml.Marshal(testData)
				require.NoError(t, err)
				err = rw.WriteFile(storagePath, data, 0600)
				require.NoError(t, err)
			},
			want:    nil,
			wantErr: errors.New("reading encoded password: password is empty in storage"),
		},
		{
			name: "invalid base64 encoding",
			setupTestData: func(t *testing.T, rw fileio.ReadWriter, storagePath string) {
				testData := &storageData{
					SealedPassword: &passwordData{
						EncodedPassword: "invalid-base64!@#",
					},
				}
				data, err := yaml.Marshal(testData)
				require.NoError(t, err)
				err = rw.WriteFile(storagePath, data, 0600)
				require.NoError(t, err)
			},
			want:    nil,
			wantErr: errors.New("decoding base64 password"),
		},
		{
			name: "file does not exist",
			setupTestData: func(t *testing.T, rw fileio.ReadWriter, storagePath string) {
				// do not create any file
			},
			want:    nil,
			wantErr: ErrNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			rw := fileio.NewReadWriter()
			rw.SetRootdir(tmpDir)

			storagePath := "tpm-storage.yaml"
			logger := log.NewPrefixLogger("test")

			if tt.setupTestData != nil {
				tt.setupTestData(t, rw, storagePath)
			}

			s := NewFileStorage(rw, storagePath, logger)

			got, err := s.GetPassword()

			if tt.wantErr != nil {
				require.Error(t, err)
				if errors.Is(tt.wantErr, ErrNotFound) {
					require.True(t, errors.Is(err, ErrNotFound))
				} else {
					require.Contains(t, err.Error(), tt.wantErr.Error())
				}
			} else {
				require.NoError(t, err)
				require.Equal(t, tt.want, got)
			}
		})
	}
}

func TestFileStorageStorePassword(t *testing.T) {
	tests := []struct {
		name          string
		newPassword   []byte
		setupTestData func(t *testing.T, rw fileio.ReadWriter, storagePath string)
		wantErr       error
		validateData  func(t *testing.T, rw fileio.ReadWriter, storagePath string)
	}{
		{
			name:        "successful store new password",
			newPassword: []byte("new-password"),
			setupTestData: func(t *testing.T, rw fileio.ReadWriter, storagePath string) {
				// start with no file
			},
			wantErr: nil,
			validateData: func(t *testing.T, rw fileio.ReadWriter, storagePath string) {
				data, err := rw.ReadFile(storagePath)
				require.NoError(t, err)

				var stored storageData
				err = yaml.Unmarshal(data, &stored)
				require.NoError(t, err)

				require.NotNil(t, stored.SealedPassword)
				require.NotEmpty(t, stored.SealedPassword.EncodedPassword)

				// ensure we can decode it
				decoded, err := base64.StdEncoding.DecodeString(stored.SealedPassword.EncodedPassword)
				require.NoError(t, err)
				require.Equal(t, []byte("new-password"), decoded)
			},
		},
		{
			name:        "update existing password",
			newPassword: []byte("updated-password"),
			setupTestData: func(t *testing.T, rw fileio.ReadWriter, storagePath string) {
				existingData := &storageData{
					SealedPassword: &passwordData{
						EncodedPassword: base64.StdEncoding.EncodeToString([]byte("old-password")),
					},
					LDevID: &keyData{
						PublicBlob:  "existing-key",
						PrivateBlob: "existing-key",
					},
				}
				data, err := yaml.Marshal(existingData)
				require.NoError(t, err)
				err = rw.WriteFile(storagePath, data, 0600)
				require.NoError(t, err)
			},
			wantErr: nil,
			validateData: func(t *testing.T, rw fileio.ReadWriter, storagePath string) {
				data, err := rw.ReadFile(storagePath)
				require.NoError(t, err)

				var stored storageData
				err = yaml.Unmarshal(data, &stored)
				require.NoError(t, err)

				// ensure password was updated
				require.NotNil(t, stored.SealedPassword)
				decoded, err := base64.StdEncoding.DecodeString(stored.SealedPassword.EncodedPassword)
				require.NoError(t, err)
				require.Equal(t, []byte("updated-password"), decoded)

				// ensure other data was preserved
				require.NotNil(t, stored.LDevID)
				require.Equal(t, "existing-key", stored.LDevID.PublicBlob)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			rw := fileio.NewReadWriter()
			rw.SetRootdir(tmpDir)

			storagePath := "tpm-storage.yaml"
			logger := log.NewPrefixLogger("test")

			if tt.setupTestData != nil {
				tt.setupTestData(t, rw, storagePath)
			}

			s := NewFileStorage(rw, storagePath, logger)

			err := s.StorePassword(tt.newPassword)
			if tt.wantErr != nil {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.wantErr.Error())
			} else {
				require.NoError(t, err)
				if tt.validateData != nil {
					tt.validateData(t, rw, storagePath)
				}
			}
		})
	}
}

func TestFileStorageClearPassword(t *testing.T) {
	tests := []struct {
		name          string
		setupTestData func(t *testing.T, rw fileio.ReadWriter, storagePath string)
		wantErr       error
		validateData  func(t *testing.T, rw fileio.ReadWriter, storagePath string)
	}{
		{
			name: "successful clear password",
			setupTestData: func(t *testing.T, rw fileio.ReadWriter, storagePath string) {
				existingData := &storageData{
					SealedPassword: &passwordData{
						EncodedPassword: base64.StdEncoding.EncodeToString([]byte("password")),
					},
					LDevID: &keyData{
						PublicBlob:  "existing-key",
						PrivateBlob: "existing-key",
					},
				}
				data, err := yaml.Marshal(existingData)
				require.NoError(t, err)
				err = rw.WriteFile(storagePath, data, 0600)
				require.NoError(t, err)
			},
			wantErr: nil,
			validateData: func(t *testing.T, rw fileio.ReadWriter, storagePath string) {
				data, err := rw.ReadFile(storagePath)
				require.NoError(t, err)

				var stored storageData
				err = yaml.Unmarshal(data, &stored)
				require.NoError(t, err)

				// ensure cleared
				require.NotNil(t, stored.SealedPassword)
				require.Empty(t, stored.SealedPassword.EncodedPassword)

				// ensure other data is sane
				require.NotNil(t, stored.LDevID)
				require.Equal(t, "existing-key", stored.LDevID.PublicBlob)
			},
		},
		{
			name: "clear when no password exists",
			setupTestData: func(t *testing.T, rw fileio.ReadWriter, storagePath string) {
				existingData := &storageData{
					LDevID: &keyData{
						PublicBlob:  "existing-key",
						PrivateBlob: "existing-key",
					},
				}
				data, err := yaml.Marshal(existingData)
				require.NoError(t, err)
				err = rw.WriteFile(storagePath, data, 0600)
				require.NoError(t, err)
			},
			wantErr: nil,
			validateData: func(t *testing.T, rw fileio.ReadWriter, storagePath string) {
				data, err := rw.ReadFile(storagePath)
				require.NoError(t, err)

				var stored storageData
				err = yaml.Unmarshal(data, &stored)
				require.NoError(t, err)

				// ensure no change
				require.Nil(t, stored.SealedPassword)
				require.NotNil(t, stored.LDevID)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			rw := fileio.NewReadWriter()
			rw.SetRootdir(tmpDir)

			storagePath := "tpm-storage.yaml"
			logger := log.NewPrefixLogger("test")

			if tt.setupTestData != nil {
				tt.setupTestData(t, rw, storagePath)
			}

			s := NewFileStorage(rw, storagePath, logger)

			err := s.ClearPassword()

			if tt.wantErr != nil {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.wantErr.Error())
			} else {
				require.NoError(t, err)
				if tt.validateData != nil {
					tt.validateData(t, rw, storagePath)
				}
			}
		})
	}
}

func TestFileStorageClose(t *testing.T) {
	tmpDir := t.TempDir()
	rw := fileio.NewReadWriter()
	rw.SetRootdir(tmpDir)

	logger := log.NewPrefixLogger("test")
	s := NewFileStorage(rw, "test-path", logger)

	err := s.Close()
	require.NoError(t, err)
}

func TestKeyDataPublic(t *testing.T) {
	tests := []struct {
		name    string
		keyData *keyData
		wantErr error
	}{
		{
			name: "valid public blob",
			keyData: &keyData{
				PublicBlob: base64.StdEncoding.EncodeToString(tpm2.Marshal(tpm2.New2B(tpm2.TPMTPublic{
					Type:    tpm2.TPMAlgRSA,
					NameAlg: tpm2.TPMAlgSHA256,
				}))),
			},
			wantErr: nil,
		},
		{
			name: "empty public blob",
			keyData: &keyData{
				PublicBlob: "",
			},
			wantErr: errors.New("public blob is empty"),
		},
		{
			name: "invalid base64",
			keyData: &keyData{
				PublicBlob: "invalid-base64!@#",
			},
			wantErr: errors.New("decode public key blob"),
		},
		{
			name: "invalid TPM2BPublic data",
			keyData: &keyData{
				PublicBlob: base64.StdEncoding.EncodeToString([]byte("invalid")),
			},
			wantErr: errors.New("unmarshal public key as TPM2BPublic"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pub, err := tt.keyData.Public()

			if tt.wantErr != nil {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.wantErr.Error())
			} else {
				require.NoError(t, err)
				require.NotNil(t, pub)
			}
		})
	}
}

func TestKeyData_Private(t *testing.T) {
	tests := []struct {
		name    string
		keyData *keyData
		wantErr error
	}{
		{
			name: "valid private blob",
			keyData: &keyData{
				PrivateBlob: base64.StdEncoding.EncodeToString(tpm2.Marshal(tpm2.TPM2BPrivate{
					Buffer: []byte("test-private"),
				})),
			},
			wantErr: nil,
		},
		{
			name: "empty private blob",
			keyData: &keyData{
				PrivateBlob: "",
			},
			wantErr: errors.New("private blob is empty"),
		},
		{
			name: "invalid base64",
			keyData: &keyData{
				PrivateBlob: "invalid-base64!@#",
			},
			wantErr: errors.New("decode private key blob"),
		},
		{
			name: "invalid TPM2BPrivate data",
			keyData: &keyData{
				PrivateBlob: base64.StdEncoding.EncodeToString([]byte("invalid")),
			},
			wantErr: errors.New("unmarshal private key as TPM2BPrivate"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			priv, err := tt.keyData.Private()

			if tt.wantErr != nil {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.wantErr.Error())
			} else {
				require.NoError(t, err)
				require.NotNil(t, priv)
			}
		})
	}
}

func TestKeyData_Update(t *testing.T) {
	public := tpm2.New2B(tpm2.TPMTPublic{
		Type:    tpm2.TPMAlgRSA,
		NameAlg: tpm2.TPMAlgSHA256,
	})
	private := tpm2.TPM2BPrivate{
		Buffer: []byte("test-private"),
	}

	keyData := &keyData{}
	err := keyData.Update(public, private)

	require.NoError(t, err)
	require.NotEmpty(t, keyData.PublicBlob)
	require.NotEmpty(t, keyData.PrivateBlob)

	// Verify the data can be decoded back
	decodedPub, err := keyData.Public()
	require.NoError(t, err)
	require.NotNil(t, decodedPub)

	decodedPriv, err := keyData.Private()
	require.NoError(t, err)
	require.NotNil(t, decodedPriv)
}

func TestPasswordData_Encoded(t *testing.T) {
	tests := []struct {
		name         string
		passwordData *passwordData
		want         string
		wantErr      error
	}{
		{
			name: "valid encoded password",
			passwordData: &passwordData{
				EncodedPassword: base64.StdEncoding.EncodeToString([]byte("test-password")),
			},
			want:    base64.StdEncoding.EncodeToString([]byte("test-password")),
			wantErr: nil,
		},
		{
			name: "empty password",
			passwordData: &passwordData{
				EncodedPassword: "",
			},
			want:    "",
			wantErr: errors.New("password is empty in storage"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.passwordData.Encoded()

			if tt.wantErr != nil {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.wantErr.Error())
			} else {
				require.NoError(t, err)
				require.Equal(t, tt.want, got)
			}
		})
	}
}

func TestPasswordDataClear(t *testing.T) {
	passwordData := &passwordData{
		EncodedPassword: "some-password",
	}

	err := passwordData.Clear()
	require.NoError(t, err)
	require.Empty(t, passwordData.EncodedPassword)
}

func TestPasswordDataUpdate(t *testing.T) {
	passwordData := &passwordData{}
	testPassword := []byte("new-password")

	passwordData.Update(testPassword)

	require.Equal(t, base64.StdEncoding.EncodeToString(testPassword), passwordData.EncodedPassword)
}

func TestStorageDataHandle(t *testing.T) {
	tests := []struct {
		name        string
		storageData *storageData
		keyType     KeyType
		want        *keyData
	}{
		{
			name: "get LDevID handle",
			storageData: &storageData{
				LDevID: &keyData{PublicBlob: "ldevid"},
				LAK:    &keyData{PublicBlob: "lak"},
			},
			keyType: LDevID,
			want:    &keyData{PublicBlob: "ldevid"},
		},
		{
			name: "get LAK handle",
			storageData: &storageData{
				LDevID: &keyData{PublicBlob: "ldevid"},
				LAK:    &keyData{PublicBlob: "lak"},
			},
			keyType: LAK,
			want:    &keyData{PublicBlob: "lak"},
		},
		{
			name: "unknown key type",
			storageData: &storageData{
				LDevID: &keyData{PublicBlob: "ldevid"},
				LAK:    &keyData{PublicBlob: "lak"},
			},
			keyType: KeyType("unknown"),
			want:    nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.storageData.Handle(tt.keyType)
			require.Equal(t, tt.want, got)
		})
	}
}

// helper for corruption test
type corruptingReadWriter struct {
	fileio.ReadWriter
	corruptOnRead func() bool
}

func (c *corruptingReadWriter) ReadFile(path string) ([]byte, error) {
	if c.corruptOnRead() {
		return []byte("{}"), nil // return empty JSON
	}
	return c.ReadWriter.ReadFile(path)
}
