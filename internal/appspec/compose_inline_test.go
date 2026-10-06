package appspec

import (
	"testing"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/api/common"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
)

func TestParseComposeFromSpec(t *testing.T) {
	testCases := []struct {
		name      string
		contents  []v1beta1.ApplicationContent
		wantImage string
		wantError error
	}{
		{
			name:      "When no base file exists it should preserve the missing-file error",
			wantError: common.ErrNoComposeFile,
		},
		{
			name: "When a base file has no services it should preserve the missing-services error",
			contents: []v1beta1.ApplicationContent{
				{Path: "docker-compose.yaml", Content: lo.ToPtr("services: {}")},
			},
			wantError: common.ErrNoComposeServices,
		},
		{
			name: "When multiple base files exist it should keep the first matching base",
			contents: []v1beta1.ApplicationContent{
				{Path: "podman-compose.yml", Content: lo.ToPtr("services:\n  app:\n    image: first:v1")},
				{Path: "docker-compose.yaml", Content: lo.ToPtr("services:\n  app:\n    image: second:v2")},
			},
			wantImage: "first:v1",
		},
		{
			name: "When overrides precede and follow the base it should apply only later overrides",
			contents: []v1beta1.ApplicationContent{
				{Path: "docker-compose.override.yaml", Content: lo.ToPtr("services:\n  app:\n    image: early:v1")},
				{Path: "docker-compose.yaml", Content: lo.ToPtr("services:\n  app:\n    image: base:v2")},
				{Path: "podman-compose.override.yml", Content: lo.ToPtr("services:\n  app:\n    image: late:v3")},
			},
			wantImage: "late:v3",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			require := require.New(t)
			spec, err := ParseComposeFromSpec(testCase.contents)
			if testCase.wantError != nil {
				require.ErrorIs(err, testCase.wantError)
				require.Nil(spec)
				return
			}
			require.NoError(err)
			require.Equal(testCase.wantImage, spec.Services["app"].Image)
		})
	}
}
