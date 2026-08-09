package tasks

import (
	"testing"
)

func TestIsRHEL10Image(t *testing.T) {
	tests := []struct {
		name            string
		sourceImageName string
		expected        bool
	}{
		{
			name:            "When source image is rhel10 bootc it should return true",
			sourceImageName: "rhel10/rhel-bootc",
			expected:        true,
		},
		{
			name:            "When source image has rhel10 prefix it should return true",
			sourceImageName: "rhel10-beta/rhel-bootc",
			expected:        true,
		},
		{
			name:            "When source image contains rhel10 in path it should return true",
			sourceImageName: "some-org/rhel10/bootc",
			expected:        true,
		},
		{
			name:            "When source image has uppercase RHEL10 it should return true",
			sourceImageName: "RHEL10/rhel-bootc",
			expected:        true,
		},
		{
			name:            "When source image is rhel9 bootc it should return false",
			sourceImageName: "rhel9/rhel-bootc",
			expected:        false,
		},
		{
			name:            "When source image is centos bootc it should return false",
			sourceImageName: "centos-bootc/centos-bootc",
			expected:        false,
		},
		{
			name:            "When source image is fedora bootc it should return false",
			sourceImageName: "fedora/fedora-bootc",
			expected:        false,
		},
		{
			name:            "When source image is empty it should return false",
			sourceImageName: "",
			expected:        false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isRHEL10Image(tt.sourceImageName)
			if result != tt.expected {
				t.Errorf("isRHEL10Image(%q) = %v, want %v", tt.sourceImageName, result, tt.expected)
			}
		})
	}
}
