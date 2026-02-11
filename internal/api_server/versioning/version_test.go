package versioning

import (
	"context"
	"testing"
)

func TestVersion_IsValid(t *testing.T) {
	tests := []struct {
		name    string
		version Version
		want    bool
	}{
		{
			name:    "v1alpha1 is valid",
			version: V1Alpha1,
			want:    true,
		},
		{
			name:    "v1beta1 is valid",
			version: V1Beta1,
			want:    true,
		},
		{
			name:    "empty string is not valid",
			version: "",
			want:    false,
		},
		{
			name:    "unknown version is not valid",
			version: "v999",
			want:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.version.IsValid(); got != tt.want {
				t.Errorf("Version.IsValid() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestContextWithVersion(t *testing.T) {
	ctx := context.Background()

	// Store version in context
	ctx = ContextWithVersion(ctx, V1Beta1)

	// Retrieve version from context
	version, ok := VersionFromContext(ctx)
	if !ok {
		t.Error("VersionFromContext() returned ok = false, expected true")
	}
	if version != V1Beta1 {
		t.Errorf("VersionFromContext() = %v, want %v", version, V1Beta1)
	}
}

func TestVersionFromContext_NotSet(t *testing.T) {
	ctx := context.Background()

	// Try to retrieve version from context without setting it
	version, ok := VersionFromContext(ctx)
	if ok {
		t.Error("VersionFromContext() returned ok = true, expected false")
	}
	if version != "" {
		t.Errorf("VersionFromContext() = %v, want empty string", version)
	}
}
