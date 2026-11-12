package device

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/firmwarepolicy"
	"github.com/flightctl/flightctl/internal/rendered"
	"github.com/flightctl/flightctl/internal/service/common"
	catalogservice "github.com/flightctl/flightctl/internal/service/updatercatalog"
	"github.com/google/uuid"
)

func WithFirmwarePolicy(catalog *catalogservice.Service, hub *rendered.FirmwarePolicyHub, wait time.Duration) HandlerOption {
	return func(h *DeviceServiceHandler) {
		h.policyCatalog = catalog
		h.policyHub = hub
		h.policyWait = wait
		if h.policyWait <= 0 {
			h.policyWait = 120 * time.Second
		}
	}
}

func policyError(err error, name string) domain.Status {
	if errors.Is(err, catalogservice.ErrPolicyConflict) {
		return domain.StatusConflict("policy revision or digest differs from accepted head")
	}
	status := common.StoreErrorToApiStatus(err, false, domain.DeviceKind, &name)
	if status.Code == http.StatusNotFound {
		return status
	}
	return domain.StatusServiceUnavailable("updater authorization policy is unavailable")
}

func (h *DeviceServiceHandler) GetFirmwarePolicy(ctx context.Context, org uuid.UUID, name string, knownRevision int64, knownDigest string) (*firmwarepolicy.View, domain.Status) {
	if h.policyCatalog == nil || h.policyHub == nil {
		return nil, domain.StatusServiceUnavailable("updater authorization policy is unavailable")
	}
	if knownRevision != 0 || knownDigest != "" {
		if err := (firmwarepolicy.Receipt{Revision: knownRevision, Digest: knownDigest}).Validate(); err != nil {
			return nil, domain.StatusBadRequest(err.Error())
		}
	}
	hints, unwatch := h.policyHub.Watch(org, name)
	defer unwatch()
	timer := time.NewTimer(h.policyWait)
	defer timer.Stop()
	expired := false
	for {
		head, err := h.policyCatalog.PolicyHeadForDevice(ctx, org, name)
		if err != nil {
			return nil, policyError(err, name)
		}
		if knownRevision > head.Revision || (knownRevision == head.Revision && knownDigest != head.Digest) {
			return nil, domain.StatusConflict("policy revision or digest differs from accepted head")
		}
		if head.Revision != knownRevision {
			view, err := h.policyCatalog.PolicyForDevice(ctx, org, name)
			if err != nil {
				return nil, policyError(err, name)
			}
			return &view, domain.StatusOK()
		}
		if expired {
			return nil, domain.StatusNoContent()
		}
		select {
		case <-ctx.Done():
			return nil, domain.StatusServiceUnavailable("policy request canceled")
		case <-timer.C:
			expired = true
		case <-hints:
		}
	}
}

func (h *DeviceServiceHandler) AcknowledgeFirmwarePolicy(ctx context.Context, org uuid.UUID, name string, receipt firmwarepolicy.Receipt) domain.Status {
	if h.policyCatalog == nil {
		return domain.StatusServiceUnavailable("updater authorization policy is unavailable")
	}
	if err := receipt.Validate(); err != nil {
		return domain.StatusBadRequest(err.Error())
	}
	if err := h.policyCatalog.AcknowledgePolicy(ctx, org, name, receipt); err != nil {
		return policyError(err, name)
	}
	return domain.StatusNoContent()
}
