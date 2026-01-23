package helm

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSanitizeReleaseName(t *testing.T) {
	testCases := []struct {
		name      string
		chartName string
		version   string
		want      string
	}{
		{
			name:      "simple chart and version",
			chartName: "nginx",
			version:   "1.0.0",
			want:      "nginx-1-0-0",
		},
		{
			name:      "chart with hyphens",
			chartName: "my-web-app",
			version:   "2.1.3",
			want:      "my-web-app-2-1-3",
		},
		{
			name:      "uppercase converted to lowercase",
			chartName: "My_Chart",
			version:   "v1.2",
			want:      "my-chart-v1-2",
		},
		{
			name:      "version with rc suffix",
			chartName: "app",
			version:   "1.0.0-rc1",
			want:      "app-1-0-0-rc1",
		},
		{
			name:      "digest version gets truncated",
			chartName: "chart",
			version:   "sha256:a3ed95caeb02ffe68cdd9fd84406680ae93d633cb16422d00e8a7c22955b46d4",
			want:      "chart-sha256-a3ed95caeb02ffe68cdd9fd84406680ae93d633c",
		},
		{
			name:      "dots converted to hyphens",
			chartName: "my.dotted.chart",
			version:   "1.0",
			want:      "my-dotted-chart-1-0",
		},
		{
			name:      "underscores converted to hyphens",
			chartName: "my_chart_name",
			version:   "1.0.0",
			want:      "my-chart-name-1-0-0",
		},
		{
			name:      "long name truncated to 53 chars",
			chartName: "a-very-long-chart-name-that-will-exceed-the-limit",
			version:   "1.0.0",
			want:      "a-very-long-chart-name-that-will-exceed-the-limit-1-0",
		},
		{
			name:      "trailing hyphens removed after truncation",
			chartName: "chart-name-with-trailing",
			version:   "1-0-0-0-0-0-0-0-0-0-0-0-0-0-0-0",
			want:      "chart-name-with-trailing-1-0-0-0-0-0-0-0-0-0-0-0-0-0",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)

			result := SanitizeReleaseName(tc.chartName, tc.version)

			require.Equal(tc.want, result)
			require.LessOrEqual(len(result), helmReleaseNameMaxLength)
		})
	}
}
