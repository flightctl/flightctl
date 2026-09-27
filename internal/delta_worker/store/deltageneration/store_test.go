package deltageneration

import (
	"context"
	"testing"

	"github.com/flightctl/flightctl/internal/delta_worker/model"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestNewStore_ReturnsNonNilStore(t *testing.T) {
	require.NotNil(t, NewStore(nil, logrus.New()))
}

func TestGenerationStore_GetAndList(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	ctx := context.Background()
	generationStore := NewStore(db, logrus.New())
	require.NoError(t, generationStore.InitialMigration(ctx))
	generation := &model.DeltaGeneration{
		OrgID:           uuid.New(),
		ImageRepository: "quay.io/example/os",
		SourceDigest:    "sha256:source",
		TargetDigest:    "sha256:target",
		Status:          model.DeltaGenerationPending,
	}

	require.NoError(t, db.Create(generation).Error)

	key := generationKeyOf(generation)
	stored, err := generationStore.GetDeltaGeneration(ctx, key)
	require.NoError(t, err)
	require.Equal(t, model.DeltaGenerationPending, stored.Status)

	listed, err := generationStore.ListDeltaGenerations(ctx, []GenerationKey{key})
	require.NoError(t, err)
	require.Len(t, listed, 1)
	require.Equal(t, key, generationKeyOf(&listed[0]))
}
