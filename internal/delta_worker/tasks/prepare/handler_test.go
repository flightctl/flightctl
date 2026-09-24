package prepare

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	deltaconfig "github.com/flightctl/flightctl/internal/delta_worker/config"
	"github.com/flightctl/flightctl/internal/delta_worker/model"
	"github.com/flightctl/flightctl/internal/delta_worker/service/deltageneration"
	"github.com/flightctl/flightctl/internal/delta_worker/service/deltaprepare"
	"github.com/flightctl/flightctl/internal/delta_worker/service/deltapreparegeneration"
	deltastore "github.com/flightctl/flightctl/internal/delta_worker/store/deltageneration"
	deltapreparestore "github.com/flightctl/flightctl/internal/delta_worker/store/deltaprepare"
	deltapreparegenerationstore "github.com/flightctl/flightctl/internal/delta_worker/store/deltapreparegeneration"
	generateTask "github.com/flightctl/flightctl/internal/delta_worker/tasks/generate"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/tasks"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	prepareTestImage = "quay.io/acme/os:v2"
	prepareTestRepo  = "quay.io/acme/os"
	prepareTestSrc   = "sha256:aaa"
	prepareTestTgt   = "sha256:bbb"
	prepareTestHash  = "spec-hash"
)

func TestPrepare_SkipPaths(t *testing.T) {
	orgId := uuid.New()
	ctx := context.Background()

	t.Run("When generateDelta is false it should Resume without inserting", func(t *testing.T) {
		store := newFakePrepareStore()
		status := &statusSpy{}
		resume := &resumeSpy{}
		emit := &emitSpy{}
		p := newTestPreparer(t, store, skipGenerateDeltaResolver(), status, resume, emit)
		err := p.Prepare(ctx, fleetPrepareEvent(orgId, "fleet-1", "tv-1"))
		require.NoError(t, err)
		assert.Empty(t, store.prepares)
		assert.Empty(t, status.sets)
		assert.Empty(t, status.clears)
		require.Len(t, emit.events, 1)
		assert.Equal(t, domain.EventReasonDeltaPrepareComplete, emit.events[0].Reason)
	})

	t.Run("When generateDelta is false and a wait is in flight it should fail the waiting prepare", func(t *testing.T) {
		store := newFakePrepareStore()
		existing := store.seedWaiting(orgId, domain.FleetKind, "fleet-1", lo.ToPtr("tv-1"), nil, time.Now())
		resume := &resumeSpy{}
		p := newTestPreparer(t, store, skipGenerateDeltaResolver(), &statusSpy{}, resume, &emitSpy{})
		err := p.Prepare(ctx, fleetPrepareEvent(orgId, "fleet-1", "tv-1"))
		require.NoError(t, err)
		assert.Equal(t, model.DeltaPrepareFailed, store.prepares[existing.ID].Status)
	})

	t.Run("When the write target is missing it should Resume without inserting", func(t *testing.T) {
		store := newFakePrepareStore()
		resume := &resumeSpy{}
		p := newTestPreparer(t, store, &Resolver{
			FleetService: mockFleetService(func(_ context.Context, _ uuid.UUID, _ string) (*domain.Fleet, error) {
				return fleetWithTV("fleet-1", "tv-1"), nil
			}),
			RepositoryService: mockRepositoryService(func(_ context.Context, _ uuid.UUID, _ domain.ListRepositoriesParams) (*domain.RepositoryList, error) {
				return &domain.RepositoryList{}, nil
			}),
			Config: &deltaconfig.DeltaGenerationConfig{},
		}, &statusSpy{}, resume, &emitSpy{})
		err := p.Prepare(ctx, fleetPrepareEvent(orgId, "fleet-1", "tv-1"))
		require.NoError(t, err)
		assert.Empty(t, store.prepares)
	})

	t.Run("When no device is eligible it should Resume without inserting", func(t *testing.T) {
		store := newFakePrepareStore()
		resume := &resumeSpy{}
		fleet := fleetWithTV("fleet-1", "tv-1")
		p := newTestPreparer(t, store, eligibleFleetResolver(fleet, deviceWithOS("d1", false, prepareTestSrc)), &statusSpy{}, resume, &emitSpy{})
		err := p.Prepare(ctx, fleetPrepareEvent(orgId, "fleet-1", "tv-1"))
		require.NoError(t, err)
		assert.Empty(t, store.prepares)
	})

	t.Run("When a newer device skip supersedes a waiting prepare it should rebind the preparing identity", func(t *testing.T) {
		store := newFakePrepareStore()
		old := store.seedWaiting(orgId, domain.DeviceKind, "d1", nil, lo.ToPtr("old-spec-hash"), time.Now())
		device := deviceWithOS("d1", true, "")
		(*device.Metadata.Annotations)[domain.DeviceAnnotationRenderedSpecHash] = "new-spec-hash"
		status := &statusSpy{}
		emit := &emitSpy{}
		p := newTestPreparer(t, store, eligibleDeviceResolver(device), status, &resumeSpy{}, emit)

		err := p.Prepare(ctx, devicePrepareEventWithSpecHashAndResourceVersion(orgId, "d1", "new-spec-hash", "2"))
		require.NoError(t, err)
		assert.Equal(t, model.DeltaPrepareFailed, store.prepares[old.ID].Status)
		require.Len(t, status.sets, 1)
		assert.Equal(t, domain.DeviceKind, status.sets[0].kind)
		assert.Equal(t, "d1", status.sets[0].name)
		assert.Equal(t, 0, status.sets[0].completed)
		assert.Equal(t, 0, status.sets[0].total)
		assert.Equal(t, int64(2), status.sets[0].sourceResourceVersion)
		assert.Equal(t, "new-spec-hash", lo.FromPtr(status.sets[0].specHash))
		require.Len(t, emit.events, 1)
		completion, err := deltaprepare.ParsePrepareCompletionEvent(orgId, emit.events[0].Message)
		require.NoError(t, err)
		assert.Equal(t, int64(2), completion.SourceResourceVersion)
		assert.Equal(t, "new-spec-hash", lo.FromPtr(completion.SpecHash))
	})

	t.Run("When DeltaCandidates is empty it should Resume without inserting", func(t *testing.T) {
		store := newFakePrepareStore()
		resume := &resumeSpy{}
		fleet := fleetWithTV("fleet-1", "tv-1")
		p := newTestPreparer(t, store, eligibleFleetResolver(fleet, deviceWithOS("d1", true, "")), &statusSpy{}, resume, &emitSpy{})
		err := p.Prepare(ctx, fleetPrepareEvent(orgId, "fleet-1", "tv-1"))
		require.NoError(t, err)
		assert.Empty(t, store.prepares)
	})
}

func TestPrepare_InsertAndAck(t *testing.T) {
	orgId := uuid.New()
	ctx := context.Background()
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)

	t.Run("When a fleet Prepare has eligible devices it should insert waiting, enqueue GenerateDelta, and set DeltaPreparing", func(t *testing.T) {
		store := newFakePrepareStore()
		var order []string
		status := &statusSpy{order: &order}
		resume := &resumeSpy{}
		emit := &emitSpy{order: &order}
		fleet := fleetWithTV("fleet-1", "tv-1")
		p := newTestPreparer(t, store, eligibleFleetResolver(fleet, deviceWithOS("d1", true, prepareTestSrc)), status, resume, emit)
		p.Now = func() time.Time { return now }

		err := p.Prepare(ctx, fleetPrepareEvent(orgId, "fleet-1", "tv-1"))
		require.NoError(t, err)
		require.Len(t, store.prepares, 1)
		prep := firstPrepare(store)
		assert.Equal(t, model.DeltaPrepareWaiting, prep.Status)
		assert.Equal(t, domain.FleetKind, prep.Kind)
		assert.Equal(t, "fleet-1", prep.Name)
		assert.Equal(t, "tv-1", lo.FromPtr(prep.TemplateVersion))
		assert.Nil(t, prep.Deadline)
		assert.Equal(t, now, prep.CreatedAt)
		assert.Len(t, store.joins, 1)
		assert.Equal(t, 1, store.insertGensN)
		require.Len(t, emit.events, 1)
		assert.Equal(t, domain.EventReasonGenerateDelta, emit.events[0].Reason)
		assert.Equal(t, []string{"status", "emit"}, order)
		var payload generateTask.GenerateDeltaPayload
		require.NoError(t, json.Unmarshal([]byte(emit.events[0].Message), &payload))
		assert.Equal(t, prepareTestRepo, payload.ImageRepository)
		assert.Equal(t, prepareTestSrc, payload.SourceDigest)
		assert.Equal(t, prepareTestTgt, payload.TargetDigest)
		require.Len(t, status.sets, 1)
		assert.Equal(t, domain.FleetKind, status.sets[0].kind)
		assert.Equal(t, "fleet-1", status.sets[0].name)
		assert.Equal(t, 0, status.sets[0].completed)
		assert.Equal(t, 1, status.sets[0].total)
	})

	t.Run("When enqueue fails it should return the emit error after insert", func(t *testing.T) {
		store := newFakePrepareStore()
		emit := &emitSpy{err: errors.New("redis down")}
		p := newTestPreparer(t, store, eligibleFleetResolver(fleetWithTV("fleet-1", "tv-1"), deviceWithOS("d1", true, prepareTestSrc)), &statusSpy{}, &resumeSpy{}, emit)
		p.Now = func() time.Time { return now }

		err := p.Prepare(ctx, fleetPrepareEvent(orgId, "fleet-1", "tv-1"))
		require.EqualError(t, err, "redis down")
		require.Len(t, store.prepares, 1)
		assert.Equal(t, model.DeltaPrepareWaiting, firstPrepare(store).Status)
	})

	t.Run("When two devices share a digest pair it should enqueue one generation", func(t *testing.T) {
		store := newFakePrepareStore()
		emit := &emitSpy{}
		fleet := fleetWithTV("fleet-1", "tv-1")
		p := newTestPreparer(t, store, eligibleFleetResolver(fleet,
			deviceWithOS("d1", true, prepareTestSrc),
			deviceWithOS("d2", true, prepareTestSrc),
		), &statusSpy{}, &resumeSpy{}, emit)

		err := p.Prepare(ctx, fleetPrepareEvent(orgId, "fleet-1", "tv-1"))
		require.NoError(t, err)
		assert.Equal(t, 1, store.insertGensN)
		require.Len(t, store.generations, 1)
		require.Len(t, emit.events, 1)
		require.Len(t, store.joins, 1)
	})

	t.Run("When omitted generateDelta it should still insert waiting", func(t *testing.T) {
		store := newFakePrepareStore()
		fleet := fleetWithTV("fleet-1", "tv-1")
		fleet.Spec.RolloutPolicy = nil
		p := newTestPreparer(t, store, eligibleFleetResolver(fleet, deviceWithOS("d1", true, prepareTestSrc)), &statusSpy{}, &resumeSpy{}, &emitSpy{})
		err := p.Prepare(ctx, fleetPrepareEvent(orgId, "fleet-1", "tv-1"))
		require.NoError(t, err)
		require.Len(t, store.prepares, 1)
		assert.Equal(t, model.DeltaPrepareWaiting, firstPrepare(store).Status)
	})
}

func TestPrepare_Deadlines(t *testing.T) {
	orgId := uuid.New()
	ctx := context.Background()
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)

	t.Run("When maxWait is omitted it should persist with a nil deadline", func(t *testing.T) {
		store := newFakePrepareStore()
		resume := &resumeSpy{}
		p := newTestPreparer(t, store, eligibleFleetResolver(fleetWithTV("fleet-1", "tv-1"), deviceWithOS("d1", true, prepareTestSrc)), &statusSpy{}, resume, &emitSpy{})
		p.Now = func() time.Time { return now }

		err := p.Prepare(ctx, fleetPrepareEvent(orgId, "fleet-1", "tv-1"))
		require.NoError(t, err)
		assert.Nil(t, firstPrepare(store).Deadline)
		assert.Equal(t, model.DeltaPrepareWaiting, firstPrepare(store).Status)
	})

	t.Run("When maxWait is set it should set deadline to CreatedAt plus wait", func(t *testing.T) {
		store := newFakePrepareStore()
		wait := 5 * time.Minute
		p := newTestPreparer(t, store, eligibleFleetResolver(fleetWithTV("fleet-1", "tv-1"), deviceWithOS("d1", true, prepareTestSrc)), &statusSpy{}, &resumeSpy{}, &emitSpy{})
		p.Now = func() time.Time { return now }
		p.MaxWaitForDelta = &wait

		err := p.Prepare(ctx, fleetPrepareEvent(orgId, "fleet-1", "tv-1"))
		require.NoError(t, err)
		require.NotNil(t, firstPrepare(store).Deadline)
		assert.Equal(t, now.Add(wait), *firstPrepare(store).Deadline)
		assert.Equal(t, model.DeltaPrepareWaiting, firstPrepare(store).Status)
	})

	t.Run("When maxWait is 0s it should enqueue, complete, Resume, and not leave DeltaPreparing True", func(t *testing.T) {
		store := newFakePrepareStore()
		status := &statusSpy{}
		resume := &resumeSpy{}
		emit := &emitSpy{}
		zero := time.Duration(0)
		p := newTestPreparer(t, store, eligibleFleetResolver(fleetWithTV("fleet-1", "tv-1"), deviceWithOS("d1", true, prepareTestSrc)), status, resume, emit)
		p.Now = func() time.Time { return now }
		p.MaxWaitForDelta = &zero

		err := p.Prepare(ctx, fleetPrepareEvent(orgId, "fleet-1", "tv-1"))
		require.NoError(t, err)
		require.Len(t, emit.events, 2)
		assert.Equal(t, domain.EventReasonGenerateDelta, emit.events[0].Reason)
		assert.Equal(t, domain.EventReasonDeltaPrepareComplete, emit.events[1].Reason)
		assert.Equal(t, model.DeltaPrepareComplete, firstPrepare(store).Status)
		assert.Equal(t, now, *firstPrepare(store).Deadline)
		assert.Empty(t, status.sets)
		assert.Empty(t, status.clears)
	})

	t.Run("When the fleet sets maxWaitForDelta it should use the fleet duration", func(t *testing.T) {
		store := newFakePrepareStore()
		fleet := fleetWithTV("fleet-1", "tv-1")
		fleetWait := domain.Duration("10m")
		fleet.Spec.RolloutPolicy = &domain.RolloutPolicy{DeltaGeneration: &domain.RolloutPolicyDeltaGeneration{MaxWaitForDelta: &fleetWait}}
		deploy := 30 * time.Minute
		p := newTestPreparer(t, store, eligibleFleetResolver(fleet, deviceWithOS("d1", true, prepareTestSrc)), &statusSpy{}, &resumeSpy{}, &emitSpy{})
		p.Now = func() time.Time { return now }
		p.MaxWaitForDelta = &deploy

		err := p.Prepare(ctx, fleetPrepareEvent(orgId, "fleet-1", "tv-1"))
		require.NoError(t, err)
		require.NotNil(t, firstPrepare(store).Deadline)
		assert.Equal(t, now.Add(10*time.Minute), *firstPrepare(store).Deadline)
	})
}

func TestPrepare_DedupeAndSupercede(t *testing.T) {
	orgId := uuid.New()
	ctx := context.Background()
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)

	t.Run("When a waiting prepare already exists with the same TV it should keep the original deadline", func(t *testing.T) {
		store := newFakePrepareStore()
		created := now.Add(-time.Hour)
		existing := store.seedWaiting(orgId, domain.FleetKind, "fleet-1", lo.ToPtr("tv-1"), nil, created)
		store.generations[deltastore.GenerationKey{
			OrgID:           orgId,
			ImageRepository: prepareTestRepo,
			SourceDigest:    prepareTestSrc,
			TargetDigest:    prepareTestTgt,
		}] = &model.DeltaGeneration{
			OrgID:           orgId,
			ImageRepository: prepareTestRepo,
			SourceDigest:    prepareTestSrc,
			TargetDigest:    prepareTestTgt,
			Status:          model.DeltaGenerationPending,
		}
		resume := &resumeSpy{}
		emit := &emitSpy{}
		p := newTestPreparer(t, store, eligibleFleetResolver(fleetWithTV("fleet-1", "tv-1"), deviceWithOS("d1", true, prepareTestSrc)), &statusSpy{}, resume, emit)
		p.Now = func() time.Time { return now }

		err := p.Prepare(ctx, fleetPrepareEvent(orgId, "fleet-1", "tv-1"))
		require.NoError(t, err)
		assert.Len(t, store.prepares, 1)
		assert.Equal(t, existing.ID, firstPrepare(store).ID)
		assert.Equal(t, created, firstPrepare(store).CreatedAt)
		assert.Equal(t, model.DeltaPrepareWaiting, firstPrepare(store).Status)
		assert.Len(t, emit.events, 1)
	})

	t.Run("When a waiting prepare with the same identity is missing joins it should enqueue without a new row", func(t *testing.T) {
		store := newFakePrepareStore()
		created := now.Add(-time.Hour)
		existing := store.seedWaiting(orgId, domain.FleetKind, "fleet-1", lo.ToPtr("tv-1"), nil, created)
		emit := &emitSpy{}
		p := newTestPreparer(t, store, eligibleFleetResolver(fleetWithTV("fleet-1", "tv-1"), deviceWithOS("d1", true, prepareTestSrc)), &statusSpy{}, &resumeSpy{}, emit)
		p.Now = func() time.Time { return now }

		err := p.Prepare(ctx, fleetPrepareEvent(orgId, "fleet-1", "tv-1"))
		require.NoError(t, err)
		assert.Len(t, store.prepares, 1)
		assert.Equal(t, existing.ID, firstPrepare(store).ID)
		assert.Equal(t, created, firstPrepare(store).CreatedAt)
		require.Len(t, emit.events, 1)
		assert.Equal(t, model.DeltaPrepareWaiting, firstPrepare(store).Status)
	})

	t.Run("When a waiting prepare exists with a different TV it should fail the old prepare and insert a new one", func(t *testing.T) {
		store := newFakePrepareStore()
		old := store.seedWaiting(orgId, domain.FleetKind, "fleet-1", lo.ToPtr("tv-10"), nil, now.Add(-time.Hour))
		old.SourceResourceVersion = 0
		status := &statusSpy{}
		resume := &resumeSpy{}
		emit := &emitSpy{}
		p := newTestPreparer(t, store, eligibleFleetResolver(fleetWithTV("fleet-1", "tv-11"), deviceWithOS("d1", true, prepareTestSrc)), status, resume, emit)
		p.Now = func() time.Time { return now }

		err := p.Prepare(ctx, fleetPrepareEvent(orgId, "fleet-1", "tv-11"))
		require.NoError(t, err)
		gotOld, ok := store.prepares[old.ID]
		require.True(t, ok)
		assert.Equal(t, model.DeltaPrepareFailed, gotOld.Status)
		assert.Len(t, store.prepares, 2)
		var inserted *model.DeltaPrepare
		for _, prep := range store.prepares {
			if prep.ID != old.ID {
				inserted = prep
			}
		}
		require.NotNil(t, inserted)
		assert.Equal(t, model.DeltaPrepareWaiting, inserted.Status)
		assert.Equal(t, "tv-11", lo.FromPtr(inserted.TemplateVersion))
		require.Len(t, emit.events, 1)
		assert.Equal(t, domain.EventReasonGenerateDelta, emit.events[0].Reason)
		assert.NotEqual(t, domain.EventReasonFleetRolloutStarted, emit.events[0].Reason)
		assert.NotEqual(t, domain.EventReasonDeltaGenerationCompleted, emit.events[0].Reason)
	})

	t.Run("When a newer prepare replaces the waiting one it should clear the previous status", func(t *testing.T) {
		store := newFakePrepareStore()
		old := store.seedWaiting(orgId, domain.FleetKind, "fleet-1", lo.ToPtr("tv-10"), nil, now.Add(-time.Hour))
		old.SourceResourceVersion = 0
		status := &statusSpy{}
		p := newTestPreparer(t, store, eligibleFleetResolver(fleetWithTV("fleet-1", "tv-11"), deviceWithOS("d1", true, prepareTestSrc)), status, &resumeSpy{}, &emitSpy{})
		p.Now = func() time.Time { return now }

		err := p.Prepare(ctx, fleetPrepareEvent(orgId, "fleet-1", "tv-11"))
		require.NoError(t, err)
		assert.Len(t, status.clears, 1)
	})

	t.Run("When prepare admission returns an error it should fail the event", func(t *testing.T) {
		store := newFakePrepareStore()
		store.insertErr = flterrors.ErrDuplicateName
		resume := &resumeSpy{}
		emit := &emitSpy{}
		p := newTestPreparer(t, store, eligibleFleetResolver(fleetWithTV("fleet-1", "tv-1"), deviceWithOS("d1", true, prepareTestSrc)), &statusSpy{}, resume, emit)

		err := p.Prepare(ctx, fleetPrepareEvent(orgId, "fleet-1", "tv-1"))
		require.ErrorIs(t, err, flterrors.ErrDuplicateName)
		assert.Empty(t, emit.events)
		assert.Empty(t, store.prepares)
	})
}

func TestPrepare_TerminalAndDevice(t *testing.T) {
	orgId := uuid.New()
	ctx := context.Background()

	t.Run("When all generations are already terminal it should skip creating generations and complete", func(t *testing.T) {
		store := newFakePrepareStore()
		store.generations[deltastore.GenerationKey{
			OrgID:           orgId,
			ImageRepository: prepareTestRepo,
			SourceDigest:    prepareTestSrc,
			TargetDigest:    prepareTestTgt,
		}] = &model.DeltaGeneration{
			OrgID:           orgId,
			ImageRepository: prepareTestRepo,
			SourceDigest:    prepareTestSrc,
			TargetDigest:    prepareTestTgt,
			Status:          model.DeltaGenerationSucceeded,
		}
		status := &statusSpy{}
		resume := &resumeSpy{}
		emit := &emitSpy{}
		p := newTestPreparer(t, store, eligibleFleetResolver(fleetWithTV("fleet-1", "tv-1"), deviceWithOS("d1", true, prepareTestSrc)), status, resume, emit)

		err := p.Prepare(ctx, fleetPrepareEvent(orgId, "fleet-1", "tv-1"))
		require.NoError(t, err)
		assert.Equal(t, 1, store.insertGensN)
		require.Len(t, emit.events, 1)
		assert.Equal(t, domain.EventReasonDeltaPrepareComplete, emit.events[0].Reason)
		assert.Equal(t, model.DeltaPrepareComplete, firstPrepare(store).Status)
		assert.Len(t, store.joins, 1)
		assert.Empty(t, status.sets)
		assert.Empty(t, status.clears)
	})

	t.Run("When every pair is already terminal including failed it should not reset failed", func(t *testing.T) {
		store := newFakePrepareStore()
		failedKey := deltastore.GenerationKey{
			OrgID:           orgId,
			ImageRepository: prepareTestRepo,
			SourceDigest:    prepareTestSrc,
			TargetDigest:    prepareTestTgt,
		}
		store.generations[failedKey] = &model.DeltaGeneration{
			OrgID:           orgId,
			ImageRepository: prepareTestRepo,
			SourceDigest:    prepareTestSrc,
			TargetDigest:    prepareTestTgt,
			Status:          model.DeltaGenerationFailed,
		}
		emit := &emitSpy{}
		p := newTestPreparer(t, store, eligibleFleetResolver(fleetWithTV("fleet-1", "tv-1"), deviceWithOS("d1", true, prepareTestSrc)), &statusSpy{}, &resumeSpy{}, emit)

		err := p.Prepare(ctx, fleetPrepareEvent(orgId, "fleet-1", "tv-1"))
		require.NoError(t, err)
		assert.Equal(t, 1, store.insertGensN)
		assert.Len(t, emit.events, 1)
		assert.Equal(t, model.DeltaGenerationPending, store.generations[failedKey].Status)
		assert.Equal(t, model.DeltaPrepareWaiting, firstPrepare(store).Status)
	})

	t.Run("When some generations already succeeded it should enqueue only new or failed-to-pending keys", func(t *testing.T) {
		store := newFakePrepareStore()
		store.generations[deltastore.GenerationKey{
			OrgID:           orgId,
			ImageRepository: prepareTestRepo,
			SourceDigest:    prepareTestSrc,
			TargetDigest:    prepareTestTgt,
		}] = &model.DeltaGeneration{
			OrgID:           orgId,
			ImageRepository: prepareTestRepo,
			SourceDigest:    prepareTestSrc,
			TargetDigest:    prepareTestTgt,
			Status:          model.DeltaGenerationSucceeded,
		}
		emit := &emitSpy{}
		status := &statusSpy{}
		r := eligibleFleetResolver(fleetWithTV("fleet-1", "tv-1"), deviceWithOS("d1", true, prepareTestSrc))
		r.Expand = func(_ context.Context, _ uuid.UUID, _ *domain.Device, _ tasks.RenderedSpec, cands []DeltaCandidate) []DeltaCandidate {
			return append(cands, DeltaCandidate{ImageRepository: "quay.io/apps/web", CurrentDigest: "sha256:ccc", NewDigest: "sha256:ddd"})
		}
		p := newTestPreparer(t, store, r, status, &resumeSpy{}, emit)

		err := p.Prepare(ctx, fleetPrepareEvent(orgId, "fleet-1", "tv-1"))
		require.NoError(t, err)
		assert.Equal(t, 1, store.insertGensN)
		require.Len(t, emit.events, 1)
		var payload generateTask.GenerateDeltaPayload
		require.NoError(t, json.Unmarshal([]byte(emit.events[0].Message), &payload))
		assert.Equal(t, "quay.io/apps/web", payload.ImageRepository)
		require.Len(t, status.sets, 1)
		assert.Equal(t, 1, status.sets[0].completed)
		assert.Equal(t, 2, status.sets[0].total)
		assert.Equal(t, model.DeltaPrepareWaiting, firstPrepare(store).Status)
	})

	t.Run("When a generation is failed and another is new it should enqueue the failed-to-pending key", func(t *testing.T) {
		store := newFakePrepareStore()
		failedKey := deltastore.GenerationKey{
			OrgID:           orgId,
			ImageRepository: "quay.io/apps/web",
			SourceDigest:    "sha256:ccc",
			TargetDigest:    "sha256:ddd",
		}
		store.generations[failedKey] = &model.DeltaGeneration{
			OrgID:           orgId,
			ImageRepository: "quay.io/apps/web",
			SourceDigest:    "sha256:ccc",
			TargetDigest:    "sha256:ddd",
			Status:          model.DeltaGenerationFailed,
		}
		emit := &emitSpy{}
		r := eligibleFleetResolver(fleetWithTV("fleet-1", "tv-1"), deviceWithOS("d1", true, prepareTestSrc))
		r.Expand = func(_ context.Context, _ uuid.UUID, _ *domain.Device, _ tasks.RenderedSpec, cands []DeltaCandidate) []DeltaCandidate {
			return append(cands, DeltaCandidate{ImageRepository: "quay.io/apps/web", CurrentDigest: "sha256:ccc", NewDigest: "sha256:ddd"})
		}
		p := newTestPreparer(t, store, r, &statusSpy{}, &resumeSpy{}, emit)

		err := p.Prepare(ctx, fleetPrepareEvent(orgId, "fleet-1", "tv-1"))
		require.NoError(t, err)
		assert.Equal(t, 1, store.insertGensN)
		assert.Equal(t, model.DeltaGenerationPending, store.generations[failedKey].Status)
		require.Len(t, emit.events, 2)
		assert.Equal(t, model.DeltaPrepareWaiting, firstPrepare(store).Status)
	})

	t.Run("When a device Prepare runs it should persist Device kind with the event spec hash", func(t *testing.T) {
		store := newFakePrepareStore()
		status := &statusSpy{}
		device := deviceWithOS("d1", true, prepareTestSrc)
		p := newTestPreparer(t, store, eligibleDeviceResolver(device), status, &resumeSpy{}, &emitSpy{})

		err := p.Prepare(ctx, devicePrepareEventWithSpecHashAndResourceVersion(orgId, "d1", prepareTestHash, "2"))
		require.NoError(t, err)
		prep := firstPrepare(store)
		assert.Equal(t, domain.DeviceKind, prep.Kind)
		assert.Equal(t, "d1", prep.Name)
		require.NotNil(t, prep.SpecHash)
		assert.Equal(t, prepareTestHash, *prep.SpecHash)
		assert.Nil(t, prep.TemplateVersion)
		require.Len(t, status.sets, 1)
		assert.Equal(t, domain.DeviceKind, status.sets[0].kind)
	})

	t.Run("When a waiting device prepare has the same spec hash it should keep the original row", func(t *testing.T) {
		store := newFakePrepareStore()
		existing := store.seedWaiting(orgId, domain.DeviceKind, "d1", nil, lo.ToPtr(prepareTestHash), time.Now())
		store.generations[deltastore.GenerationKey{
			OrgID:           orgId,
			ImageRepository: prepareTestRepo,
			SourceDigest:    prepareTestSrc,
			TargetDigest:    prepareTestTgt,
		}] = &model.DeltaGeneration{
			OrgID:           orgId,
			ImageRepository: prepareTestRepo,
			SourceDigest:    prepareTestSrc,
			TargetDigest:    prepareTestTgt,
			Status:          model.DeltaGenerationPending,
		}
		device := deviceWithOS("d1", true, prepareTestSrc)
		device.Spec.Os.Image = prepareTestImage
		device.Metadata.ResourceVersion = lo.ToPtr("99")
		resume := &resumeSpy{}
		emit := &emitSpy{}
		p := newTestPreparer(t, store, eligibleDeviceResolver(device), &statusSpy{}, resume, emit)

		err := p.Prepare(ctx, devicePrepareEvent(orgId, "d1"))
		require.NoError(t, err)
		assert.Len(t, store.prepares, 1)
		assert.Equal(t, existing.ID, firstPrepare(store).ID)
		assert.Len(t, emit.events, 1)
	})

	t.Run("When device spec hash changes it should fail the old prepare and insert a new one", func(t *testing.T) {
		store := newFakePrepareStore()
		old := store.seedWaiting(orgId, domain.DeviceKind, "d1", nil, lo.ToPtr("old-spec-hash"), time.Now())
		device := deviceWithOS("d1", true, prepareTestSrc)
		resume := &resumeSpy{}
		emit := &emitSpy{}
		p := newTestPreparer(t, store, eligibleDeviceResolver(device), &statusSpy{}, resume, emit)

		err := p.Prepare(ctx, devicePrepareEventWithSpecHashAndResourceVersion(orgId, "d1", prepareTestHash, "2"))
		require.NoError(t, err)
		assert.Equal(t, model.DeltaPrepareFailed, store.prepares[old.ID].Status)
		assert.Len(t, store.prepares, 2)
		require.Len(t, emit.events, 1)
		assert.Equal(t, domain.EventReasonGenerateDelta, emit.events[0].Reason)
	})

	t.Run("When a fleet prepare event is superseded it should not emit completion", func(t *testing.T) {
		store := newFakePrepareStore()
		fleet := &domain.Fleet{Metadata: domain.ObjectMeta{Name: lo.ToPtr("fleet-1")}, Spec: domain.FleetSpec{}}
		status := &statusSpy{}
		emit := &emitSpy{}
		resolver := eligibleFleetResolver(fleet, deviceWithOS("d1", true, prepareTestSrc))
		resolver.TemplateVersionService = mockTemplateVersionService(
			func(_ context.Context, _ uuid.UUID, fleet, name string) (*domain.TemplateVersion, error) {
				return &domain.TemplateVersion{
					Metadata: domain.ObjectMeta{Name: lo.ToPtr(name)},
					Spec:     domain.TemplateVersionSpec{Fleet: fleet},
				}, nil
			},
			func(_ context.Context, _ uuid.UUID, fleet string) (*domain.TemplateVersion, error) {
				return &domain.TemplateVersion{
					Metadata: domain.ObjectMeta{Name: lo.ToPtr("tv-current")},
					Spec:     domain.TemplateVersionSpec{Fleet: fleet},
				}, nil
			},
		)
		p := newTestPreparer(t, store, resolver, status, &resumeSpy{}, emit)

		err := p.Prepare(ctx, fleetPrepareEvent(orgId, "fleet-1", "tv-from-event"))
		require.NoError(t, err)
		assert.Empty(t, store.prepares)
		assert.Empty(t, status.sets)
		assert.Empty(t, status.clears)
		assert.Empty(t, emit.events)
	})

	t.Run("When a standalone device Prepare runs it should use deployment wait and timeout", func(t *testing.T) {
		store := newFakePrepareStore()
		device := deviceWithOS("d1", true, prepareTestSrc)
		device.Metadata.Generation = lo.ToPtr(int64(3))
		deployWait := 15 * time.Minute
		p := newTestPreparer(t, store, eligibleDeviceResolver(device), &statusSpy{}, &resumeSpy{}, &emitSpy{})
		p.MaxWaitForDelta = &deployWait
		p.DeltaGenerationTimeout = 45 * time.Minute

		err := p.Prepare(ctx, devicePrepareEvent(orgId, "d1"))
		require.NoError(t, err)
		require.NotNil(t, firstPrepare(store).Deadline)
	})
}

func TestMaxWaitFromFleet(t *testing.T) {
	deploy := 30 * time.Minute

	t.Run("When fleet policy is omitted it should use deployment", func(t *testing.T) {
		got, err := maxWaitFromFleet(&domain.Fleet{Spec: domain.FleetSpec{}}, &deploy)
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, deploy, *got)
	})

	t.Run("When fleet maxWaitForDelta is set it should override deployment", func(t *testing.T) {
		d := domain.Duration("0s")
		got, err := maxWaitFromFleet(&domain.Fleet{Spec: domain.FleetSpec{RolloutPolicy: &domain.RolloutPolicy{DeltaGeneration: &domain.RolloutPolicyDeltaGeneration{MaxWaitForDelta: &d}}}}, &deploy)
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, time.Duration(0), *got)
	})

	t.Run("When fleet is nil it should use deployment", func(t *testing.T) {
		got, err := maxWaitFromFleet(nil, &deploy)
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, deploy, *got)
	})
}

func TestJobTimeoutFromFleet(t *testing.T) {
	deploy := 30 * time.Minute

	t.Run("When fleet policy is omitted it should use deployment", func(t *testing.T) {
		got, err := jobTimeoutFromFleet(&domain.Fleet{Spec: domain.FleetSpec{}}, deploy)
		require.NoError(t, err)
		assert.Equal(t, deploy, got)
	})

	t.Run("When fleet deltaGenerationTimeout is set it should override deployment", func(t *testing.T) {
		d := domain.Duration("2m")
		got, err := jobTimeoutFromFleet(&domain.Fleet{Spec: domain.FleetSpec{RolloutPolicy: &domain.RolloutPolicy{DeltaGeneration: &domain.RolloutPolicyDeltaGeneration{DeltaGenerationTimeout: &d}}}}, deploy)
		require.NoError(t, err)
		assert.Equal(t, 2*time.Minute, got)
	})

	t.Run("When fleet is nil it should use deployment", func(t *testing.T) {
		got, err := jobTimeoutFromFleet(nil, deploy)
		require.NoError(t, err)
		assert.Equal(t, deploy, got)
	})
}

type fakePrepareStore struct {
	prepares    map[uuid.UUID]*model.DeltaPrepare
	waiting     map[string]uuid.UUID
	generations map[deltastore.GenerationKey]*model.DeltaGeneration
	joins       []model.DeltaPrepareGeneration
	insertErr   error
	casErr      error
	insertGensN int
}

func newFakePrepareStore() *fakePrepareStore {
	return &fakePrepareStore{
		prepares:    map[uuid.UUID]*model.DeltaPrepare{},
		waiting:     map[string]uuid.UUID{},
		generations: map[deltastore.GenerationKey]*model.DeltaGeneration{},
	}
}

func (f *fakePrepareStore) identityKey(orgID uuid.UUID, kind, name string) string {
	return orgID.String() + "/" + kind + "/" + name
}

func (f *fakePrepareStore) seedWaiting(orgID uuid.UUID, kind, name string, tv, specHash *string, created time.Time) *model.DeltaPrepare {
	prep := &model.DeltaPrepare{
		ID:                    uuid.New(),
		OrgID:                 orgID,
		Kind:                  kind,
		Name:                  name,
		TemplateVersion:       tv,
		SpecHash:              specHash,
		SourceResourceVersion: 1,
		CreatedAt:             created,
		Status:                model.DeltaPrepareWaiting,
	}
	f.prepares[prep.ID] = prep
	f.waiting[f.identityKey(orgID, kind, name)] = prep.ID
	return prep
}

func (f *fakePrepareStore) getWaitingPrepare(_ context.Context, orgID uuid.UUID, kind, name string) (*model.DeltaPrepare, error) {
	id, ok := f.waiting[f.identityKey(orgID, kind, name)]
	if !ok {
		return nil, nil
	}
	prep := f.prepares[id]
	if prep == nil || prep.Status != model.DeltaPrepareWaiting {
		return nil, nil
	}
	cp := *prep
	return &cp, nil
}

func (f *fakePrepareStore) insertPrepare(_ context.Context, prep *model.DeltaPrepare) error {
	if f.insertErr != nil {
		return f.insertErr
	}
	if prep.ID == uuid.Nil {
		prep.ID = uuid.New()
	}
	key := f.identityKey(prep.OrgID, prep.Kind, prep.Name)
	if id, ok := f.waiting[key]; ok {
		if existing := f.prepares[id]; existing != nil && existing.Status == model.DeltaPrepareWaiting {
			return flterrors.ErrDuplicateName
		}
	}
	cp := *prep
	f.prepares[cp.ID] = &cp
	if cp.Status == model.DeltaPrepareWaiting || cp.Status == "" {
		f.waiting[key] = cp.ID
	}
	return nil
}

func (f *fakePrepareStore) insertGenerations(_ context.Context, gens []*model.DeltaGeneration) ([]model.DeltaGeneration, error) {
	f.insertGensN++
	var current []model.DeltaGeneration
	for _, gen := range gens {
		key := deltastore.GenerationKey{
			OrgID:           gen.OrgID,
			ImageRepository: gen.ImageRepository,
			SourceDigest:    gen.SourceDigest,
			TargetDigest:    gen.TargetDigest,
		}
		existing, ok := f.generations[key]
		if !ok {
			cp := *gen
			if cp.Status == "" {
				cp.Status = model.DeltaGenerationPending
			}
			f.generations[key] = &cp
			current = append(current, cp)
			continue
		}
		if existing.Status == model.DeltaGenerationFailed {
			existing.Status = model.DeltaGenerationPending
		}
		current = append(current, *existing)
	}
	return current, nil
}

func (f *fakePrepareStore) insertPrepareGenerations(_ context.Context, prepareID uuid.UUID, keys []deltastore.GenerationKey) error {
	for _, key := range keys {
		f.joins = append(f.joins, model.DeltaPrepareGeneration{
			PrepareID:       prepareID,
			OrgID:           key.OrgID,
			ImageRepository: key.ImageRepository,
			SourceDigest:    key.SourceDigest,
			TargetDigest:    key.TargetDigest,
		})
	}
	if prepare := f.prepares[prepareID]; prepare != nil && prepare.Status == model.DeltaPrepareWaiting {
		count := 0
		for _, join := range f.joins {
			if join.PrepareID == prepareID {
				key := deltastore.GenerationKey{
					OrgID:           join.OrgID,
					ImageRepository: join.ImageRepository,
					SourceDigest:    join.SourceDigest,
					TargetDigest:    join.TargetDigest,
				}
				generation := f.generations[key]
				if generation == nil || !isTerminalGeneration(generation.Status) {
					count++
				}
			}
		}
		prepare.PendingGenerationsCount = count
		prepare.ResourceVersion++
		if count == 0 {
			prepare.Status = model.DeltaPrepareComplete
			delete(f.waiting, f.identityKey(prepare.OrgID, prepare.Kind, prepare.Name))
		}
	}
	return nil
}

func (f *fakePrepareStore) getGeneration(_ context.Context, key deltastore.GenerationKey, _ ...deltastore.GenerationGetOption) (*model.DeltaGeneration, error) {
	gen, ok := f.generations[key]
	if !ok {
		return nil, flterrors.ErrResourceNotFound
	}
	cp := *gen
	return &cp, nil
}

type fakePrepareService struct {
	store  *fakePrepareStore
	status preparingStatus
}

func (f *fakePrepareService) CreateDeltaPrepare(ctx context.Context, prepare *model.DeltaPrepare) error {
	return f.store.insertPrepare(ctx, prepare)
}

func (f *fakePrepareService) CreateOrReplaceWaitingDeltaPrepare(ctx context.Context, prepare *model.DeltaPrepare) (deltapreparestore.PrepareAdmission, error) {
	if f.store.insertErr != nil {
		return deltapreparestore.PrepareAdmission{}, f.store.insertErr
	}
	var latest *model.DeltaPrepare
	for _, candidate := range f.store.prepares {
		if candidate.OrgID != prepare.OrgID || candidate.Kind != prepare.Kind || candidate.Name != prepare.Name {
			continue
		}
		if latest == nil || candidate.SourceResourceVersion > latest.SourceResourceVersion {
			latest = candidate
		}
	}
	if latest != nil {
		if prepare.SourceResourceVersion < latest.SourceResourceVersion {
			copy := *latest
			return deltapreparestore.PrepareAdmission{Prepare: &copy}, nil
		}
		if prepare.SourceResourceVersion == latest.SourceResourceVersion {
			identity := prepareIdentity{templateVersion: prepare.TemplateVersion, specHash: prepare.SpecHash, resourceVersion: prepare.SourceResourceVersion}
			if !samePrepareIdentity(latest, identity) {
				return deltapreparestore.PrepareAdmission{}, errors.New("conflicting delta prepares")
			}
			copy := *latest
			return deltapreparestore.PrepareAdmission{Prepare: &copy, Accepted: latest.Status == model.DeltaPrepareWaiting}, nil
		}
	}

	key := f.store.identityKey(prepare.OrgID, prepare.Kind, prepare.Name)
	replaced := latest != nil && prepare.SourceResourceVersion > latest.SourceResourceVersion
	if id, ok := f.store.waiting[key]; ok {
		if waiting := f.store.prepares[id]; waiting != nil {
			waiting.Status = model.DeltaPrepareFailed
			waiting.ResourceVersion++
			replaced = true
		}
		delete(f.store.waiting, key)
	}
	if err := f.store.insertPrepare(ctx, prepare); err != nil {
		return deltapreparestore.PrepareAdmission{}, err
	}
	copy := *prepare
	if replaced && f.status != nil {
		if err := f.status.Clear(ctx, prepare.OrgID, prepare.Kind, prepare.Name); err != nil {
			return deltapreparestore.PrepareAdmission{}, err
		}
	}
	return deltapreparestore.PrepareAdmission{Prepare: &copy, Accepted: true, Replaced: replaced}, nil
}

func (f *fakePrepareService) GetDeltaPrepareByID(_ context.Context, id uuid.UUID, _ ...deltapreparestore.PrepareGetOption) (*model.DeltaPrepare, error) {
	prepare := f.store.prepares[id]
	if prepare == nil {
		return nil, flterrors.ErrResourceNotFound
	}
	copy := *prepare
	return &copy, nil
}

func (f *fakePrepareService) GetLatestDeltaPrepareForResource(ctx context.Context, orgID uuid.UUID, kind, name string, _ ...deltapreparestore.PrepareGetOption) (*model.DeltaPrepare, error) {
	return f.store.getWaitingPrepare(ctx, orgID, kind, name)
}

func (f *fakePrepareService) ListDeltaPrepares(_ context.Context, ids []uuid.UUID) ([]model.DeltaPrepare, error) {
	result := make([]model.DeltaPrepare, 0, len(ids))
	for _, id := range ids {
		if prepare := f.store.prepares[id]; prepare != nil {
			result = append(result, *prepare)
		}
	}
	return result, nil
}

func (f *fakePrepareService) UpdateDeltaPrepare(_ context.Context, _ int64, prepare *model.DeltaPrepare) (*model.DeltaPrepare, error) {
	if f.store.casErr != nil {
		return nil, f.store.casErr
	}
	current := f.store.prepares[prepare.ID]
	if current == nil {
		return nil, flterrors.ErrNoRowsUpdated
	}
	copy := *prepare
	copy.ResourceVersion = current.ResourceVersion + 1
	f.store.prepares[copy.ID] = &copy
	key := f.store.identityKey(copy.OrgID, copy.Kind, copy.Name)
	if copy.Status == model.DeltaPrepareWaiting {
		f.store.waiting[key] = copy.ID
	} else {
		delete(f.store.waiting, key)
	}
	return &copy, nil
}

func (f *fakePrepareService) DecrementPendingGenerationsForGeneration(_ context.Context, key deltastore.GenerationKey, _ string) ([]deltapreparestore.PrepareProgress, error) {
	generation := f.store.generations[key]
	if generation == nil || !isTerminalGeneration(generation.Status) {
		return nil, nil
	}
	var claimed []deltapreparestore.PrepareProgress
	seen := make(map[uuid.UUID]struct{})
	for _, join := range f.store.joins {
		if join.OrgID != key.OrgID || join.ImageRepository != key.ImageRepository || join.SourceDigest != key.SourceDigest || join.TargetDigest != key.TargetDigest {
			continue
		}
		if _, ok := seen[join.PrepareID]; ok {
			continue
		}
		seen[join.PrepareID] = struct{}{}
		prepare := f.store.prepares[join.PrepareID]
		if prepare == nil || prepare.Status != model.DeltaPrepareWaiting || prepare.PendingGenerationsCount <= 0 {
			continue
		}
		prepare.PendingGenerationsCount--
		prepare.ResourceVersion++
		if prepare.PendingGenerationsCount == 0 {
			prepare.Status = model.DeltaPrepareComplete
			delete(f.store.waiting, f.store.identityKey(prepare.OrgID, prepare.Kind, prepare.Name))
			copy := *prepare
			completed, total := f.generationCounts(copy.ID)
			claimed = append(claimed, deltapreparestore.PrepareProgress{
				Prepare:   copy,
				Completed: completed,
				Total:     total,
			})
		}
	}
	return claimed, nil
}

func (f *fakePrepareService) generationCounts(prepareID uuid.UUID) (int, int) {
	completed, total := 0, 0
	for _, join := range f.store.joins {
		if join.PrepareID != prepareID {
			continue
		}
		total++
		key := deltastore.GenerationKey{OrgID: join.OrgID, ImageRepository: join.ImageRepository, SourceDigest: join.SourceDigest, TargetDigest: join.TargetDigest}
		if generation := f.store.generations[key]; generation != nil && isTerminalGeneration(generation.Status) {
			completed++
		}
	}
	return completed, total
}

func (f *fakePrepareService) SetDeltaPreparingStatus(ctx context.Context, prepare *model.DeltaPrepare, completed, total int) error {
	if f.status == nil {
		return nil
	}
	return f.status.SetPreparing(ctx, prepare, completed, total)
}

func (f *fakePrepareService) ClearDeltaPreparingStatus(ctx context.Context, orgID uuid.UUID, kind, name string) error {
	if f.status == nil {
		return nil
	}
	return f.status.Clear(ctx, orgID, kind, name)
}

type fakeGenerationService struct {
	store *fakePrepareStore
}

func (f *fakeGenerationService) CreateDeltaGenerations(ctx context.Context, generations []*model.DeltaGeneration) ([]model.DeltaGeneration, error) {
	return f.store.insertGenerations(ctx, generations)
}

func (f *fakeGenerationService) GetDeltaGeneration(ctx context.Context, key deltastore.GenerationKey, opts ...deltastore.GenerationGetOption) (*model.DeltaGeneration, error) {
	return f.store.getGeneration(ctx, key, opts...)
}

func (f *fakeGenerationService) ListDeltaGenerations(_ context.Context, keys []deltastore.GenerationKey) ([]model.DeltaGeneration, error) {
	result := make([]model.DeltaGeneration, 0, len(keys))
	for _, key := range keys {
		if generation := f.store.generations[key]; generation != nil {
			result = append(result, *generation)
		}
	}
	return result, nil
}

func (f *fakeGenerationService) UpdateDeltaGeneration(_ context.Context, _ int64, generation *model.DeltaGeneration) (*model.DeltaGeneration, error) {
	key := deltastore.GenerationKey{OrgID: generation.OrgID, ImageRepository: generation.ImageRepository, SourceDigest: generation.SourceDigest, TargetDigest: generation.TargetDigest}
	current := f.store.generations[key]
	if current == nil {
		return nil, flterrors.ErrNoRowsUpdated
	}
	copy := *generation
	copy.ResourceVersion = current.ResourceVersion + 1
	f.store.generations[key] = &copy
	return &copy, nil
}

type fakePrepareGenerationService struct {
	store *fakePrepareStore
}

func (f *fakePrepareGenerationService) CreateDeltaPrepareGenerations(ctx context.Context, joins []*model.DeltaPrepareGeneration) (deltapreparegenerationstore.CreateDeltaPrepareGenerationsResult, error) {
	if len(joins) == 0 {
		return deltapreparegenerationstore.CreateDeltaPrepareGenerationsResult{}, nil
	}
	keys := make([]deltastore.GenerationKey, 0, len(joins))
	for _, join := range joins {
		keys = append(keys, deltastore.GenerationKey{OrgID: join.OrgID, ImageRepository: join.ImageRepository, SourceDigest: join.SourceDigest, TargetDigest: join.TargetDigest})
	}
	if err := f.store.insertPrepareGenerations(ctx, joins[0].PrepareID, keys); err != nil {
		return deltapreparegenerationstore.CreateDeltaPrepareGenerationsResult{}, err
	}
	result := deltapreparegenerationstore.CreateDeltaPrepareGenerationsResult{InsertedJoins: joins}
	if prepare := f.store.prepares[joins[0].PrepareID]; prepare != nil {
		result.UpdatedPrepares = map[uuid.UUID]model.DeltaPrepare{prepare.ID: *prepare}
	}
	return result, nil
}

func (f *fakePrepareGenerationService) ListDeltaPrepareGenerations(_ context.Context, filter deltapreparegenerationstore.ListFilter) ([]model.DeltaPrepareGeneration, error) {
	result := make([]model.DeltaPrepareGeneration, 0, len(f.store.joins))
	for _, join := range f.store.joins {
		if filter.PrepareID != nil && join.PrepareID != *filter.PrepareID {
			continue
		}
		if filter.GenerationKey != nil {
			key := filter.GenerationKey
			if join.OrgID != key.OrgID || join.ImageRepository != key.ImageRepository || join.SourceDigest != key.SourceDigest || join.TargetDigest != key.TargetDigest {
				continue
			}
		}
		result = append(result, join)
	}
	return result, nil
}

var _ deltaprepare.Service = (*fakePrepareService)(nil)
var _ deltageneration.Service = (*fakeGenerationService)(nil)
var _ deltapreparegeneration.Service = (*fakePrepareGenerationService)(nil)

type statusSpy struct {
	sets   []statusCall
	clears []statusCall
	order  *[]string
}

type statusCall struct {
	kind, name                string
	completed, total          int
	sourceResourceVersion     int64
	templateVersion, specHash *string
}

func (s *statusSpy) SetPreparing(_ context.Context, prepare *model.DeltaPrepare, completed, total int) error {
	if s.order != nil {
		*s.order = append(*s.order, "status")
	}
	s.sets = append(s.sets, statusCall{
		kind:                  prepare.Kind,
		name:                  prepare.Name,
		completed:             completed,
		total:                 total,
		sourceResourceVersion: prepare.SourceResourceVersion,
		templateVersion:       prepare.TemplateVersion,
		specHash:              prepare.SpecHash,
	})
	return nil
}

func (s *statusSpy) Clear(_ context.Context, _ uuid.UUID, kind, name string) error {
	s.clears = append(s.clears, statusCall{kind: kind, name: name})
	return nil
}

type emitSpy struct {
	events []*domain.Event
	err    error
	order  *[]string
}

func (e *emitSpy) emit(_ context.Context, _ uuid.UUID, event *domain.Event) error {
	if e.err != nil {
		return e.err
	}
	if event == nil {
		return nil
	}
	cp := *event
	e.events = append(e.events, &cp)
	if e.order != nil {
		*e.order = append(*e.order, "emit")
	}
	return nil
}

type resumeSpy struct{}

type preparingStatus interface {
	SetPreparing(context.Context, *model.DeltaPrepare, int, int) error
	Clear(context.Context, uuid.UUID, string, string) error
}

func newTestPreparer(t *testing.T, store *fakePrepareStore, resolver *Resolver, status preparingStatus, _ *resumeSpy, emit *emitSpy) *Handler {
	if resolver.FleetService == nil {
		resolver.FleetService = mockFleetService(func(_ context.Context, _ uuid.UUID, _ string) (*domain.Fleet, error) {
			return fleetWithTV("fleet-1", "tv-1"), nil
		})
	}
	if resolver.DeviceService == nil {
		resolver.DeviceService = mockDeviceService(nil, func(_ context.Context, _ uuid.UUID, _ string) ([]*domain.Device, error) {
			return nil, nil
		})
	}
	if resolver.RepositoryService == nil {
		resolver.RepositoryService = testRepositoryService()
	}
	if resolver.Config == nil {
		resolver.Config = &deltaconfig.DeltaGenerationConfig{}
	}
	if resolver.TemplateVersionService == nil {
		resolver.TemplateVersionService = mockTemplateVersionService(func(_ context.Context, _ uuid.UUID, fleet, name string) (*domain.TemplateVersion, error) {
			return &domain.TemplateVersion{
				Metadata: domain.ObjectMeta{Name: lo.ToPtr(name)},
				Spec:     domain.TemplateVersionSpec{Fleet: fleet},
			}, nil
		})
	}
	emitFunc := emit.emit
	p, err := NewHandler(
		resolver,
		emitFunc,
		&fakePrepareService{store: store, status: status},
		&fakeGenerationService{store: store},
		&fakePrepareGenerationService{store: store},
	)
	if err != nil {
		t.Fatalf("creating prepare handler: %v", err)
	}
	p.DeltaGenerationTimeout = 30 * time.Minute
	return p
}

func firstPrepare(store *fakePrepareStore) *model.DeltaPrepare {
	for _, prep := range store.prepares {
		return prep
	}
	return nil
}

func fleetWithTV(name, _ string) *domain.Fleet {
	return &domain.Fleet{
		Metadata: domain.ObjectMeta{
			Name: lo.ToPtr(name),
		},
		Spec: domain.FleetSpec{},
	}
}

func skipGenerateDeltaResolver() *Resolver {
	return &Resolver{
		FleetService: mockFleetService(func(_ context.Context, _ uuid.UUID, _ string) (*domain.Fleet, error) {
			f := fleetWithTV("fleet-1", "tv-1")
			f.Spec.RolloutPolicy = &domain.RolloutPolicy{DeltaGeneration: &domain.RolloutPolicyDeltaGeneration{GenerateDelta: lo.ToPtr(false)}}
			return f, nil
		}),
		TemplateVersionService: mockTemplateVersionService(func(_ context.Context, _ uuid.UUID, _, _ string) (*domain.TemplateVersion, error) {
			return &domain.TemplateVersion{}, nil
		}),
		RepositoryService: testRepositoryService(),
		Config:            &deltaconfig.DeltaGenerationConfig{},
	}
}

func eligibleFleetResolver(fleet *domain.Fleet, devices ...*domain.Device) *Resolver {
	return &Resolver{
		FleetService: mockFleetService(func(_ context.Context, _ uuid.UUID, _ string) (*domain.Fleet, error) {
			return fleet, nil
		}),
		TemplateVersionService: mockTemplateVersionService(func(_ context.Context, _ uuid.UUID, _, name string) (*domain.TemplateVersion, error) {
			return &domain.TemplateVersion{
				Metadata: domain.ObjectMeta{Name: lo.ToPtr(name)},
				Status:   &domain.TemplateVersionStatus{Os: &domain.DeviceOsSpec{Image: prepareTestImage}},
			}, nil
		}),
		DeviceService: mockDeviceService(nil, func(_ context.Context, _ uuid.UUID, _ string) ([]*domain.Device, error) {
			return devices, nil
		}),
		RepositoryService: testRepositoryService(),
		Config:            &deltaconfig.DeltaGenerationConfig{},
		Render: func(_ context.Context, _ uuid.UUID, spec *domain.DeviceSpec) (tasks.RenderedSpec, error) {
			return tasks.RenderedSpec{OsImage: spec.Os.Image}, nil
		},
		Inspect: func(_ context.Context, _ uuid.UUID, _ string) (string, error) {
			return prepareTestTgt, nil
		},
	}
}

func eligibleDeviceResolver(device *domain.Device) *Resolver {
	return &Resolver{
		DeviceService: mockDeviceService(func(_ context.Context, _ uuid.UUID, _ string) (*domain.Device, error) {
			return device, nil
		}, nil),
		RepositoryService: testRepositoryService(),
		Config:            &deltaconfig.DeltaGenerationConfig{},
		Render: func(_ context.Context, _ uuid.UUID, spec *domain.DeviceSpec) (tasks.RenderedSpec, error) {
			return tasks.RenderedSpec{OsImage: spec.Os.Image}, nil
		},
		Inspect: func(_ context.Context, _ uuid.UUID, _ string) (string, error) {
			return prepareTestTgt, nil
		},
	}
}
