package deltapreparegeneration

import (
	"context"
	"testing"

	"github.com/flightctl/flightctl/internal/delta_worker/model"
	deltagenerationstore "github.com/flightctl/flightctl/internal/delta_worker/store/deltageneration"
	deltapreparestore "github.com/flightctl/flightctl/internal/delta_worker/store/deltaprepare"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestNewStore_ReturnsNonNilStore(t *testing.T) {
	require.NotNil(t, NewStore(nil, logrus.New()))
}

func TestCreateDeltaPrepareGenerationsInitializesMultiplePrepares(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	require.NoError(t, err)

	log := logrus.New()
	generationStore := deltagenerationstore.NewStore(db, log)
	prepareStore := deltapreparestore.NewStore(db, log)
	prepareGenerationStore := NewStore(db, log)
	ctx := context.Background()
	require.NoError(t, generationStore.InitialMigration(ctx))
	require.NoError(t, prepareStore.InitialMigration(ctx))
	require.NoError(t, prepareGenerationStore.InitialMigration(ctx))

	orgID := uuid.New()
	pendingKey := deltagenerationstore.GenerationKey{OrgID: orgID, ImageRepository: "repo", SourceDigest: "source", TargetDigest: "pending"}
	terminalKey := deltagenerationstore.GenerationKey{OrgID: orgID, ImageRepository: "repo", SourceDigest: "source", TargetDigest: "terminal"}
	for _, generation := range []*model.DeltaGeneration{
		{OrgID: pendingKey.OrgID, ImageRepository: pendingKey.ImageRepository, SourceDigest: pendingKey.SourceDigest, TargetDigest: pendingKey.TargetDigest, Status: model.DeltaGenerationPending},
		{OrgID: terminalKey.OrgID, ImageRepository: terminalKey.ImageRepository, SourceDigest: terminalKey.SourceDigest, TargetDigest: terminalKey.TargetDigest, Status: model.DeltaGenerationSucceeded},
	} {
		require.NoError(t, db.Create(generation).Error)
	}

	first := &model.DeltaPrepare{ID: uuid.New(), OrgID: orgID, Kind: "fleet", Name: "fleet-1", Status: model.DeltaPrepareWaiting}
	second := &model.DeltaPrepare{ID: uuid.New(), OrgID: orgID, Kind: "fleet", Name: "fleet-2", Status: model.DeltaPrepareWaiting}
	require.NoError(t, prepareStore.CreateDeltaPrepare(ctx, first))
	require.NoError(t, prepareStore.CreateDeltaPrepare(ctx, second))

	created, err := prepareGenerationStore.CreateDeltaPrepareGenerations(ctx, []*model.DeltaPrepareGeneration{
		{PrepareID: first.ID, OrgID: pendingKey.OrgID, ImageRepository: pendingKey.ImageRepository, SourceDigest: pendingKey.SourceDigest, TargetDigest: pendingKey.TargetDigest},
		{PrepareID: first.ID, OrgID: terminalKey.OrgID, ImageRepository: terminalKey.ImageRepository, SourceDigest: terminalKey.SourceDigest, TargetDigest: terminalKey.TargetDigest},
		{PrepareID: second.ID, OrgID: terminalKey.OrgID, ImageRepository: terminalKey.ImageRepository, SourceDigest: terminalKey.SourceDigest, TargetDigest: terminalKey.TargetDigest},
	})
	require.NoError(t, err)
	require.Equal(t, model.DeltaPrepareWaiting, created.UpdatedPrepares[first.ID].Status)
	require.Equal(t, 1, created.UpdatedPrepares[first.ID].PendingGenerationsCount)
	require.Equal(t, model.DeltaPrepareComplete, created.UpdatedPrepares[second.ID].Status)
	require.Equal(t, 0, created.UpdatedPrepares[second.ID].PendingGenerationsCount)

	firstJoins, err := prepareGenerationStore.ListDeltaPrepareGenerations(ctx, ListFilter{PrepareID: &first.ID})
	require.NoError(t, err)
	require.Len(t, firstJoins, 2)
	for _, join := range firstJoins {
		if join.TargetDigest == terminalKey.TargetDigest {
			require.True(t, join.Completed)
		}
	}
	secondJoins, err := prepareGenerationStore.ListDeltaPrepareGenerations(ctx, ListFilter{PrepareID: &second.ID})
	require.NoError(t, err)
	require.Len(t, secondJoins, 1)
	require.True(t, secondJoins[0].Completed)

	storedFirst, err := prepareStore.GetDeltaPrepare(ctx, deltapreparestore.PrepareKey{ID: first.ID})
	require.NoError(t, err)
	require.Equal(t, model.DeltaPrepareWaiting, storedFirst.Status)
	require.Equal(t, 1, storedFirst.PendingGenerationsCount)

	storedSecond, err := prepareStore.GetDeltaPrepare(ctx, deltapreparestore.PrepareKey{ID: second.ID})
	require.NoError(t, err)
	require.Equal(t, model.DeltaPrepareComplete, storedSecond.Status)
	require.Equal(t, 0, storedSecond.PendingGenerationsCount)
}
