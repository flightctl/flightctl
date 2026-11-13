package updatercatalog

import (
	"context"

	"github.com/flightctl/flightctl/internal/firmwarepolicy"
	"github.com/flightctl/flightctl/internal/instrumentation/tracing"
	catalogstore "github.com/flightctl/flightctl/internal/store/updatercatalog"
	"github.com/google/uuid"
)

type PolicyNotification struct {
	OrgID      uuid.UUID
	DeviceName string
	Revision   int64
}

func (s *Service) ExpandPendingRefresh(ctx context.Context, limit int) (bool, error) {
	ctx, span := tracing.StartSpan(ctx, "flightctl/service/updatercatalog", "ExpandPendingRefresh")
	defer span.End()
	return s.store.ExpandPendingRefresh(ctx, limit)
}
func (s *Service) PendingNotifications(ctx context.Context, limit int) ([]PolicyNotification, error) {
	ctx, span := tracing.StartSpan(ctx, "flightctl/service/updatercatalog", "PendingNotifications")
	defer span.End()
	records, err := s.store.PendingNotifications(ctx, limit)
	if err != nil {
		return nil, err
	}
	result := make([]PolicyNotification, 0, len(records))
	for _, r := range records {
		result = append(result, PolicyNotification{OrgID: r.OrgID, DeviceName: r.DeviceName, Revision: r.Revision})
	}
	return result, nil
}
func (s *Service) MarkNotified(ctx context.Context, orgID uuid.UUID, name string, revision int64) error {
	ctx, span := tracing.StartSpan(ctx, "flightctl/service/updatercatalog", "MarkNotified")
	defer span.End()
	return s.store.MarkNotified(ctx, orgID, name, revision)
}
func (s *Service) CurrentRevision(ctx context.Context) (int64, error) {
	ctx, span := tracing.StartSpan(ctx, "flightctl/service/updatercatalog", "CurrentRevision")
	defer span.End()
	return s.store.CurrentRevision(ctx)
}

func (s *Service) PolicyForDevice(ctx context.Context, org uuid.UUID, name string) (firmwarepolicy.View, error) {
	ctx, span := tracing.StartSpan(ctx, "flightctl/service/updatercatalog", "PolicyForDevice")
	defer span.End()
	return s.store.PolicyForDevice(ctx, org, name)
}
func (s *Service) AcknowledgePolicy(ctx context.Context, org uuid.UUID, name string, receipt firmwarepolicy.Receipt) error {
	ctx, span := tracing.StartSpan(ctx, "flightctl/service/updatercatalog", "AcknowledgePolicy")
	defer span.End()
	return s.store.AcknowledgePolicy(ctx, org, name, receipt)
}

var ErrPolicyConflict = catalogstore.ErrPolicyConflict

func (s *Service) PolicyHeadForDevice(ctx context.Context, org uuid.UUID, name string) (firmwarepolicy.Receipt, error) {
	ctx, span := tracing.StartSpan(ctx, "flightctl/service/updatercatalog", "PolicyHeadForDevice")
	defer span.End()
	return s.store.PolicyHeadForDevice(ctx, org, name)
}
