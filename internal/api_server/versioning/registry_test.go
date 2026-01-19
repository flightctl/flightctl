package versioning

import (
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/api/server"
)

func TestNewRegistry(t *testing.T) {
	registry := NewRegistry(V1Beta1)
	if registry.FallbackVersion() != V1Beta1 {
		t.Errorf("FallbackVersion() = %v, want %v", registry.FallbackVersion(), V1Beta1)
	}
}

func TestRegistry_Negotiate(t *testing.T) {
	registry := NewRegistry(V1Beta1)

	tests := []struct {
		name          string
		requested     Version
		metadata      *server.EndpointMetadata
		wantVersion   Version
		wantSupported []Version
		wantErr       error
	}{
		{
			name:      "no version requested uses first from metadata",
			requested: "",
			metadata: &server.EndpointMetadata{
				Versions: []server.EndpointMetadataVersion{{Version: "v1beta1"}},
			},
			wantVersion:   V1Beta1,
			wantSupported: []Version{V1Beta1},
			wantErr:       nil,
		},
		{
			name:      "no version requested with multiple versions uses first (most preferred)",
			requested: "",
			metadata: &server.EndpointMetadata{
				Versions: []server.EndpointMetadataVersion{
					{Version: "v1"},
					{Version: "v1beta1"},
				},
			},
			wantVersion:   "v1",
			wantSupported: []Version{"v1", V1Beta1},
			wantErr:       nil,
		},
		{
			name:      "requested v1beta1 succeeds",
			requested: V1Beta1,
			metadata: &server.EndpointMetadata{
				Versions: []server.EndpointMetadataVersion{{Version: "v1beta1"}},
			},
			wantVersion:   V1Beta1,
			wantSupported: []Version{V1Beta1},
			wantErr:       nil,
		},
		{
			name:      "unsupported version returns error",
			requested: "v2",
			metadata: &server.EndpointMetadata{
				Versions: []server.EndpointMetadataVersion{{Version: "v1beta1"}},
			},
			wantVersion:   "",
			wantSupported: []Version{V1Beta1},
			wantErr:       ErrNotAcceptable,
		},
		{
			name:          "nil metadata with no version requested returns fallback",
			requested:     "",
			metadata:      nil,
			wantVersion:   V1Beta1,
			wantSupported: nil,
			wantErr:       nil,
		},
		{
			name:          "nil metadata with fallback version requested returns success",
			requested:     V1Beta1,
			metadata:      nil,
			wantVersion:   V1Beta1,
			wantSupported: nil,
			wantErr:       nil,
		},
		{
			name:          "nil metadata with non-fallback version requested returns error",
			requested:     "v2",
			metadata:      nil,
			wantVersion:   "",
			wantSupported: nil,
			wantErr:       ErrNotAcceptable,
		},
		{
			name:      "empty versions with no request returns fallback",
			requested: "",
			metadata: &server.EndpointMetadata{
				Versions: []server.EndpointMetadataVersion{},
			},
			wantVersion:   V1Beta1,
			wantSupported: []Version{},
			wantErr:       nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotVersion, gotSupported, err := registry.Negotiate(tt.requested, tt.metadata)

			if err != tt.wantErr {
				t.Errorf("Negotiate() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if gotVersion != tt.wantVersion {
				t.Errorf("Negotiate() version = %v, want %v", gotVersion, tt.wantVersion)
			}
			if len(gotSupported) != len(tt.wantSupported) {
				t.Errorf("Negotiate() supported = %v, want %v", gotSupported, tt.wantSupported)
				return
			}
			for i, v := range gotSupported {
				if v != tt.wantSupported[i] {
					t.Errorf("Negotiate() supported[%d] = %v, want %v", i, v, tt.wantSupported[i])
				}
			}
		})
	}
}

func TestRegistry_DeprecationDate(t *testing.T) {
	registry := NewRegistry(V1Beta1)
	deprecationDate := time.Date(2025, 12, 31, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		name     string
		version  Version
		metadata *server.EndpointMetadata
		wantNil  bool
		wantDate *time.Time
	}{
		{
			name:     "nil metadata returns nil",
			version:  V1Beta1,
			metadata: nil,
			wantNil:  true,
		},
		{
			name:    "non-deprecated version returns nil",
			version: V1Beta1,
			metadata: &server.EndpointMetadata{
				Versions: []server.EndpointMetadataVersion{{Version: "v1beta1", DeprecatedAt: nil}},
			},
			wantNil: true,
		},
		{
			name:    "deprecated version returns date",
			version: V1Beta1,
			metadata: &server.EndpointMetadata{
				Versions: []server.EndpointMetadataVersion{{Version: "v1beta1", DeprecatedAt: &deprecationDate}},
			},
			wantNil:  false,
			wantDate: &deprecationDate,
		},
		{
			name:    "version not found returns nil",
			version: "v2",
			metadata: &server.EndpointMetadata{
				Versions: []server.EndpointMetadataVersion{{Version: "v1beta1", DeprecatedAt: &deprecationDate}},
			},
			wantNil: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := registry.DeprecationDate(tt.version, tt.metadata)
			if tt.wantNil {
				if got != nil {
					t.Errorf("DeprecationDate() = %v, want nil", got)
				}
			} else {
				if got == nil {
					t.Errorf("DeprecationDate() = nil, want %v", tt.wantDate)
				} else if !got.Equal(*tt.wantDate) {
					t.Errorf("DeprecationDate() = %v, want %v", got, tt.wantDate)
				}
			}
		})
	}
}
