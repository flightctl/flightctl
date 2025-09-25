package identity

import (
	"testing"

	"github.com/flightctl/flightctl/internal/tpm"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestNewExportableFactory(t *testing.T) {
	tests := []struct {
		name    string
		withTPM bool
	}{
		{
			name:    "TPM factory",
			withTPM: true,
		},
		{
			name:    "Software-only factory",
			withTPM: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			logger := log.NewPrefixLogger("")

			var tpmClient tpm.Client
			if tt.withTPM {
				tpmClient = tpm.NewMockClient(ctrl)
			}

			factory := NewExportableFactory(tpmClient, logger)
			require.NotNil(t, factory)
		})
	}
}

func TestExportableFactory_NewExportableProvider_Software(t *testing.T) {
	tests := []struct {
		name    string
		withTPM bool
		wantErr bool
	}{
		{
			name:    "TPM factory supports software",
			withTPM: true,
			wantErr: false,
		},
		{
			name:    "Software-only factory supports software",
			withTPM: false,
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			logger := log.NewPrefixLogger("")

			var tpmClient tpm.Client
			if tt.withTPM {
				tpmClient = tpm.NewMockClient(ctrl)
			}

			factory := NewExportableFactory(tpmClient, logger)

			provider, err := factory.NewExportableProvider(IdentityTypeSoftware)

			if tt.wantErr {
				require.Error(t, err)
				require.Nil(t, provider)
			} else {
				require.NoError(t, err)
				require.NotNil(t, provider)
			}
		})
	}
}

func TestExportableFactory_NewExportableProvider_TPM(t *testing.T) {
	tests := []struct {
		name     string
		withTPM  bool
		wantErr  bool
		errorMsg string
	}{
		{
			name:    "TPM factory supports TPM",
			withTPM: true,
			wantErr: false,
		},
		{
			name:     "Software-only factory rejects TPM",
			withTPM:  false,
			wantErr:  true,
			errorMsg: "tpm provider not initialized",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			logger := log.NewPrefixLogger("")

			var tpmClient tpm.Client
			if tt.withTPM {
				tpmClient = tpm.NewMockClient(ctrl)
			}

			factory := NewExportableFactory(tpmClient, logger)

			provider, err := factory.NewExportableProvider(IdentityTypeTPM)

			if tt.wantErr {
				require.Error(t, err)
				require.Nil(t, provider)
				require.Contains(t, err.Error(), tt.errorMsg)
			} else {
				require.NoError(t, err)
				require.NotNil(t, provider)
			}
		})
	}
}

func TestExportableFactory_NewExportableProvider_UnsupportedType(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	logger := log.NewPrefixLogger("")

	factory := NewExportableFactory(nil, logger)

	provider, err := factory.NewExportableProvider("unsupported")

	require.Error(t, err)
	require.Nil(t, provider)
	require.Contains(t, err.Error(), "unsupported identity type: unsupported")
}
