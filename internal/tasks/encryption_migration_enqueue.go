package tasks

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/instrumentation/encryption"
	"github.com/flightctl/flightctl/internal/worker_client"
	"github.com/flightctl/flightctl/pkg/queues"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
)

// Internal worker reason for encryption migration batches. Not an API EventReason;
// messages are enqueued directly onto task-queue and are not persisted as Events.
const EventReasonEncryptionMigrationBatch domain.EventReason = "EncryptionMigrationBatch"

const encryptionMigrationEnqueueTimeout = 30 * time.Second

// IncompleteWork returns kind/org pairs that still need migration for the active key.
func (m *EncryptionMigrator) IncompleteWork(ctx context.Context) ([]EncryptionMigrationWork, error) {
	if m.manager == nil {
		return nil, fmt.Errorf("encryption migration: encryption manager is nil")
	}
	_, strategy := m.manager.GetActiveStrategy()
	if strategy == nil {
		return nil, encryption.ErrNoActiveStrategy
	}
	activeKeyID := strategy.ActiveKeyID()
	registryHash := m.currentRegistryHash()

	work := make([]EncryptionMigrationWork, 0)
	for kind := range m.resources {
		err := m.forEachOrgID(ctx, func(orgID uuid.UUID) error {
			checkpoint, err := m.loadCheckpoint(ctx, kind, orgID)
			if err != nil {
				return err
			}
			if !checkpoint.Complete || !checkpointMatchesMigrationTarget(checkpoint, activeKeyID, registryHash) {
				work = append(work, EncryptionMigrationWork{Kind: kind, OrgID: orgID})
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return work, nil
}

// EnqueueEncryptionMigration enqueues one batch job for the given kind and org.
func EnqueueEncryptionMigration(ctx context.Context, publisher queues.QueueProducer, kind string, orgID uuid.UUID) error {
	if publisher == nil {
		return fmt.Errorf("encryption migration: queue publisher is nil")
	}
	if kind == "" {
		return fmt.Errorf("encryption migration: kind is required")
	}
	event := domain.GetBaseEvent(
		ctx,
		domain.ResourceKind(kind),
		encryptionMigrationResourceName,
		EventReasonEncryptionMigrationBatch,
		fmt.Sprintf("Run encryption migration batch for %s org %s", kind, orgID),
		nil,
	)
	payload, err := json.Marshal(worker_client.EventWithOrgId{
		OrgId: orgID,
		Event: *event,
	})
	if err != nil {
		return fmt.Errorf("marshal encryption migration event: %w", err)
	}
	if err := publisher.Enqueue(ctx, payload, time.Now().UnixMicro()); err != nil {
		return fmt.Errorf("enqueue encryption migration for %s org %s: %w", kind, orgID, err)
	}
	return nil
}

// enqueueEncryptionMigrationAfter re-enqueues after delay without blocking the consumer.
// BackoffUntil in the checkpoint remains durable across restarts; worker start re-enqueues
// incomplete work and RunBatch re-honors the backoff.
func enqueueEncryptionMigrationAfter(ctx context.Context, publisher queues.QueueProducer, kind string, orgID uuid.UUID, delay time.Duration, log logrus.FieldLogger) {
	if ctx == nil {
		ctx = context.Background()
	}
	if delay <= 0 {
		enqueueCtx, cancel := context.WithTimeout(ctx, encryptionMigrationEnqueueTimeout)
		defer cancel()
		if err := EnqueueEncryptionMigration(enqueueCtx, publisher, kind, orgID); err != nil && log != nil {
			log.WithError(err).Errorf("encryption migration: delayed enqueue failed for %s org %s", kind, orgID)
		}
		return
	}
	go func() {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		enqueueCtx, cancel := context.WithTimeout(ctx, encryptionMigrationEnqueueTimeout)
		defer cancel()
		if err := EnqueueEncryptionMigration(enqueueCtx, publisher, kind, orgID); err != nil && log != nil {
			log.WithError(err).Errorf("encryption migration: delayed enqueue failed for %s org %s", kind, orgID)
		}
	}()
}

// EnqueueEncryptionMigrationIfNeeded enqueues a batch for each incomplete kind/org.
// The incomplete kind/org list is materialized first (see IncompleteWork), then enqueued.
func EnqueueEncryptionMigrationIfNeeded(ctx context.Context, publisher queues.QueueProducer, migrator *EncryptionMigrator, log logrus.FieldLogger) error {
	if migrator == nil {
		return fmt.Errorf("encryption migration: migrator is nil")
	}
	if migrator.manager == nil {
		return fmt.Errorf("encryption migration: encryption manager is nil")
	}
	work, err := migrator.IncompleteWork(ctx)
	if err != nil {
		return err
	}
	for _, item := range work {
		if err := EnqueueEncryptionMigration(ctx, publisher, item.Kind, item.OrgID); err != nil {
			return err
		}
		if log != nil {
			log.Infof("encryption migration: enqueued batch for %s org %s", item.Kind, item.OrgID)
		}
	}
	migrator.maybePrepareKeyRetirement(ctx)
	return nil
}
