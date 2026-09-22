package deltaprepare

import (
	"context"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/delta_worker/model"
	deltagenerationstore "github.com/flightctl/flightctl/internal/delta_worker/store/deltageneration"
	deltapreparegenerationstore "github.com/flightctl/flightctl/internal/delta_worker/store/deltapreparegeneration"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestNewStore_ReturnsNonNilStore(t *testing.T) {
	require.NotNil(t, NewStore(nil, logrus.New()))
}

func TestListWaitingPastDeadline_WhenLimitIsOutOfRangeItShouldError(t *testing.T) {
	t.Parallel()
	s := NewStore(nil, logrus.New())
	ctx := context.Background()

	now := time.Now()
	_, err := s.ListWaitingPastDeadline(ctx, 0, now)
	require.Error(t, err)

	_, err = s.ListWaitingPastDeadline(ctx, MaxListWaitingPastDeadline+1, now)
	require.Error(t, err)
}

func TestDecrementPendingGenerationsForGeneration(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	require.NoError(t, err)

	log := logrus.New()
	generationStore := deltagenerationstore.NewStore(db, log)
	prepareStore := NewStore(db, log)
	prepareGenerationStore := deltapreparegenerationstore.NewStore(db, log)
	ctx := context.Background()
	require.NoError(t, generationStore.InitialMigration(ctx))
	require.NoError(t, prepareStore.InitialMigration(ctx))
	require.NoError(t, prepareGenerationStore.InitialMigration(ctx))

	orgID := uuid.New()
	pendingKey := deltagenerationstore.GenerationKey{OrgID: orgID, ImageRepository: "repo", SourceDigest: "source", TargetDigest: "pending"}
	secondPendingKey := deltagenerationstore.GenerationKey{OrgID: orgID, ImageRepository: "repo", SourceDigest: "source", TargetDigest: "second-pending"}
	newPendingKey := deltagenerationstore.GenerationKey{OrgID: orgID, ImageRepository: "repo", SourceDigest: "source", TargetDigest: "new-pending"}
	alreadyTerminalKey := deltagenerationstore.GenerationKey{OrgID: orgID, ImageRepository: "repo", SourceDigest: "source", TargetDigest: "already-terminal"}
	for _, generation := range []*model.DeltaGeneration{
		{OrgID: pendingKey.OrgID, ImageRepository: pendingKey.ImageRepository, SourceDigest: pendingKey.SourceDigest, TargetDigest: pendingKey.TargetDigest, Status: model.DeltaGenerationPending},
		{OrgID: secondPendingKey.OrgID, ImageRepository: secondPendingKey.ImageRepository, SourceDigest: secondPendingKey.SourceDigest, TargetDigest: secondPendingKey.TargetDigest, Status: model.DeltaGenerationPending},
		{OrgID: newPendingKey.OrgID, ImageRepository: newPendingKey.ImageRepository, SourceDigest: newPendingKey.SourceDigest, TargetDigest: newPendingKey.TargetDigest, Status: model.DeltaGenerationPending},
		{OrgID: alreadyTerminalKey.OrgID, ImageRepository: alreadyTerminalKey.ImageRepository, SourceDigest: alreadyTerminalKey.SourceDigest, TargetDigest: alreadyTerminalKey.TargetDigest, Status: model.DeltaGenerationSucceeded},
	} {
		require.NoError(t, db.Create(generation).Error)
	}

	prepare := &model.DeltaPrepare{ID: uuid.New(), OrgID: orgID, Kind: "fleet", Name: "fleet-1", Status: model.DeltaPrepareWaiting}
	require.NoError(t, prepareStore.CreateDeltaPrepare(ctx, prepare))
	_, err = prepareGenerationStore.CreateDeltaPrepareGenerations(ctx, []*model.DeltaPrepareGeneration{
		{PrepareID: prepare.ID, OrgID: pendingKey.OrgID, ImageRepository: pendingKey.ImageRepository, SourceDigest: pendingKey.SourceDigest, TargetDigest: pendingKey.TargetDigest},
		{PrepareID: prepare.ID, OrgID: secondPendingKey.OrgID, ImageRepository: secondPendingKey.ImageRepository, SourceDigest: secondPendingKey.SourceDigest, TargetDigest: secondPendingKey.TargetDigest},
		{PrepareID: prepare.ID, OrgID: alreadyTerminalKey.OrgID, ImageRepository: alreadyTerminalKey.ImageRepository, SourceDigest: alreadyTerminalKey.SourceDigest, TargetDigest: alreadyTerminalKey.TargetDigest},
	})
	require.NoError(t, err)

	stored, err := prepareStore.GetDeltaPrepare(ctx, PrepareKey{ID: prepare.ID})
	require.NoError(t, err)
	require.Equal(t, 2, stored.PendingGenerationsCount)

	// A terminal generation was already complete when its join was created;
	// redelivery must not consume the counter for either pending generation.
	claimed, err := prepareStore.DecrementPendingGenerationsForGeneration(ctx, alreadyTerminalKey, model.DeltaGenerationSucceeded)
	require.NoError(t, err)
	require.Empty(t, claimed)
	stored, err = prepareStore.GetDeltaPrepare(ctx, PrepareKey{ID: prepare.ID})
	require.NoError(t, err)
	require.Equal(t, 2, stored.PendingGenerationsCount)

	require.NoError(t, db.Model(&model.DeltaGeneration{}).
		Where("org_id = ? AND image_repository = ? AND source_digest = ? AND target_digest = ?",
			pendingKey.OrgID, pendingKey.ImageRepository, pendingKey.SourceDigest, pendingKey.TargetDigest).
		Update("status", model.DeltaGenerationSucceeded).Error)

	claimed, err = prepareStore.DecrementPendingGenerationsForGeneration(ctx, pendingKey, model.DeltaGenerationSucceeded)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	require.Equal(t, model.DeltaPrepareWaiting, claimed[0].Prepare.Status)
	require.Equal(t, 1, claimed[0].Prepare.PendingGenerationsCount)
	require.Equal(t, 2, claimed[0].Completed)
	require.Equal(t, 3, claimed[0].Total)

	// Redelivery of the same completion event must not consume the counter for
	// a different generation that is still pending.
	claimed, err = prepareStore.DecrementPendingGenerationsForGeneration(ctx, pendingKey, model.DeltaGenerationSucceeded)
	require.NoError(t, err)
	require.Empty(t, claimed)
	stored, err = prepareStore.GetDeltaPrepare(ctx, PrepareKey{ID: prepare.ID})
	require.NoError(t, err)
	require.Equal(t, 1, stored.PendingGenerationsCount)

	require.NoError(t, db.Model(&model.DeltaGeneration{}).
		Where("org_id = ? AND image_repository = ? AND source_digest = ? AND target_digest = ?",
			secondPendingKey.OrgID, secondPendingKey.ImageRepository, secondPendingKey.SourceDigest, secondPendingKey.TargetDigest).
		Update("status", model.DeltaGenerationSucceeded).Error)

	claimed, err = prepareStore.DecrementPendingGenerationsForGeneration(ctx, secondPendingKey, model.DeltaGenerationSucceeded)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	require.Equal(t, model.DeltaPrepareComplete, claimed[0].Prepare.Status)
	require.Equal(t, 0, claimed[0].Prepare.PendingGenerationsCount)
	require.Equal(t, 3, claimed[0].Completed)
	require.Equal(t, 3, claimed[0].Total)

	// A new prepare records the already-terminal generation as completed while
	// it is created, so a later notification for that generation is a no-op.
	newPrepare := &model.DeltaPrepare{ID: uuid.New(), OrgID: orgID, Kind: "fleet", Name: "fleet-new", Status: model.DeltaPrepareWaiting}
	require.NoError(t, prepareStore.CreateDeltaPrepare(ctx, newPrepare))
	_, err = prepareGenerationStore.CreateDeltaPrepareGenerations(ctx, []*model.DeltaPrepareGeneration{
		{PrepareID: newPrepare.ID, OrgID: secondPendingKey.OrgID, ImageRepository: secondPendingKey.ImageRepository, SourceDigest: secondPendingKey.SourceDigest, TargetDigest: secondPendingKey.TargetDigest},
		{PrepareID: newPrepare.ID, OrgID: newPendingKey.OrgID, ImageRepository: newPendingKey.ImageRepository, SourceDigest: newPendingKey.SourceDigest, TargetDigest: newPendingKey.TargetDigest},
	})
	require.NoError(t, err)
	newStored, err := prepareStore.GetDeltaPrepare(ctx, PrepareKey{ID: newPrepare.ID})
	require.NoError(t, err)
	require.Equal(t, model.DeltaPrepareWaiting, newStored.Status)
	require.Equal(t, 1, newStored.PendingGenerationsCount)

	claimed, err = prepareStore.DecrementPendingGenerationsForGeneration(ctx, secondPendingKey, model.DeltaGenerationSucceeded)
	require.NoError(t, err)
	require.Empty(t, claimed)

	require.NoError(t, db.Model(&model.DeltaGeneration{}).
		Where("org_id = ? AND image_repository = ? AND source_digest = ? AND target_digest = ?",
			newPendingKey.OrgID, newPendingKey.ImageRepository, newPendingKey.SourceDigest, newPendingKey.TargetDigest).
		Update("status", model.DeltaGenerationSucceeded).Error)
	claimed, err = prepareStore.DecrementPendingGenerationsForGeneration(ctx, newPendingKey, model.DeltaGenerationSucceeded)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	require.Equal(t, newPrepare.ID, claimed[0].Prepare.ID)
	require.Equal(t, model.DeltaPrepareComplete, claimed[0].Prepare.Status)
	require.Equal(t, 0, claimed[0].Prepare.PendingGenerationsCount)
	require.Equal(t, 2, claimed[0].Completed)
	require.Equal(t, 2, claimed[0].Total)

	// A second notification cannot update an already completed prepare.
	claimed, err = prepareStore.DecrementPendingGenerationsForGeneration(ctx, secondPendingKey, model.DeltaGenerationSucceeded)
	require.NoError(t, err)
	require.Empty(t, claimed)

	terminalPrepare := &model.DeltaPrepare{ID: uuid.New(), OrgID: orgID, Kind: "fleet", Name: "fleet-2", Status: model.DeltaPrepareWaiting}
	require.NoError(t, prepareStore.CreateDeltaPrepare(ctx, terminalPrepare))
	_, err = prepareGenerationStore.CreateDeltaPrepareGenerations(ctx, []*model.DeltaPrepareGeneration{{
		PrepareID: terminalPrepare.ID, OrgID: alreadyTerminalKey.OrgID, ImageRepository: alreadyTerminalKey.ImageRepository,
		SourceDigest: alreadyTerminalKey.SourceDigest, TargetDigest: alreadyTerminalKey.TargetDigest,
	}})
	require.NoError(t, err)
	stored, err = prepareStore.GetDeltaPrepare(ctx, PrepareKey{ID: terminalPrepare.ID})
	require.NoError(t, err)
	require.Equal(t, model.DeltaPrepareComplete, stored.Status)
	require.Equal(t, 0, stored.PendingGenerationsCount)
}

func TestDecrementPendingGenerationsForGenerationSkipsStaleTerminalEvent(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	require.NoError(t, err)

	log := logrus.New()
	generationStore := deltagenerationstore.NewStore(db, log)
	prepareStore := NewStore(db, log)
	prepareGenerationStore := deltapreparegenerationstore.NewStore(db, log)
	ctx := context.Background()
	require.NoError(t, generationStore.InitialMigration(ctx))
	require.NoError(t, prepareStore.InitialMigration(ctx))
	require.NoError(t, prepareGenerationStore.InitialMigration(ctx))

	orgID := uuid.New()
	key := deltagenerationstore.GenerationKey{OrgID: orgID, ImageRepository: "repo", SourceDigest: "source", TargetDigest: "retry"}
	require.NoError(t, db.Create(&model.DeltaGeneration{
		OrgID:           key.OrgID,
		ImageRepository: key.ImageRepository,
		SourceDigest:    key.SourceDigest,
		TargetDigest:    key.TargetDigest,
		Status:          model.DeltaGenerationPending,
	}).Error)

	prepare := &model.DeltaPrepare{ID: uuid.New(), OrgID: orgID, Kind: "fleet", Name: "fleet-retry", Status: model.DeltaPrepareWaiting}
	require.NoError(t, prepareStore.CreateDeltaPrepare(ctx, prepare))
	_, err = prepareGenerationStore.CreateDeltaPrepareGenerations(ctx, []*model.DeltaPrepareGeneration{{
		PrepareID:       prepare.ID,
		OrgID:           key.OrgID,
		ImageRepository: key.ImageRepository,
		SourceDigest:    key.SourceDigest,
		TargetDigest:    key.TargetDigest,
	}})
	require.NoError(t, err)

	require.NoError(t, db.Model(&model.DeltaGeneration{}).
		Where("org_id = ? AND image_repository = ? AND source_digest = ? AND target_digest = ?",
			key.OrgID, key.ImageRepository, key.SourceDigest, key.TargetDigest).
		Update("status", model.DeltaGenerationFailed).Error)

	// A completion event can be delayed until after a failed generation is
	// requeued. It must not decrement the counter while the current row is
	// pending again.
	require.NoError(t, db.Model(&model.DeltaGeneration{}).
		Where("org_id = ? AND image_repository = ? AND source_digest = ? AND target_digest = ?",
			key.OrgID, key.ImageRepository, key.SourceDigest, key.TargetDigest).
		Update("status", model.DeltaGenerationPending).Error)
	claimed, err := prepareStore.DecrementPendingGenerationsForGeneration(ctx, key, model.DeltaGenerationFailed)
	require.NoError(t, err)
	require.Empty(t, claimed)

	// The retry succeeds, but the old failed event is delivered before the
	// success event. The old event must not claim the join based only on the
	// fact that the row is terminal again.
	require.NoError(t, db.Model(&model.DeltaGeneration{}).
		Where("org_id = ? AND image_repository = ? AND source_digest = ? AND target_digest = ?",
			key.OrgID, key.ImageRepository, key.SourceDigest, key.TargetDigest).
		Update("status", model.DeltaGenerationSucceeded).Error)

	claimed, err = prepareStore.DecrementPendingGenerationsForGeneration(ctx, key, model.DeltaGenerationFailed)
	require.NoError(t, err)
	require.Empty(t, claimed)

	claimed, err = prepareStore.DecrementPendingGenerationsForGeneration(ctx, key, model.DeltaGenerationSucceeded)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	require.Equal(t, model.DeltaPrepareComplete, claimed[0].Prepare.Status)
}
