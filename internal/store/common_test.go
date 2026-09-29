package store

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCreateParamsFromKey(t *testing.T) {
	tests := []struct {
		name   string
		key    string
		want   string
		wantOk bool
	}{
		{
			name:   "When key is a nested status path it should map to its JSONB text accessor",
			key:    "status.summary.status",
			want:   "status -> 'summary' ->> 'status'",
			wantOk: true,
		},
		{
			name:   "When key is the update status path it should map to its JSONB text accessor",
			key:    "status.updated.status",
			want:   "status -> 'updated' ->> 'status'",
			wantOk: true,
		},
		{
			name:   "When key contains a coalesce separator it should map to a COALESCE of NULLIF paths preferring the first",
			key:    OsModeStatusSelector,
			want:   "COALESCE(NULLIF(status -> 'systemInfo' ->> 'osMode', ''), NULLIF(status -> 'capabilities' ->> 'osMode', ''))",
			wantOk: true,
		},
		{
			name:   "When key is not allowlisted it should not resolve to any SQL",
			key:    "status.arbitrary.selector",
			want:   "",
			wantOk: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := createParamsFromKey(tt.key)
			assert.Equal(t, tt.wantOk, ok)
			assert.Equal(t, tt.want, got)
		})
	}
}
