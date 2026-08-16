package device

import (
	"testing"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
)

func TestShouldPersistRenderedUpdate(t *testing.T) {
	const hash = "abc123"
	fingerprint := []domain.DependencySyncConfigRefStatus{{
		ConfigProviderName: "cfg",
		Fingerprint:        lo.ToPtr("fp1"),
	}}

	tests := []struct {
		name         string
		annotations  map[string]string
		specHash     string
		fingerprints []domain.DependencySyncConfigRefStatus
		forceUpdate  bool
		want         bool
	}{
		{
			name:        "When hash matches with no fingerprints and forceUpdate false it should skip",
			annotations: map[string]string{domain.DeviceAnnotationRenderedSpecHash: hash},
			specHash:    hash,
			want:        false,
		},
		{
			name:        "When hash mismatches it should write",
			annotations: map[string]string{domain.DeviceAnnotationRenderedSpecHash: "old"},
			specHash:    hash,
			want:        true,
		},
		{
			name:        "When rendered spec hash annotation is missing it should write",
			annotations: map[string]string{},
			specHash:    hash,
			want:        true,
		},
		{
			name:         "When hash matches with non-empty fingerprints it should write",
			annotations:  map[string]string{domain.DeviceAnnotationRenderedSpecHash: hash},
			specHash:     hash,
			fingerprints: fingerprint,
			want:         true,
		},
		{
			name:        "When hash matches and forceUpdate is true it should write",
			annotations: map[string]string{domain.DeviceAnnotationRenderedSpecHash: hash},
			specHash:    hash,
			forceUpdate: true,
			want:        true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldPersistRenderedUpdate(tt.annotations, tt.specHash, tt.fingerprints, tt.forceUpdate)
			require.Equal(t, tt.want, got)
		})
	}
}
