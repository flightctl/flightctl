package device

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/flightctl/flightctl/internal/contextutil"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/service/common"
	"github.com/flightctl/flightctl/internal/store"
	devicestore "github.com/flightctl/flightctl/internal/store/device"
	"github.com/flightctl/flightctl/internal/util/validation"
	"github.com/google/uuid"
)

var errDeviceCredentialNotRevoked = errors.New("device has no revoked enrollment credential flag to dismiss")

func (h *DeviceServiceHandler) OverrideDeviceEnrollmentCredential(ctx context.Context, orgId uuid.UUID, name string, request domain.DeviceEnrollmentCredentialOverrideRequest) (*domain.Device, domain.Status) {
	reason, validationErr := validation.NormalizeEnrollmentCredentialAuditReason(request.Reason)
	if validationErr != nil {
		return nil, domain.StatusBadRequest(validationErr.Error())
	}
	identity, ok := contextutil.GetMappedIdentityFromContext(ctx)
	if !ok || identity == nil || strings.TrimSpace(identity.GetUsername()) == "" {
		return nil, domain.StatusForbidden("an authenticated identity with a username is required to dismiss a revoked credential flag")
	}
	result, _, _, err := h.deviceStore.Mutate(ctx, orgId, name, nil, func(m *devicestore.DeviceMutation) error {
		if err := m.RequireExisting(); err != nil {
			return err
		}
		status := m.Device.Status
		if status == nil || status.EnrollmentCredential == nil || !status.EnrollmentCredential.Revoked {
			return errDeviceCredentialNotRevoked
		}
		if status.EnrollmentCredential.Override != nil {
			return store.ErrMutateSkipWrite
		}
		status.EnrollmentCredential.Override = &domain.DeviceEnrollmentCredentialOverride{
			OverriddenAt: time.Now().UTC(),
			OverriddenBy: identity.GetUsername(),
			Reason:       reason,
		}
		domain.RemoveStatusCondition(&status.Conditions, domain.ConditionTypeDeviceEnrollmentCredentialRevoked)
		return nil
	})
	if errors.Is(err, errDeviceCredentialNotRevoked) {
		return nil, domain.StatusConflict(err.Error())
	}
	if err != nil {
		return nil, common.StoreErrorToApiStatus(err, false, domain.DeviceKind, &name)
	}
	return result, domain.StatusOK()
}
