package device

import (
	"context"
	"net/http"
	"testing"

	"github.com/flightctl/flightctl/internal/domain"
	devicestore "github.com/flightctl/flightctl/internal/store/device"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
)

func TestUpdateRenderedConfigResources(t *testing.T) {
	const payload = `[{"name":"registry:quay.io","type":"registry","registry":"quay.io","scheme":"https","skipServerVerification":false,"access":[{"scope":"","consumers":[{"type":"os"}]}]}]`
	for _, tc := range []struct {
		name, config, applications, resources string
	}{
		{"When rendered payloads are unchanged it should keep the rendered version", "[]", "[]", "[]"},
		{"When resources change without a publication signal it should keep the rendered version", "[]", "[]", payload},
		{"When rendered payloads change without a publication signal it should keep the rendered version", "changed-config", "changed-applications", payload},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, _, svc := newTestHandler()
			ctx := context.Background()
			org := uuid.New()
			_, err := st.device.Create(ctx, org, &domain.Device{Metadata: domain.ObjectMeta{Name: lo.ToPtr("device"), Annotations: &map[string]string{domain.DeviceAnnotationRenderedVersion: "1", domain.DeviceAnnotationRenderedSpecHash: "hash"}}, Spec: &domain.DeviceSpec{}, Status: lo.ToPtr(domain.NewDeviceStatus())}, &devicestore.DeviceRendered{Config: "[]", Applications: "[]", ConfigResources: "[]"})
			require.NoError(t, err)
			status := svc.UpdateRenderedDevice(ctx, org, "device", tc.config, tc.applications, tc.resources, "hash", "", nil, false, nil)
			require.Equal(t, int32(http.StatusOK), status.Code)
			stored, err := st.device.Get(ctx, org, "device")
			require.NoError(t, err)
			require.Equal(t, "1", stored.Version())
			require.Nil(t, stored.Spec.ConfigResources)
			require.Equal(t, "[]", st.device.rendered["device"].Config)
			require.Equal(t, "[]", st.device.rendered["device"].Applications)
			require.Equal(t, "[]", st.device.rendered["device"].ConfigResources)
		})
	}
}
