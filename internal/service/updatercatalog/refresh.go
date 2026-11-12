package updatercatalog

import (
	"context"

	"github.com/flightctl/flightctl/internal/instrumentation/tracing"
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
