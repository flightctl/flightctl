package model

import (
	"testing"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestNormalizeCapabilityCounts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input map[string]int64
		want  map[string]int64
	}{
		{
			name:  "When counts is nil it should return empty map",
			input: nil,
			want:  map[string]int64{},
		},
		{
			name:  "When empty key is present it should move count to unknown",
			input: map[string]int64{"": 3, "image": 2},
			want:  map[string]int64{CapabilityCountUnknown: 3, "image": 2},
		},
		{
			name:  "When unknown already exists it should add empty-key count",
			input: map[string]int64{"": 1, CapabilityCountUnknown: 2, "package": 4},
			want:  map[string]int64{CapabilityCountUnknown: 3, "package": 4},
		},
		{
			name:  "When no empty key it should leave counts unchanged",
			input: map[string]int64{"image": 1, "package": 2},
			want:  map[string]int64{"image": 1, "package": 2},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var inputCopy map[string]int64
			if tt.input != nil {
				inputCopy = make(map[string]int64, len(tt.input))
				for k, v := range tt.input {
					inputCopy[k] = v
				}
			}
			got := NormalizeCapabilityCounts(tt.input)
			require.Equal(t, tt.want, got)
			require.Equal(t, inputCopy, tt.input, "input map must not be mutated")
		})
	}
}

func TestDeviceOsModeCountKey(t *testing.T) {
	t.Parallel()

	image := domain.OsModeImage
	packageMode := domain.OsModePackage

	tests := []struct {
		name   string
		status *domain.DeviceStatus
		want   string
	}{
		{
			name:   "When status is nil it should return unknown",
			status: nil,
			want:   CapabilityCountUnknown,
		},
		{
			name:   "When capabilities is nil it should return unknown",
			status: &domain.DeviceStatus{},
			want:   CapabilityCountUnknown,
		},
		{
			name: "When osMode is image it should return image",
			status: &domain.DeviceStatus{
				Capabilities: &domain.DeviceCapabilities{OsMode: &image},
			},
			want: "image",
		},
		{
			name: "When osMode is package it should return package",
			status: &domain.DeviceStatus{
				Capabilities: &domain.DeviceCapabilities{OsMode: &packageMode},
			},
			want: "package",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, deviceOsModeCountKey(tt.status))
		})
	}
}
