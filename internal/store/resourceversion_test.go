package store

import (
	"testing"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/stretchr/testify/require"
)

func TestCheckResourceVersionConflict(t *testing.T) {
	rv := func(v string) *string { return &v }

	tests := []struct {
		name     string
		current  *domain.ObjectMeta
		incoming *domain.ObjectMeta
		wantErr  error
	}{
		{
			name:     "When current is nil it should skip the check",
			current:  nil,
			incoming: &domain.ObjectMeta{ResourceVersion: rv("1")},
			wantErr:  nil,
		},
		{
			name:     "When incoming is nil it should skip the check",
			current:  &domain.ObjectMeta{ResourceVersion: rv("1")},
			incoming: nil,
			wantErr:  nil,
		},
		{
			name:     "When incoming resourceVersion is nil it should skip the check",
			current:  &domain.ObjectMeta{ResourceVersion: rv("1")},
			incoming: &domain.ObjectMeta{},
			wantErr:  nil,
		},
		{
			name:     "When current resourceVersion is unset and incoming supplies a value it should conflict",
			current:  &domain.ObjectMeta{},
			incoming: &domain.ObjectMeta{ResourceVersion: rv("0")},
			wantErr:  flterrors.ErrResourceVersionConflict,
		},
		{
			name:     "When resourceVersions match it should succeed",
			current:  &domain.ObjectMeta{ResourceVersion: rv("7")},
			incoming: &domain.ObjectMeta{ResourceVersion: rv("7")},
			wantErr:  nil,
		},
		{
			name:     "When resourceVersions differ it should conflict",
			current:  &domain.ObjectMeta{ResourceVersion: rv("7")},
			incoming: &domain.ObjectMeta{ResourceVersion: rv("8")},
			wantErr:  flterrors.ErrResourceVersionConflict,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := CheckResourceVersionConflict(tt.current, tt.incoming)
			require.ErrorIs(t, err, tt.wantErr)
		})
	}
}
