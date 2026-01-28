package helm

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSanitizeReleaseName(t *testing.T) {
	testCases := []struct {
		name     string
		chartRef string
		want     string
	}{
		{
			name:     "simple chart and version",
			chartRef: "registry.io/nginx:1.0.0",
			want:     "nginx-1-0-0",
		},
		{
			name:     "chart with hyphens",
			chartRef: "registry.io/my-web-app:2.1.3",
			want:     "my-web-app-2-1-3",
		},
		{
			name:     "uppercase converted to lowercase",
			chartRef: "registry.io/charts/my-chart:v1.2",
			want:     "my-chart-v1-2",
		},
		{
			name:     "version with rc suffix",
			chartRef: "registry.io/app:1.0.0-rc1",
			want:     "app-1-0-0-rc1",
		},
		{
			name:     "digest version gets truncated",
			chartRef: "registry.io/chart@sha256:a3ed95caeb02ffe68cdd9fd84406680ae93d633cb16422d00e8a7c22955b46d4",
			want:     "chart-sha256-a3ed95caeb02ffe68cdd9fd84406680ae93d633c",
		},
		{
			name:     "dots in chart name converted to hyphens",
			chartRef: "registry.io/my.dotted.chart:1.0",
			want:     "my-dotted-chart-1-0",
		},
		{
			name:     "underscores in chart name converted to hyphens",
			chartRef: "registry.io/my_chart_name:1.0.0",
			want:     "my-chart-name-1-0-0",
		},
		{
			name:     "long name truncated to 53 chars",
			chartRef: "registry.io/a-very-long-chart-name-that-will-exceed-the-limit:1.0.0",
			want:     "a-very-long-chart-name-that-will-exceed-the-limit-1-0",
		},
		{
			name:     "trailing hyphens removed after truncation",
			chartRef: "registry.io/chart-name-with-trailing:1-0-0-0-0-0-0-0-0-0-0-0-0-0-0-0",
			want:     "chart-name-with-trailing-1-0-0-0-0-0-0-0-0-0-0-0-0-0",
		},
		{
			name:     "oci prefix is stripped",
			chartRef: "oci://registry.io/charts/nginx:1.0.0",
			want:     "nginx-1-0-0",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)

			result, err := SanitizeReleaseName(tc.chartRef)

			require.NoError(err)
			require.Equal(tc.want, result)
			require.LessOrEqual(len(result), helmReleaseNameMaxLength)
		})
	}
}
