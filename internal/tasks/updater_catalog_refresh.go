package tasks

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/flightctl/flightctl/internal/instrumentation/tracing"
	"github.com/flightctl/flightctl/internal/rendered"
	catalogservice "github.com/flightctl/flightctl/internal/service/updatercatalog"
	"github.com/sirupsen/logrus"
)

type UpdaterCatalogRefresh struct {
	catalog  *catalogservice.Service
	notifier *rendered.FirmwarePolicyNotifier
	log      logrus.FieldLogger
}

func NewUpdaterCatalogRefresh(catalog *catalogservice.Service, notifier *rendered.FirmwarePolicyNotifier, log logrus.FieldLogger) (*UpdaterCatalogRefresh, error) {
	if catalog == nil || notifier == nil || log == nil {
		return nil, errors.New("updater catalog refresh requires catalog, notifier and logger")
	}
	return &UpdaterCatalogRefresh{catalog: catalog, notifier: notifier, log: log}, nil
}

func (t *UpdaterCatalogRefresh) DispatchPending(ctx context.Context, limit int) (bool, error) {
	if limit < 1 || limit > 256 {
		return false, errors.New("policy refresh limit must be between 1 and 256")
	}
	ctx, span := tracing.StartSpan(ctx, "flightctl/tasks/updatercatalog", "DispatchPending")
	defer span.End()
	work, err := t.catalog.ExpandPendingRefresh(ctx, limit)
	if err != nil {
		return false, fmt.Errorf("expand pending policy refresh: %w", err)
	}
	pending, err := t.catalog.PendingNotifications(ctx, limit)
	if err != nil {
		return work, fmt.Errorf("list pending policy notifications: %w", err)
	}
	if len(pending) == 0 {
		return work, nil
	}
	revision, err := t.catalog.CurrentRevision(ctx)
	if err != nil {
		return work, fmt.Errorf("read current policy revision: %w", err)
	}
	for _, target := range pending {
		if revision < target.Revision {
			return work, errors.New("accepted policy revision is older than pending delivery")
		}
		if err = t.notifier.Notify(ctx, target.OrgID, target.DeviceName, revision); err != nil {
			return work, fmt.Errorf("publish policy notification for %s/%s: %w", target.OrgID, target.DeviceName, err)
		}
		if err = t.catalog.MarkNotified(ctx, target.OrgID, target.DeviceName, revision); err != nil {
			return work, fmt.Errorf("mark policy notification for %s/%s: %w", target.OrgID, target.DeviceName, err)
		}
	}
	return true, nil
}

func (t *UpdaterCatalogRefresh) Run(ctx context.Context) {
	retry := time.NewTicker(5 * time.Second)
	defer retry.Stop()
	for ctx.Err() == nil {
		dispatchCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		work, err := t.DispatchPending(dispatchCtx, 256)
		cancel()
		if err != nil && ctx.Err() == nil {
			t.log.WithError(err).Error("failed to refresh updater catalog policy; durable work retained")
		}
		if work && err == nil {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-retry.C:
		}
	}
}
