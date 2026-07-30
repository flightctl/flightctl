package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/org"
	"github.com/flightctl/flightctl/internal/service/common"
)

const encryptionMigrationStatusCheckpointKey = "status"

// encryptionMigrationStatusCheckpoint tracks system-wide started/completed events for one migration target.
type encryptionMigrationStatusCheckpoint struct {
	TargetActiveKeyID string `json:"targetActiveKeyId"`
	RegistryHash      string `json:"registryHash,omitempty"`
	StartedEmitted    bool   `json:"startedEmitted,omitempty"`
	CompletedEmitted  bool   `json:"completedEmitted,omitempty"`
}

func (m *EncryptionMigrator) emitStartedEventIfNeeded(ctx context.Context, activeKeyID, registryHash string) {
	if m == nil || m.eventSvc == nil || activeKeyID == "" {
		return
	}
	status, err := m.loadStatusCheckpoint(ctx)
	if err != nil {
		m.log.WithError(err).Error("encryption migration: load status checkpoint before started event")
		return
	}
	if !checkpointMatchesMigrationTarget(EncryptionMigrationCheckpoint{
		TargetActiveKeyID: status.TargetActiveKeyID,
		RegistryHash:      status.RegistryHash,
	}, activeKeyID, registryHash) {
		status = encryptionMigrationStatusCheckpoint{
			TargetActiveKeyID: activeKeyID,
			RegistryHash:      registryHash,
		}
	}
	if status.StartedEmitted {
		return
	}

	m.eventSvc.CreateEvent(ctx, org.DefaultID, common.GetEncryptionMigrationStartedEvent(ctx, activeKeyID))
	status.TargetActiveKeyID = activeKeyID
	status.RegistryHash = registryHash
	status.StartedEmitted = true
	status.CompletedEmitted = false
	if err := m.saveStatusCheckpoint(ctx, status); err != nil {
		m.log.WithError(err).Error("encryption migration: save status checkpoint after started event")
	}
}

func (m *EncryptionMigrator) emitCompletedEventIfNeeded(ctx context.Context, activeKeyID, registryHash string, retiredKeyIDs []string) {
	if m == nil || m.eventSvc == nil || activeKeyID == "" {
		return
	}
	status, err := m.loadStatusCheckpoint(ctx)
	if err != nil {
		m.log.WithError(err).Error("encryption migration: load status checkpoint before completed event")
		return
	}
	if !checkpointMatchesMigrationTarget(EncryptionMigrationCheckpoint{
		TargetActiveKeyID: status.TargetActiveKeyID,
		RegistryHash:      status.RegistryHash,
	}, activeKeyID, registryHash) {
		status = encryptionMigrationStatusCheckpoint{
			TargetActiveKeyID: activeKeyID,
			RegistryHash:      registryHash,
			StartedEmitted:    true,
		}
	}
	if status.CompletedEmitted {
		return
	}

	m.eventSvc.CreateEvent(ctx, org.DefaultID, common.GetEncryptionMigrationCompletedEvent(ctx, activeKeyID, retiredKeyIDs))
	status.TargetActiveKeyID = activeKeyID
	status.RegistryHash = registryHash
	status.StartedEmitted = true
	status.CompletedEmitted = true
	if err := m.saveStatusCheckpoint(ctx, status); err != nil {
		m.log.WithError(err).Error("encryption migration: save status checkpoint after completed event")
	}
}

func (m *EncryptionMigrator) loadStatusCheckpoint(ctx context.Context) (encryptionMigrationStatusCheckpoint, error) {
	data, err := m.checkpoints.Get(ctx, EncryptionMigrationConsumer, encryptionMigrationStatusCheckpointKey)
	if err != nil {
		if errors.Is(err, flterrors.ErrResourceNotFound) {
			return encryptionMigrationStatusCheckpoint{}, nil
		}
		return encryptionMigrationStatusCheckpoint{}, fmt.Errorf("load encryption migration status checkpoint: %w", err)
	}
	var status encryptionMigrationStatusCheckpoint
	if err := json.Unmarshal(data, &status); err != nil {
		return encryptionMigrationStatusCheckpoint{}, fmt.Errorf("decode encryption migration status checkpoint: %w", err)
	}
	return status, nil
}

func (m *EncryptionMigrator) saveStatusCheckpoint(ctx context.Context, status encryptionMigrationStatusCheckpoint) error {
	data, err := json.Marshal(status)
	if err != nil {
		return fmt.Errorf("encode encryption migration status checkpoint: %w", err)
	}
	if err := m.checkpoints.Set(ctx, EncryptionMigrationConsumer, encryptionMigrationStatusCheckpointKey, data); err != nil {
		return fmt.Errorf("save encryption migration status checkpoint: %w", err)
	}
	return nil
}
