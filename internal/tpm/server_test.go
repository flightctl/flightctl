package tpm

import (
	"crypto/x509"
	"encoding/asn1"
	"testing"

	"github.com/google/go-tpm/tpm2"
	"github.com/stretchr/testify/require"
)

func TestRemoveSANFromUnhandledExtensions(t *testing.T) {
	require := require.New(t)

	// SAN OID: 2.5.29.17
	sanOID := asn1.ObjectIdentifier{2, 5, 29, 17}
	// other OIDs for testing
	keyUsageOID := asn1.ObjectIdentifier{2, 5, 29, 15}
	basicConstraintsOID := asn1.ObjectIdentifier{2, 5, 29, 19}
	extKeyUsageOID := asn1.ObjectIdentifier{2, 5, 29, 37}

	tests := []struct {
		name     string
		cert     *x509.Certificate
		expected []asn1.ObjectIdentifier
	}{
		{
			name:     "nil certificate",
			cert:     nil,
			expected: nil,
		},
		{
			name: "empty unhandled extensions",
			cert: &x509.Certificate{
				UnhandledCriticalExtensions: []asn1.ObjectIdentifier{},
			},
			expected: []asn1.ObjectIdentifier{},
		},
		{
			name: "no SAN extension",
			cert: &x509.Certificate{
				UnhandledCriticalExtensions: []asn1.ObjectIdentifier{
					keyUsageOID,
					basicConstraintsOID,
				},
			},
			expected: []asn1.ObjectIdentifier{
				keyUsageOID,
				basicConstraintsOID,
			},
		},
		{
			name: "only SAN extension",
			cert: &x509.Certificate{
				UnhandledCriticalExtensions: []asn1.ObjectIdentifier{
					sanOID,
				},
			},
			expected: []asn1.ObjectIdentifier{},
		},
		{
			name: "SAN extension at beginning",
			cert: &x509.Certificate{
				UnhandledCriticalExtensions: []asn1.ObjectIdentifier{
					sanOID,
					keyUsageOID,
					basicConstraintsOID,
				},
			},
			expected: []asn1.ObjectIdentifier{
				keyUsageOID,
				basicConstraintsOID,
			},
		},
		{
			name: "SAN extension in middle",
			cert: &x509.Certificate{
				UnhandledCriticalExtensions: []asn1.ObjectIdentifier{
					keyUsageOID,
					sanOID,
					basicConstraintsOID,
				},
			},
			expected: []asn1.ObjectIdentifier{
				keyUsageOID,
				basicConstraintsOID,
			},
		},
		{
			name: "SAN extension at end",
			cert: &x509.Certificate{
				UnhandledCriticalExtensions: []asn1.ObjectIdentifier{
					keyUsageOID,
					basicConstraintsOID,
					sanOID,
				},
			},
			expected: []asn1.ObjectIdentifier{
				keyUsageOID,
				basicConstraintsOID,
			},
		},
		{
			name: "multiple SAN extensions",
			cert: &x509.Certificate{
				UnhandledCriticalExtensions: []asn1.ObjectIdentifier{
					sanOID,
					keyUsageOID,
					sanOID,
					basicConstraintsOID,
					sanOID,
				},
			},
			expected: []asn1.ObjectIdentifier{
				keyUsageOID,
				basicConstraintsOID,
			},
		},
		{
			name: "all extensions are SAN",
			cert: &x509.Certificate{
				UnhandledCriticalExtensions: []asn1.ObjectIdentifier{
					sanOID,
					sanOID,
					sanOID,
				},
			},
			expected: []asn1.ObjectIdentifier{},
		},
		{
			name: "mixed extensions with duplicates",
			cert: &x509.Certificate{
				UnhandledCriticalExtensions: []asn1.ObjectIdentifier{
					keyUsageOID,
					sanOID,
					keyUsageOID,
					extKeyUsageOID,
					sanOID,
					basicConstraintsOID,
				},
			},
			expected: []asn1.ObjectIdentifier{
				keyUsageOID,
				keyUsageOID,
				extKeyUsageOID,
				basicConstraintsOID,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// make a copy if cert is not nil to avoid modifying test data
			var testCert *x509.Certificate
			if tt.cert != nil {
				testCert = &x509.Certificate{
					UnhandledCriticalExtensions: make([]asn1.ObjectIdentifier, len(tt.cert.UnhandledCriticalExtensions)),
				}
				copy(testCert.UnhandledCriticalExtensions, tt.cert.UnhandledCriticalExtensions)
			}

			removeSANFromUnhandledExtensions(testCert)

			if testCert == nil {
				require.Nil(testCert)
				return
			}

			require.Equal(len(tt.expected), len(testCert.UnhandledCriticalExtensions))
			require.Equal(tt.expected, testCert.UnhandledCriticalExtensions)
		})
	}
}

func TestRemoveSANFromUnhandledExtensions_PreservesOrder(t *testing.T) {
	require := require.New(t)

	// ensure that non-SAN extensions maintain their relative order
	sanOID := asn1.ObjectIdentifier{2, 5, 29, 17}
	oid1 := asn1.ObjectIdentifier{1, 2, 3, 4}
	oid2 := asn1.ObjectIdentifier{1, 2, 3, 5}
	oid3 := asn1.ObjectIdentifier{1, 2, 3, 6}
	oid4 := asn1.ObjectIdentifier{1, 2, 3, 7}

	cert := &x509.Certificate{
		UnhandledCriticalExtensions: []asn1.ObjectIdentifier{
			oid1,
			sanOID,
			oid2,
			oid3,
			sanOID,
			oid4,
		},
	}

	removeSANFromUnhandledExtensions(cert)

	expected := []asn1.ObjectIdentifier{oid1, oid2, oid3, oid4}
	require.Equal(expected, cert.UnhandledCriticalExtensions)
}

func TestExtractAttestedName(t *testing.T) {
	require := require.New(t)

	tests := []struct {
		name        string
		setupAttest func() []byte
		expectedErr string
	}{
		{
			name: "valid certify attestation",
			setupAttest: func() []byte {
				// create a valid TPMS_ATTEST with TPMS_CERTIFY_INFO
				attestedName := tpm2.TPM2BName{
					Buffer: []byte{
						0x00, 0x0b, // TPM_ALG_SHA256
						0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08,
						0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10,
						0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17, 0x18,
						0x19, 0x1a, 0x1b, 0x1c, 0x1d, 0x1e, 0x1f, 0x20,
					},
				}

				certifyInfo := tpm2.TPMSCertifyInfo{
					Name:          attestedName,
					QualifiedName: tpm2.TPM2BName{Buffer: []byte{}},
				}

				attest := tpm2.TPMSAttest{
					Type:            tpm2.TPMSTAttestCertify,
					QualifiedSigner: tpm2.TPM2BName{Buffer: []byte{}},
					ExtraData:       tpm2.TPM2BData{Buffer: []byte{}},
					ClockInfo: tpm2.TPMSClockInfo{
						Clock:        0,
						ResetCount:   0,
						RestartCount: 0,
						Safe:         true,
					},
					FirmwareVersion: 0,
					Attested:        tpm2.NewTPMUAttest(tpm2.TPMSTAttestCertify, &certifyInfo),
				}

				return tpm2.Marshal(tpm2.New2B(attest))
			},
		},
		{
			name: "invalid unmarshal data",
			setupAttest: func() []byte {
				return []byte{0x00, 0x01, 0xff} // invalid data
			},
			expectedErr: "extracting TPMS_ATTEST contents",
		},
		{
			name: "wrong attestation type",
			setupAttest: func() []byte {
				// create TPMS_ATTEST with TPM_ST_ATTEST_QUOTE (not CERTIFY)
				quoteInfo := tpm2.TPMSQuoteInfo{
					PCRSelect: tpm2.TPMLPCRSelection{
						PCRSelections: []tpm2.TPMSPCRSelection{},
					},
					PCRDigest: tpm2.TPM2BDigest{Buffer: []byte{}},
				}

				attest := tpm2.TPMSAttest{
					Type:            tpm2.TPMSTAttestQuote,
					QualifiedSigner: tpm2.TPM2BName{Buffer: []byte{}},
					ExtraData:       tpm2.TPM2BData{Buffer: []byte{}},
					ClockInfo: tpm2.TPMSClockInfo{
						Clock:        0,
						ResetCount:   0,
						RestartCount: 0,
						Safe:         true,
					},
					FirmwareVersion: 0,
					Attested:        tpm2.NewTPMUAttest(tpm2.TPMSTAttestQuote, &quoteInfo),
				}
				return tpm2.Marshal(tpm2.New2B(attest))
			},
			expectedErr: "invalid attestation type",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			certifyInfo := tt.setupAttest()

			name, err := extractAttestedName(certifyInfo)

			if tt.expectedErr != "" {
				require.Error(err)
				require.Contains(err.Error(), tt.expectedErr)
				require.Nil(name)
			} else {
				require.NoError(err)
				require.NotNil(name)
				require.NotEmpty(name)
			}
		})
	}
}

func TestComputeTPMName(t *testing.T) {
	require := require.New(t)

	tests := []struct {
		name    string
		pub     *tpm2.TPMTPublic
		wantErr string
	}{
		{
			name: "SHA256 name algorithm",
			pub: &tpm2.TPMTPublic{
				Type:    tpm2.TPMAlgECC,
				NameAlg: tpm2.TPMAlgSHA256,
				Parameters: tpm2.NewTPMUPublicParms(
					tpm2.TPMAlgECC,
					&tpm2.TPMSECCParms{
						CurveID: tpm2.TPMECCNistP256,
					},
				),
			},
		},
		{
			name: "unsupported name algorithm",
			pub: &tpm2.TPMTPublic{
				Type:    tpm2.TPMAlgECC,
				NameAlg: tpm2.TPMAlgSHA1,
			},
			wantErr: "unsupported NameAlg",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			name, err := computeTPMName(tt.pub)

			if tt.wantErr != "" {
				require.Error(err)
				require.Contains(err.Error(), tt.wantErr)
				require.Nil(name)
			} else {
				require.NoError(err)
				require.NotNil(name)
				// TPM Name should be algorithm prefix (2 bytes) + digest (32 bytes for SHA256)
				require.Equal(34, len(name))
				// first 2 bytes should be algorithm ID
				require.Equal(byte(0x00), name[0])
				require.Equal(byte(0x0b), name[1]) // TPM_ALG_SHA256 = 0x000B
			}
		})
	}
}
