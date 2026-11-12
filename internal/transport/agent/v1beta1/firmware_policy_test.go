package agenttransportv1beta1

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	agentapi "github.com/flightctl/flightctl/api/agent/v1beta1"
	convert "github.com/flightctl/flightctl/internal/api/convert/v1beta1"
	"github.com/flightctl/flightctl/internal/api_server/middleware"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/firmwarepolicy"
	"github.com/flightctl/flightctl/internal/service/device"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestFirmwarePolicyAuthentication(t *testing.T) {
	org := uuid.New()
	identity := middleware.NewAgentIdentity("device", org.String(), "device", time.Now().Add(time.Hour))
	for _, tc := range []struct {
		name     string
		identity any
		org      uuid.UUID
		device   string
		status   int
	}{
		{"When identity is missing it should deny", nil, org, "device", 401},
		{"When identity has the wrong type it should deny", "user", org, "device", 401},
		{"When identity is typed nil it should deny", (*middleware.AgentIdentity)(nil), org, "device", 401},
		{"When the device differs it should deny", identity, org, "other", 401},
		{"When the organization differs it should deny", identity, uuid.New(), "device", 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			svc := device.NewMockService(ctrl)
			handler := NewAgentTransportHandler(svc, nil, nil, convert.NewConverter(), nil, logrus.New())
			ctx := util.WithOrganizationID(context.Background(), tc.org)
			if tc.identity != nil {
				ctx = context.WithValue(ctx, consts.IdentityCtxKey, tc.identity)
			}
			for _, method := range []string{http.MethodGet, http.MethodPut} {
				req := httptest.NewRequest(method, "/", nil).WithContext(ctx)
				w := httptest.NewRecorder()
				if method == http.MethodGet {
					handler.GetFirmwarePolicy(w, req, tc.device, agentapi.GetFirmwarePolicyParams{})
				} else {
					handler.AcknowledgeFirmwarePolicy(w, req, tc.device)
				}
				require.Equal(t, tc.status, w.Code)
			}
		})
	}
}

func TestFirmwarePolicyAuthenticatedRead(t *testing.T) {
	ctrl := gomock.NewController(t)
	svc := device.NewMockService(ctrl)
	handler := NewAgentTransportHandler(svc, nil, nil, convert.NewConverter(), nil, logrus.New())
	org := uuid.New()
	ctx := context.WithValue(util.WithOrganizationID(context.Background(), org), consts.IdentityCtxKey, middleware.NewAgentIdentity("device", org.String(), "device", time.Now().Add(time.Hour)))
	view := &firmwarepolicy.View{Revision: 1, Digest: firmwarepolicy.Hash([]byte("payload")), Document: []byte("payload")}
	svc.EXPECT().GetFirmwarePolicy(gomock.Any(), org, "device", int64(0), "").Return(view, domain.StatusOK())
	req := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx)
	w := httptest.NewRecorder()
	handler.GetFirmwarePolicy(w, req, "device", agentapi.GetFirmwarePolicyParams{})
	require.Equal(t, 200, w.Code)
	require.Contains(t, w.Body.String(), view.Digest)
}

func TestFirmwarePolicyReceiptValidation(t *testing.T) {
	org := uuid.New()
	ctx := context.WithValue(util.WithOrganizationID(context.Background(), org), consts.IdentityCtxKey, middleware.NewAgentIdentity("device", org.String(), "device", time.Now().Add(time.Hour)))
	for _, body := range []string{`{}`, `{"revision":1,"revision":2}`, `{"revision":1,"unknown":true}`, `{} {}`} {
		ctrl := gomock.NewController(t)
		svc := device.NewMockService(ctrl)
		handler := NewAgentTransportHandler(svc, nil, nil, convert.NewConverter(), nil, logrus.New())
		w := httptest.NewRecorder()
		handler.AcknowledgeFirmwarePolicy(w, httptest.NewRequest(http.MethodPut, "/", strings.NewReader(body)).WithContext(ctx), "device")
		require.Equal(t, 400, w.Code)
	}
}
