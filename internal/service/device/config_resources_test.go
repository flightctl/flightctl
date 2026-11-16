package device

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/flightctl/flightctl/internal/domain"
	devicestore "github.com/flightctl/flightctl/internal/store/device"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

func TestUpdateRenderedConfigResources(t *testing.T) {
	const payload = `[{"name":"registry:quay.io","type":"registry","registry":"quay.io","scheme":"https","skipServerVerification":false,"access":[{"scope":"","consumers":[{"type":"os"}]}]}]`
	for _, tc := range []struct {
		name, input, previous string
		valid                 bool
	}{
		{"When content is unchanged it should keep the rendered version", payload, payload, true},
		{"When JSON formatting differs it should keep the rendered version", `[{"registry":"quay.io", "type":"registry", "name":"registry:quay.io", "scheme":"https", "access":[{"consumers":[{"type":"os"}],"scope":""}],"skipServerVerification":false}]`, payload, true},
		{"When empty content is omitted it should keep the rendered version", `null`, `[]`, true},
		{"When omitted content becomes empty it should keep the rendered version", `[]`, ``, true},
		{"When required fields are missing it should reject without publishing", `[{"type":"registry","name":"secret-resource-sentinel"}]`, payload, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, _, svc := newTestHandler()
			ctx := context.Background()
			org := uuid.New()
			var logs bytes.Buffer
			logger := logrus.New()
			logger.SetOutput(&logs)
			svc.(*DeviceServiceHandler).log = logger
			_, err := st.device.Create(ctx, org, &domain.Device{Metadata: domain.ObjectMeta{Name: lo.ToPtr("device"), Annotations: &map[string]string{domain.DeviceAnnotationRenderedVersion: "1", domain.DeviceAnnotationRenderedSpecHash: "hash"}}, Spec: &domain.DeviceSpec{}, Status: lo.ToPtr(domain.NewDeviceStatus())}, &devicestore.DeviceRendered{ConfigResources: tc.previous})
			require.NoError(t, err)
			var input []domain.ConfigResource
			require.NoError(t, json.Unmarshal([]byte(tc.input), &input))
			status := svc.UpdateRenderedDevice(ctx, org, "device", "[]", "[]", "hash", "", input, nil, false, nil)
			if tc.valid {
				require.Equal(t, int32(http.StatusOK), status.Code)
			} else {
				require.NotEqual(t, int32(http.StatusOK), status.Code)
			}
			require.NotContains(t, status.Message, "secret-resource-sentinel")
			require.NotContains(t, logs.String(), "secret-resource-sentinel")
			stored, err := st.device.Get(ctx, org, "device")
			require.NoError(t, err)
			require.Equal(t, "1", stored.Version())
			require.Nil(t, stored.Spec.ConfigResources)
			require.Equal(t, tc.previous, st.device.rendered["device"].ConfigResources)
		})
	}
}
