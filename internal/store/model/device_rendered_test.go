package model

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestDeviceRenderedConfigResources(t *testing.T) {
	mgr := setupEncryption(t)
	ctx := context.Background()
	const payload = `[{"name":"registry:quay.io","type":"registry","registry":"quay.io","scheme":"https","skipServerVerification":false,"access":[{"scope":"","authentication":{"authType":"docker","username":"device","password":"resource-secret-sentinel"},"consumers":[{"type":"os"}]}]}]`
	for _, tc := range []struct{ name, input string }{
		{"When resources contain credentials it should encrypt and restore them", payload},
		{"When resources are empty it should retain an empty collection", "[]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := &Device{Resource: Resource{Annotations: map[string]string{domain.DeviceAnnotationRenderedVersion: "1"}}, Spec: MakeJSONField(domain.DeviceSpec{}), RenderedConfigResources: MakeJSONField(json.RawMessage(tc.input))}
			require.NoError(t, EncryptionHandlers()[domain.DeviceKind](ctx, d, mgr.ProcessEncryption))
			require.NotContains(t, string(d.RenderedConfigResources.Data), "resource-secret-sentinel")
			ordinary, err := d.ToApiResource()
			require.NoError(t, err)
			require.Nil(t, ordinary.Spec.ConfigResources)
			rendered, err := d.ToApiResource(WithRendered(ctx, nil))
			require.NoError(t, err)
			require.NotNil(t, rendered.Spec.ConfigResources)
			data, err := json.Marshal(rendered.Spec.ConfigResources)
			require.NoError(t, err)
			require.JSONEq(t, tc.input, string(data))
			if tc.input == payload {
				require.Contains(t, string(data), `"skipServerVerification":false`)
			}
		})
	}
	for _, payload := range []string{`{"password":"invalid-resource-sentinel"}`, `"enc:v1:missing:invalid"`} {
		t.Run("When stored resources are invalid it should fail without disclosing contents", func(t *testing.T) {
			d := &Device{Resource: Resource{Annotations: map[string]string{domain.DeviceAnnotationRenderedVersion: "1"}}, RenderedConfigResources: MakeJSONField(json.RawMessage(payload))}
			_, err := d.ToApiResource(WithRendered(ctx, nil))
			require.Error(t, err)
			require.NotContains(t, err.Error(), "invalid-resource-sentinel")
		})
	}
	t.Run("When legacy rows lack resources it should preserve rendered retrieval", func(t *testing.T) {
		d := &Device{Resource: Resource{Annotations: map[string]string{domain.DeviceAnnotationRenderedVersion: "1"}}}
		rendered, err := d.ToApiResource(WithRendered(ctx, nil))
		require.NoError(t, err)
		require.Nil(t, rendered.Spec.ConfigResources)
	})
}
