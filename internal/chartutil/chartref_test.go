package chartutil

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseChartRef(t *testing.T) {
	testCases := []struct {
		name        string
		chartRef    string
		wantName    string
		wantVersion string
		wantErr     bool
	}{
		{
			name:        "tag-based OCI reference",
			chartRef:    "oci://registry.example.com/charts/myapp:1.0.0",
			wantName:    "myapp",
			wantVersion: "1.0.0",
			wantErr:     false,
		},
		{
			name:        "tag-based reference with complex path",
			chartRef:    "oci://quay.io/flightctl/helm-charts/webapp:2.1.3",
			wantName:    "webapp",
			wantVersion: "2.1.3",
			wantErr:     false,
		},
		{
			name:        "digest-based OCI reference",
			chartRef:    "oci://registry.example.com/charts/myapp@sha256:a3ed95caeb02ffe68cdd9fd84406680ae93d633cb16422d00e8a7c22955b46d4",
			wantName:    "myapp",
			wantVersion: "sha256:a3ed95caeb02ffe68cdd9fd84406680ae93d633cb16422d00e8a7c22955b46d4",
			wantErr:     false,
		},
		{
			name:        "reference without version or digest",
			chartRef:    "oci://registry.example.com/charts/myapp",
			wantName:    "",
			wantVersion: "",
			wantErr:     true,
		},
		{
			name:        "reference with both tag and digest",
			chartRef:    "oci://registry.example.com/charts/myapp:1.0.0@sha256:a3ed95caeb02ffe68cdd9fd84406680ae93d633cb16422d00e8a7c22955b46d4",
			wantName:    "myapp",
			wantVersion: "sha256:a3ed95caeb02ffe68cdd9fd84406680ae93d633cb16422d00e8a7c22955b46d4",
			wantErr:     false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)

			name, version, err := ParseChartRef(tc.chartRef)

			if tc.wantErr {
				require.Error(err)
				return
			}

			require.NoError(err)
			require.Equal(tc.wantName, name)
			require.Equal(tc.wantVersion, version)
		})
	}
}

func TestSplitChartRef(t *testing.T) {
	testCases := []struct {
		name        string
		chartRef    string
		wantPath    string
		wantVersion string
	}{
		{
			name:        "tag-based OCI reference",
			chartRef:    "oci://registry.example.com/charts/myapp:1.0.0",
			wantPath:    "oci://registry.example.com/charts/myapp",
			wantVersion: "1.0.0",
		},
		{
			name:        "digest-based OCI reference returns full ref with empty version",
			chartRef:    "oci://registry.example.com/charts/myapp@sha256:a3ed95caeb02ffe68cdd9fd84406680ae93d633cb16422d00e8a7c22955b46d4",
			wantPath:    "oci://registry.example.com/charts/myapp@sha256:a3ed95caeb02ffe68cdd9fd84406680ae93d633cb16422d00e8a7c22955b46d4",
			wantVersion: "",
		},
		{
			name:        "reference without version or digest",
			chartRef:    "oci://registry.example.com/charts/myapp",
			wantPath:    "oci://registry.example.com/charts/myapp",
			wantVersion: "",
		},
		{
			name:        "non-OCI reference with tag",
			chartRef:    "registry.example.com/charts/myapp:1.0.0",
			wantPath:    "registry.example.com/charts/myapp",
			wantVersion: "1.0.0",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)

			path, version := SplitChartRef(tc.chartRef)

			require.Equal(tc.wantPath, path)
			require.Equal(tc.wantVersion, version)
		})
	}
}
