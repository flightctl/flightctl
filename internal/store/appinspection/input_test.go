package appinspection_test

import (
	"testing"

	"github.com/flightctl/flightctl/internal/store/appinspection"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/google/uuid"
	"github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/require"
)

func TestKeyForInput(t *testing.T) {
	orgID := uuid.New()
	inline := model.ApplicationInspectionInput{
		Provider: model.InspectionProviderCompose, Format: model.InspectionFormatInline,
		InlineFingerprint: model.InspectionFingerprint{1},
	}
	packaged := model.ApplicationInspectionInput{
		Provider: model.InspectionProviderQuadlet, Format: model.InspectionFormatTar,
		ParentDigest: digest.FromString("package"), Platform: "linux/arm64/v8",
	}
	chart := model.ApplicationInspectionInput{
		Provider: model.InspectionProviderHelm, Format: model.InspectionFormatChart,
		ParentDigest: digest.FromString("chart"),
		Helm:         &model.InspectionHelmIdentity{ReleaseName: "release", Namespace: "default"},
	}
	for _, tt := range []struct {
		name   string
		input  model.ApplicationInspectionInput
		change func(*model.ApplicationInspectionInput)
	}{
		{"provider", inline, func(in *model.ApplicationInspectionInput) { in.Provider = model.InspectionProviderQuadlet }},
		{"inline content", inline, func(in *model.ApplicationInspectionInput) { in.InlineFingerprint[0]++ }},
		{"format", packaged, func(in *model.ApplicationInspectionInput) { in.Format = model.InspectionFormatTarGz }},
		{"parent digest", packaged, func(in *model.ApplicationInspectionInput) { in.ParentDigest = digest.FromString("changed") }},
		{"platform", packaged, func(in *model.ApplicationInspectionInput) { in.Platform = "linux/amd64" }},
		{"platform variant", packaged, func(in *model.ApplicationInspectionInput) { in.Platform = "linux/arm64/v9" }},
		{"Helm values", chart, func(in *model.ApplicationInspectionInput) { in.Helm.Values[0]++ }},
		{"Helm dependencies", chart, func(in *model.ApplicationInspectionInput) { in.Helm.Dependencies[0]++ }},
		{"Helm context", chart, func(in *model.ApplicationInspectionInput) { in.Helm.RenderContext[0]++ }},
		{"Helm release", chart, func(in *model.ApplicationInspectionInput) { in.Helm.ReleaseName = "changed" }},
		{"Helm namespace", chart, func(in *model.ApplicationInspectionInput) { in.Helm.Namespace = "changed" }},
	} {
		t.Run("When the "+tt.name+" changes it should produce a distinct key", func(t *testing.T) {
			base, err := appinspection.KeyForInput(orgID, tt.input)
			require.NoError(t, err)
			other := tt.input
			if other.Helm != nil {
				identity := *other.Helm
				other.Helm = &identity
			}
			same, err := appinspection.KeyForInput(orgID, other)
			require.NoError(t, err)
			require.Equal(t, base, same)
			tt.change(&other)
			changed, err := appinspection.KeyForInput(orgID, other)
			require.NoError(t, err)
			require.NotEqual(t, base, changed)
		})
	}
	t.Run("When an input is identical across organizations it should have different keys", func(t *testing.T) {
		a, err := appinspection.KeyForInput(orgID, inline)
		require.NoError(t, err)
		b, err := appinspection.KeyForInput(uuid.New(), inline)
		require.NoError(t, err)
		require.NotEqual(t, a, b)
	})
	for _, tt := range []struct {
		name   string
		input  model.ApplicationInspectionInput
		change func(*model.ApplicationInspectionInput)
	}{
		{"provider", inline, func(in *model.ApplicationInspectionInput) { in.Provider = "secret-value" }},
		{"format", inline, func(in *model.ApplicationInspectionInput) { in.Format = "secret-value" }},
		{"inline fingerprint", inline, func(in *model.ApplicationInspectionInput) { in.InlineFingerprint = model.InspectionFingerprint{} }},
		{"mixed identity", inline, func(in *model.ApplicationInspectionInput) { in.ParentDigest = digest.FromString("mixed") }},
		{"inline platform", inline, func(in *model.ApplicationInspectionInput) { in.Platform = "linux/amd64" }},
		{"digest", packaged, func(in *model.ApplicationInspectionInput) { in.ParentDigest = "secret-value" }},
		{"platform", packaged, func(in *model.ApplicationInspectionInput) { in.Platform = "secret-value" }},
		{"chart identity", chart, func(in *model.ApplicationInspectionInput) { in.Helm = nil }},
		{"chart format", chart, func(in *model.ApplicationInspectionInput) { in.Format = model.InspectionFormatRaw }},
		{"chart context for non-Helm input", inline, func(in *model.ApplicationInspectionInput) { in.Helm = chart.Helm }},
		{"chart name", chart, func(in *model.ApplicationInspectionInput) {
			in.Helm = &model.InspectionHelmIdentity{ReleaseName: "\nsecret-value", Namespace: "default"}
		}},
	} {
		t.Run("When the "+tt.name+" is invalid it should reject it without echoing it", func(t *testing.T) {
			tt.change(&tt.input)
			_, err := appinspection.KeyForInput(orgID, tt.input)
			require.Error(t, err)
			require.NotContains(t, err.Error(), "secret-value")
		})
	}
	t.Run("When using the default organization it should accept its zero UUID", func(t *testing.T) {
		key, err := appinspection.KeyForInput(uuid.Nil, inline)
		require.NoError(t, err)
		require.NotEmpty(t, key)
	})
}
