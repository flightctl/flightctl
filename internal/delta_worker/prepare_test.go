package delta_worker

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	deviceservice "github.com/flightctl/flightctl/internal/service/device"
	fleetservice "github.com/flightctl/flightctl/internal/service/fleet"
	deltastore "github.com/flightctl/flightctl/internal/store/delta"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/tasks"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/internal/worker_client"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gomock "go.uber.org/mock/gomock"
)

const (
	prepareTestImage = "quay.io/acme/os:v2"
	prepareTestRepo  = "quay.io/acme/os"
	prepareTestSrc   = "sha256:aaa"
	prepareTestTgt   = "sha256:bbb"
)

func TestPrepare_SkipPaths(t *testing.T) {
	orgId := uuid.New()
	ctx := context.Background()

	t.Run("When generateDelta is false it should Resume without inserting", func(t *testing.T) {
		store := newFakePrepareStore()
		status := &statusSpy{}
		resume := &resumeSpy{}
		p := newTestPreparer(t, store, skipGenerateDeltaResolver(), status, resume, nil)
		err := p.Prepare(ctx, fleetPrepareEvent(orgId, "fleet-1", "tv-1"))
		require.NoError(t, err)
		assert.Empty(t, store.prepares)
		assert.Contains(t, resumeEventReasons(p), domain.EventReasonFleetRolloutStarted)
		assert.Empty(t, status.sets)
		require.Len(t, status.clears, 1)
		assert.Equal(t, domain.FleetKind, status.clears[0].kind)
	})

	t.Run("When generateDelta is false and a wait is in flight it should fail the waiting prepare", func(t *testing.T) {
		store := newFakePrepareStore()
		existing := store.seedWaiting(orgId, domain.FleetKind, "fleet-1", lo.ToPtr("tv-1"), nil, time.Now())
		resume := &resumeSpy{}
		p := newTestPreparer(t, store, skipGenerateDeltaResolver(), &statusSpy{}, resume, nil)
		err := p.Prepare(ctx, fleetPrepareEvent(orgId, "fleet-1", "tv-1"))
		require.NoError(t, err)
		assert.Equal(t, model.DeltaPrepareFailed, store.prepares[existing.ID].Status)
		assert.Contains(t, resumeEventReasons(p), domain.EventReasonFleetRolloutStarted)
	})

	t.Run("When the write target is missing it should Resume without inserting", func(t *testing.T) {
		store := newFakePrepareStore()
		resume := &resumeSpy{}
		p := newTestPreparer(t, store, &Resolver{
			Fleet: func(_ context.Context, _ uuid.UUID, _ string) (*domain.Fleet, error) {
				return fleetWithTV("fleet-1", "tv-1"), nil
			},
			WriteTarget: func(_ context.Context, _ uuid.UUID) (*domain.OciRepoSpec, error) {
				return nil, nil
			},
		}, &statusSpy{}, resume, nil)
		err := p.Prepare(ctx, fleetPrepareEvent(orgId, "fleet-1", "tv-1"))
		require.NoError(t, err)
		assert.Empty(t, store.prepares)
		assert.Contains(t, resumeEventReasons(p), domain.EventReasonFleetRolloutStarted)
	})

	t.Run("When no device is eligible it should Resume without inserting", func(t *testing.T) {
		store := newFakePrepareStore()
		resume := &resumeSpy{}
		fleet := fleetWithTV("fleet-1", "tv-1")
		p := newTestPreparer(t, store, eligibleFleetResolver(fleet, deviceWithOS("d1", false, prepareTestSrc)), &statusSpy{}, resume, nil)
		err := p.Prepare(ctx, fleetPrepareEvent(orgId, "fleet-1", "tv-1"))
		require.NoError(t, err)
		assert.Empty(t, store.prepares)
		assert.Contains(t, resumeEventReasons(p), domain.EventReasonFleetRolloutStarted)
	})

	t.Run("When DeltaCandidates is empty it should Resume without inserting", func(t *testing.T) {
		store := newFakePrepareStore()
		resume := &resumeSpy{}
		fleet := fleetWithTV("fleet-1", "tv-1")
		p := newTestPreparer(t, store, eligibleFleetResolver(fleet, deviceWithOS("d1", true, "")), &statusSpy{}, resume, nil)
		err := p.Prepare(ctx, fleetPrepareEvent(orgId, "fleet-1", "tv-1"))
		require.NoError(t, err)
		assert.Empty(t, store.prepares)
		assert.Contains(t, resumeEventReasons(p), domain.EventReasonFleetRolloutStarted)
	})
}

func TestCompleteNow(t *testing.T) {
	orgId := uuid.New()
	ctx := context.Background()

	t.Run("When CAS wins and live TV matches it should emit FleetRolloutStarted, annotate, SetOutOfDate, and clear preparing", func(t *testing.T) {
		store := newFakePrepareStore()
		prep := store.seedWaiting(orgId, domain.FleetKind, "fleet-1", lo.ToPtr("tv-1"), nil, time.Now())
		status := &statusSpy{}
		var annotated map[string]string
		var outOfDateOwner string
		p := newCompleteNowPreparer(t, store, eligibleFleetResolver(fleetWithTV("fleet-1", "tv-1")), status, func(ann map[string]string) {
			annotated = ann
		}, func(owner string) {
			outOfDateOwner = owner
		})

		err := p.completeNow(ctx, fleetPrepareEvent(orgId, "fleet-1", "tv-1"), prep, orgId, domain.FleetKind, "fleet-1")
		require.NoError(t, err)
		assert.Equal(t, model.DeltaPrepareComplete, store.prepares[prep.ID].Status)
		assert.Equal(t, []domain.EventReason{domain.EventReasonFleetRolloutStarted}, resumeEventReasons(p))
		assert.Equal(t, "tv-1", annotated[domain.FleetAnnotationTemplateVersion])
		assert.Equal(t, util.ResourceOwner(domain.FleetKind, "fleet-1"), outOfDateOwner)
		require.Len(t, status.clears, 1)
		assert.Equal(t, domain.FleetKind, status.clears[0].kind)
		d, err := recordedEvents(p)[0].Details.AsFleetRolloutStartedDetails()
		require.NoError(t, err)
		assert.Equal(t, domain.None, d.RolloutStrategy)
	})

	t.Run("When live fleet has deviceSelection it should emit batched FleetRolloutStarted", func(t *testing.T) {
		store := newFakePrepareStore()
		prep := store.seedWaiting(orgId, domain.FleetKind, "fleet-1", lo.ToPtr("tv-1"), nil, time.Now())
		fleet := fleetWithTV("fleet-1", "tv-1")
		fleet.Spec.RolloutPolicy = &domain.RolloutPolicy{DeviceSelection: &domain.RolloutDeviceSelection{}}
		p := newTestPreparer(t, store, eligibleFleetResolver(fleet), &statusSpy{}, &resumeSpy{}, nil)

		err := p.completeNow(ctx, fleetPrepareEvent(orgId, "fleet-1", "tv-1"), prep, orgId, domain.FleetKind, "fleet-1")
		require.NoError(t, err)
		d, err := recordedEvents(p)[0].Details.AsFleetRolloutStartedDetails()
		require.NoError(t, err)
		assert.Equal(t, domain.Batched, d.RolloutStrategy)
	})

	t.Run("When CAS wins and live TV is stale it should not emit or clear preparing", func(t *testing.T) {
		store := newFakePrepareStore()
		prep := store.seedWaiting(orgId, domain.FleetKind, "fleet-1", lo.ToPtr("tv-1"), nil, time.Now())
		status := &statusSpy{}
		p := newTestPreparer(t, store, eligibleFleetResolver(fleetWithTV("fleet-1", "tv-2")), status, &resumeSpy{}, nil)

		err := p.completeNow(ctx, fleetPrepareEvent(orgId, "fleet-1", "tv-2"), prep, orgId, domain.FleetKind, "fleet-1")
		require.NoError(t, err)
		assert.Equal(t, model.DeltaPrepareComplete, store.prepares[prep.ID].Status)
		assert.NotContains(t, resumeEventReasons(p), domain.EventReasonFleetRolloutStarted)
		assert.Empty(t, status.clears)
	})

	t.Run("When CAS misses it should not emit", func(t *testing.T) {
		store := newFakePrepareStore()
		prep := store.seedWaiting(orgId, domain.FleetKind, "fleet-1", lo.ToPtr("tv-1"), nil, time.Now())
		require.NoError(t, store.CASPrepareStatus(ctx, prep.ID, model.DeltaPrepareComplete))
		status := &statusSpy{}
		p := newTestPreparer(t, store, eligibleFleetResolver(fleetWithTV("fleet-1", "tv-1")), status, &resumeSpy{}, nil)

		err := p.completeNow(ctx, fleetPrepareEvent(orgId, "fleet-1", "tv-1"), prep, orgId, domain.FleetKind, "fleet-1")
		require.NoError(t, err)
		assert.NotContains(t, resumeEventReasons(p), domain.EventReasonFleetRolloutStarted)
		assert.Empty(t, status.clears)
	})

	t.Run("When two completeNow calls race it should emit only once", func(t *testing.T) {
		store := newFakePrepareStore()
		prep := store.seedWaiting(orgId, domain.FleetKind, "fleet-1", lo.ToPtr("tv-1"), nil, time.Now())
		p := newTestPreparer(t, store, eligibleFleetResolver(fleetWithTV("fleet-1", "tv-1")), &statusSpy{}, &resumeSpy{}, nil)

		require.NoError(t, p.completeNow(ctx, fleetPrepareEvent(orgId, "fleet-1", "tv-1"), prep, orgId, domain.FleetKind, "fleet-1"))
		require.NoError(t, p.completeNow(ctx, fleetPrepareEvent(orgId, "fleet-1", "tv-1"), prep, orgId, domain.FleetKind, "fleet-1"))
		assert.Equal(t, []domain.EventReason{domain.EventReasonFleetRolloutStarted}, resumeEventReasons(p))
	})

	t.Run("When the prepare is not waiting it should not emit", func(t *testing.T) {
		store := newFakePrepareStore()
		prep := store.seedWaiting(orgId, domain.FleetKind, "fleet-1", lo.ToPtr("tv-1"), nil, time.Now())
		require.NoError(t, store.CASPrepareStatus(ctx, prep.ID, model.DeltaPrepareFailed))
		p := newTestPreparer(t, store, eligibleFleetResolver(fleetWithTV("fleet-1", "tv-1")), &statusSpy{}, &resumeSpy{}, nil)

		err := p.completeNow(ctx, fleetPrepareEvent(orgId, "fleet-1", "tv-1"), prep, orgId, domain.FleetKind, "fleet-1")
		require.NoError(t, err)
		assert.NotContains(t, resumeEventReasons(p), domain.EventReasonFleetRolloutStarted)
	})

	t.Run("When device RV matches it should emit DeltaGenerationCompleted and clear preparing", func(t *testing.T) {
		store := newFakePrepareStore()
		rv := int64(7)
		prep := store.seedWaiting(orgId, domain.DeviceKind, "d1", nil, &rv, time.Now())
		status := &statusSpy{}
		device := deviceWithOS("d1", true, prepareTestSrc)
		device.Metadata.Generation = lo.ToPtr(int64(7))
		p := newTestPreparer(t, store, eligibleDeviceResolver(device), status, &resumeSpy{}, nil)

		err := p.completeNow(ctx, devicePrepareEvent(orgId, "d1"), prep, orgId, domain.DeviceKind, "d1")
		require.NoError(t, err)
		assert.Equal(t, []domain.EventReason{domain.EventReasonDeltaGenerationCompleted}, resumeEventReasons(p))
		require.Len(t, status.clears, 1)
		assert.Equal(t, domain.DeviceKind, status.clears[0].kind)
	})

	t.Run("When device RV is stale it should not emit or clear preparing", func(t *testing.T) {
		store := newFakePrepareStore()
		rv := int64(7)
		prep := store.seedWaiting(orgId, domain.DeviceKind, "d1", nil, &rv, time.Now())
		status := &statusSpy{}
		device := deviceWithOS("d1", true, prepareTestSrc)
		device.Metadata.Generation = lo.ToPtr(int64(8))
		p := newTestPreparer(t, store, eligibleDeviceResolver(device), status, &resumeSpy{}, nil)

		err := p.completeNow(ctx, devicePrepareEvent(orgId, "d1"), prep, orgId, domain.DeviceKind, "d1")
		require.NoError(t, err)
		assert.NotContains(t, resumeEventReasons(p), domain.EventReasonDeltaGenerationCompleted)
		assert.Empty(t, status.clears)
	})
}

func TestCompleteWaitingIfTerminal(t *testing.T) {
	orgId := uuid.New()
	ctx := context.Background()
	keyA := deltastore.GenerationKey{OrgID: orgId, ImageRepository: prepareTestRepo, SourceDigest: prepareTestSrc, TargetDigest: prepareTestTgt}
	keyB := deltastore.GenerationKey{OrgID: orgId, ImageRepository: prepareTestRepo, SourceDigest: "sha256:ccc", TargetDigest: prepareTestTgt}

	t.Run("When every joined generation is terminal it should complete and emit", func(t *testing.T) {
		store := newFakePrepareStore()
		prep := store.seedWaiting(orgId, domain.FleetKind, "fleet-1", lo.ToPtr("tv-1"), nil, time.Now())
		require.NoError(t, store.InsertPrepareGenerations(ctx, prep.ID, []deltastore.GenerationKey{keyA, keyB}))
		store.generations[keyA] = &model.DeltaGeneration{Status: model.DeltaGenerationSucceeded}
		store.generations[keyB] = &model.DeltaGeneration{Status: model.DeltaGenerationFailed}
		status := &statusSpy{}
		p := newTestPreparer(t, store, eligibleFleetResolver(fleetWithTV("fleet-1", "tv-1")), status, &resumeSpy{}, nil)

		require.NoError(t, p.completeWaitingIfTerminal(ctx, keyA))
		assert.Equal(t, model.DeltaPrepareComplete, store.prepares[prep.ID].Status)
		assert.Contains(t, resumeEventReasons(p), domain.EventReasonFleetRolloutStarted)
		require.Len(t, status.clears, 1)
	})

	t.Run("When a joined generation is still pending it should not complete", func(t *testing.T) {
		store := newFakePrepareStore()
		prep := store.seedWaiting(orgId, domain.FleetKind, "fleet-1", lo.ToPtr("tv-1"), nil, time.Now())
		require.NoError(t, store.InsertPrepareGenerations(ctx, prep.ID, []deltastore.GenerationKey{keyA, keyB}))
		store.generations[keyA] = &model.DeltaGeneration{Status: model.DeltaGenerationSucceeded}
		store.generations[keyB] = &model.DeltaGeneration{Status: model.DeltaGenerationPending}
		p := newTestPreparer(t, store, eligibleFleetResolver(fleetWithTV("fleet-1", "tv-1")), &statusSpy{}, &resumeSpy{}, nil)

		require.NoError(t, p.completeWaitingIfTerminal(ctx, keyA))
		assert.Equal(t, model.DeltaPrepareWaiting, store.prepares[prep.ID].Status)
		assert.NotContains(t, resumeEventReasons(p), domain.EventReasonFleetRolloutStarted)
	})

	t.Run("When no waiting prepare is joined it should not emit", func(t *testing.T) {
		store := newFakePrepareStore()
		store.generations[keyA] = &model.DeltaGeneration{Status: model.DeltaGenerationSucceeded}
		p := newTestPreparer(t, store, eligibleFleetResolver(fleetWithTV("fleet-1", "tv-1")), &statusSpy{}, &resumeSpy{}, nil)

		require.NoError(t, p.completeWaitingIfTerminal(ctx, keyA))
		assert.NotContains(t, resumeEventReasons(p), domain.EventReasonFleetRolloutStarted)
	})

	t.Run("When the prepare was superceded it should not emit", func(t *testing.T) {
		store := newFakePrepareStore()
		prep := store.seedWaiting(orgId, domain.FleetKind, "fleet-1", lo.ToPtr("tv-1"), nil, time.Now())
		require.NoError(t, store.InsertPrepareGenerations(ctx, prep.ID, []deltastore.GenerationKey{keyA}))
		store.generations[keyA] = &model.DeltaGeneration{Status: model.DeltaGenerationSucceeded}
		require.NoError(t, store.CASPrepareStatus(ctx, prep.ID, model.DeltaPrepareFailed))
		p := newTestPreparer(t, store, eligibleFleetResolver(fleetWithTV("fleet-1", "tv-1")), &statusSpy{}, &resumeSpy{}, nil)

		require.NoError(t, p.completeWaitingIfTerminal(ctx, keyA))
		assert.NotContains(t, resumeEventReasons(p), domain.EventReasonFleetRolloutStarted)
	})
}

func TestPrepare_InsertAndAck(t *testing.T) {
	orgId := uuid.New()
	ctx := context.Background()
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)

	t.Run("When a fleet Prepare has eligible devices it should insert waiting, enqueue GenerateDelta, and set DeltaPreparing", func(t *testing.T) {
		store := newFakePrepareStore()
		status := &statusSpy{}
		resume := &resumeSpy{}
		emit := &emitSpy{}
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
		var payload generateDeltaPayload
		require.NoError(t, json.Unmarshal([]byte(emit.events[0].Message), &payload))
		assert.Equal(t, prepareTestRepo, payload.ImageRepository)
		assert.Equal(t, prepareTestSrc, payload.SourceDigest)
		assert.Equal(t, prepareTestTgt, payload.TargetDigest)
		assert.NotContains(t, resumeEventReasons(p), domain.EventReasonFleetRolloutStarted)
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
		p.MaxWait = func(*domain.Fleet) *time.Duration { return nil }

		err := p.Prepare(ctx, fleetPrepareEvent(orgId, "fleet-1", "tv-1"))
		require.NoError(t, err)
		assert.Nil(t, firstPrepare(store).Deadline)
		assert.Equal(t, model.DeltaPrepareWaiting, firstPrepare(store).Status)
		assert.NotContains(t, resumeEventReasons(p), domain.EventReasonFleetRolloutStarted)
	})

	t.Run("When maxWait is set it should set deadline to CreatedAt plus wait", func(t *testing.T) {
		store := newFakePrepareStore()
		wait := 5 * time.Minute
		p := newTestPreparer(t, store, eligibleFleetResolver(fleetWithTV("fleet-1", "tv-1"), deviceWithOS("d1", true, prepareTestSrc)), &statusSpy{}, &resumeSpy{}, &emitSpy{})
		p.Now = func() time.Time { return now }
		p.MaxWait = func(*domain.Fleet) *time.Duration { return &wait }

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
		p.MaxWait = func(*domain.Fleet) *time.Duration { return &zero }

		err := p.Prepare(ctx, fleetPrepareEvent(orgId, "fleet-1", "tv-1"))
		require.NoError(t, err)
		require.Len(t, emit.events, 1)
		assert.Equal(t, model.DeltaPrepareComplete, firstPrepare(store).Status)
		assert.Equal(t, now, *firstPrepare(store).Deadline)
		assert.Contains(t, resumeEventReasons(p), domain.EventReasonFleetRolloutStarted)
		assert.Empty(t, status.sets)
		require.Len(t, status.clears, 1)
	})

	t.Run("When the fleet sets maxWaitForDelta it should use the fleet duration", func(t *testing.T) {
		store := newFakePrepareStore()
		fleet := fleetWithTV("fleet-1", "tv-1")
		fleetWait := domain.Duration("10m")
		fleet.Spec.RolloutPolicy = &domain.RolloutPolicy{MaxWaitForDelta: &fleetWait}
		deploy := 30 * time.Minute
		p := newTestPreparer(t, store, eligibleFleetResolver(fleet, deviceWithOS("d1", true, prepareTestSrc)), &statusSpy{}, &resumeSpy{}, &emitSpy{})
		p.Now = func() time.Time { return now }
		p.MaxWait = func(f *domain.Fleet) *time.Duration {
			d, err := maxWaitFromFleet(f, &deploy)
			require.NoError(t, err)
			return d
		}

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
		assert.NotContains(t, resumeEventReasons(p), domain.EventReasonFleetRolloutStarted)
		assert.Empty(t, emit.events)
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

	t.Run("When a waiting prepare exists with a different TV it should CAS-fail the old prepare without Resume and insert a new one", func(t *testing.T) {
		store := newFakePrepareStore()
		old := store.seedWaiting(orgId, domain.FleetKind, "fleet-1", lo.ToPtr("tv-10"), nil, now.Add(-time.Hour))
		status := &statusSpy{}
		resume := &resumeSpy{}
		emit := &emitSpy{}
		p := newTestPreparer(t, store, eligibleFleetResolver(fleetWithTV("fleet-1", "tv-11"), deviceWithOS("d1", true, prepareTestSrc)), status, resume, emit)
		p.Now = func() time.Time { return now }

		err := p.Prepare(ctx, fleetPrepareEvent(orgId, "fleet-1", "tv-10"))
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
		assert.NotContains(t, resumeEventReasons(p), domain.EventReasonFleetRolloutStarted)
		require.Len(t, emit.events, 1)
		assert.Equal(t, domain.EventReasonGenerateDelta, emit.events[0].Reason)
		assert.NotEqual(t, domain.EventReasonFleetRolloutStarted, emit.events[0].Reason)
		assert.NotEqual(t, domain.EventReasonDeltaGenerationCompleted, emit.events[0].Reason)
	})

	t.Run("When supercede CAS loses it should not clear preparing status", func(t *testing.T) {
		store := newFakePrepareStore()
		store.seedWaiting(orgId, domain.FleetKind, "fleet-1", lo.ToPtr("tv-10"), nil, now.Add(-time.Hour))
		store.casErr = flterrors.ErrNoRowsUpdated
		status := &statusSpy{}
		p := newTestPreparer(t, store, eligibleFleetResolver(fleetWithTV("fleet-1", "tv-11"), deviceWithOS("d1", true, prepareTestSrc)), status, &resumeSpy{}, &emitSpy{})
		p.Now = func() time.Time { return now }

		err := p.Prepare(ctx, fleetPrepareEvent(orgId, "fleet-1", "tv-10"))
		require.NoError(t, err)
		assert.Empty(t, status.clears)
	})

	t.Run("When InsertPrepare returns ErrDuplicateName it should ignore the event", func(t *testing.T) {
		store := newFakePrepareStore()
		store.insertErr = flterrors.ErrDuplicateName
		resume := &resumeSpy{}
		emit := &emitSpy{}
		p := newTestPreparer(t, store, eligibleFleetResolver(fleetWithTV("fleet-1", "tv-1"), deviceWithOS("d1", true, prepareTestSrc)), &statusSpy{}, resume, emit)

		err := p.Prepare(ctx, fleetPrepareEvent(orgId, "fleet-1", "tv-1"))
		require.NoError(t, err)
		assert.NotContains(t, resumeEventReasons(p), domain.EventReasonFleetRolloutStarted)
		assert.Empty(t, emit.events)
		assert.Empty(t, store.prepares)
	})
}

func TestPrepare_TerminalAndDevice(t *testing.T) {
	orgId := uuid.New()
	ctx := context.Background()

	t.Run("When all generations are already terminal it should skip InsertGenerations, complete, and Resume", func(t *testing.T) {
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
		assert.Equal(t, 0, store.insertGensN)
		assert.Empty(t, emit.events)
		assert.Contains(t, resumeEventReasons(p), domain.EventReasonFleetRolloutStarted)
		assert.Equal(t, model.DeltaPrepareComplete, firstPrepare(store).Status)
		assert.Len(t, store.joins, 1)
		assert.Empty(t, status.sets)
		require.Len(t, status.clears, 1)
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
		assert.Equal(t, 0, store.insertGensN)
		assert.Empty(t, emit.events)
		assert.Equal(t, model.DeltaGenerationFailed, store.generations[failedKey].Status)
		assert.Equal(t, model.DeltaPrepareComplete, firstPrepare(store).Status)
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
		r.Expand = func(_ tasks.RenderedSpec, cands []DeltaCandidate) []DeltaCandidate {
			return append(cands, DeltaCandidate{ImageRepository: "quay.io/apps/web", CurrentDigest: "sha256:ccc", NewDigest: "sha256:ddd"})
		}
		p := newTestPreparer(t, store, r, status, &resumeSpy{}, emit)

		err := p.Prepare(ctx, fleetPrepareEvent(orgId, "fleet-1", "tv-1"))
		require.NoError(t, err)
		assert.Equal(t, 1, store.insertGensN)
		require.Len(t, emit.events, 1)
		var payload generateDeltaPayload
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
		r.Expand = func(_ tasks.RenderedSpec, cands []DeltaCandidate) []DeltaCandidate {
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

	t.Run("When a device Prepare runs it should persist Device kind with live resourceVersion", func(t *testing.T) {
		store := newFakePrepareStore()
		status := &statusSpy{}
		device := deviceWithOS("d1", true, prepareTestSrc)
		device.Metadata.Generation = lo.ToPtr(int64(7))
		p := newTestPreparer(t, store, eligibleDeviceResolver(device), status, &resumeSpy{}, &emitSpy{})

		err := p.Prepare(ctx, devicePrepareEvent(orgId, "d1"))
		require.NoError(t, err)
		prep := firstPrepare(store)
		assert.Equal(t, domain.DeviceKind, prep.Kind)
		assert.Equal(t, "d1", prep.Name)
		require.NotNil(t, prep.SpecResourceVersion)
		assert.Equal(t, int64(7), *prep.SpecResourceVersion)
		assert.Nil(t, prep.TemplateVersion)
		require.Len(t, status.sets, 1)
		assert.Equal(t, domain.DeviceKind, status.sets[0].kind)
	})

	t.Run("When a waiting device prepare has the same generation it should keep the original row", func(t *testing.T) {
		store := newFakePrepareStore()
		rv := int64(5)
		existing := store.seedWaiting(orgId, domain.DeviceKind, "d1", nil, &rv, time.Now())
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
		device.Metadata.Generation = lo.ToPtr(int64(5))
		device.Metadata.ResourceVersion = lo.ToPtr("99")
		resume := &resumeSpy{}
		emit := &emitSpy{}
		p := newTestPreparer(t, store, eligibleDeviceResolver(device), &statusSpy{}, resume, emit)

		err := p.Prepare(ctx, devicePrepareEvent(orgId, "d1"))
		require.NoError(t, err)
		assert.Len(t, store.prepares, 1)
		assert.Equal(t, existing.ID, firstPrepare(store).ID)
		assert.NotContains(t, resumeEventReasons(p), domain.EventReasonFleetRolloutStarted)
		assert.Empty(t, emit.events)
	})

	t.Run("When device generation changes it should CAS-fail the old prepare and insert a new one", func(t *testing.T) {
		store := newFakePrepareStore()
		rv := int64(5)
		old := store.seedWaiting(orgId, domain.DeviceKind, "d1", nil, &rv, time.Now())
		device := deviceWithOS("d1", true, prepareTestSrc)
		device.Metadata.Generation = lo.ToPtr(int64(6))
		resume := &resumeSpy{}
		emit := &emitSpy{}
		p := newTestPreparer(t, store, eligibleDeviceResolver(device), &statusSpy{}, resume, emit)

		err := p.Prepare(ctx, devicePrepareEvent(orgId, "d1"))
		require.NoError(t, err)
		assert.Equal(t, model.DeltaPrepareFailed, store.prepares[old.ID].Status)
		assert.Len(t, store.prepares, 2)
		assert.NotContains(t, resumeEventReasons(p), domain.EventReasonFleetRolloutStarted)
		require.Len(t, emit.events, 1)
		assert.Equal(t, domain.EventReasonGenerateDelta, emit.events[0].Reason)
	})

	t.Run("When a fleet annotation is missing it should fall back to event details TemplateVersion", func(t *testing.T) {
		store := newFakePrepareStore()
		fleet := &domain.Fleet{Metadata: domain.ObjectMeta{Name: lo.ToPtr("fleet-1")}, Spec: domain.FleetSpec{}}
		p := newTestPreparer(t, store, eligibleFleetResolver(fleet, deviceWithOS("d1", true, prepareTestSrc)), &statusSpy{}, &resumeSpy{}, &emitSpy{})

		err := p.Prepare(ctx, fleetPrepareEvent(orgId, "fleet-1", "tv-from-event"))
		require.NoError(t, err)
		assert.Equal(t, "tv-from-event", lo.FromPtr(firstPrepare(store).TemplateVersion))
	})

	t.Run("When a standalone device Prepare runs it should use deployment wait and timeout", func(t *testing.T) {
		store := newFakePrepareStore()
		device := deviceWithOS("d1", true, prepareTestSrc)
		device.Metadata.Generation = lo.ToPtr(int64(3))
		var maxWaitFleet *domain.Fleet
		var jobTimeoutFleet *domain.Fleet
		deployWait := 15 * time.Minute
		p := newTestPreparer(t, store, eligibleDeviceResolver(device), &statusSpy{}, &resumeSpy{}, &emitSpy{})
		p.MaxWait = func(f *domain.Fleet) *time.Duration {
			maxWaitFleet = f
			return &deployWait
		}
		p.JobTimeout = func(f *domain.Fleet) time.Duration {
			jobTimeoutFleet = f
			return 45 * time.Minute
		}

		err := p.Prepare(ctx, devicePrepareEvent(orgId, "d1"))
		require.NoError(t, err)
		assert.Nil(t, maxWaitFleet)
		assert.Nil(t, jobTimeoutFleet)
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
		got, err := maxWaitFromFleet(&domain.Fleet{Spec: domain.FleetSpec{RolloutPolicy: &domain.RolloutPolicy{MaxWaitForDelta: &d}}}, &deploy)
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
		got, err := jobTimeoutFromFleet(&domain.Fleet{Spec: domain.FleetSpec{RolloutPolicy: &domain.RolloutPolicy{DeltaGenerationTimeout: &d}}}, deploy)
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

func (f *fakePrepareStore) seedWaiting(orgID uuid.UUID, kind, name string, tv *string, rv *int64, created time.Time) *model.DeltaPrepare {
	prep := &model.DeltaPrepare{
		ID:                  uuid.New(),
		OrgID:               orgID,
		Kind:                kind,
		Name:                name,
		TemplateVersion:     tv,
		SpecResourceVersion: rv,
		CreatedAt:           created,
		Status:              model.DeltaPrepareWaiting,
	}
	f.prepares[prep.ID] = prep
	f.waiting[f.identityKey(orgID, kind, name)] = prep.ID
	return prep
}

func (f *fakePrepareStore) GetWaitingPrepare(_ context.Context, orgID uuid.UUID, kind, name string) (*model.DeltaPrepare, error) {
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

func (f *fakePrepareStore) InsertPrepare(_ context.Context, prep *model.DeltaPrepare) error {
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

func (f *fakePrepareStore) CASPrepareStatus(_ context.Context, id uuid.UUID, to string) error {
	if f.casErr != nil {
		return f.casErr
	}
	prep, ok := f.prepares[id]
	if !ok || prep.Status != model.DeltaPrepareWaiting {
		return flterrors.ErrNoRowsUpdated
	}
	prep.Status = to
	if to != model.DeltaPrepareWaiting {
		delete(f.waiting, f.identityKey(prep.OrgID, prep.Kind, prep.Name))
	}
	return nil
}

func (f *fakePrepareStore) InsertGenerations(_ context.Context, gens []*model.DeltaGeneration) ([]deltastore.GenerationKey, error) {
	f.insertGensN++
	var changed []deltastore.GenerationKey
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
			changed = append(changed, key)
			continue
		}
		if existing.Status != model.DeltaGenerationFailed {
			continue
		}
		existing.Status = model.DeltaGenerationPending
		changed = append(changed, key)
	}
	return changed, nil
}

func (f *fakePrepareStore) InsertPrepareGenerations(_ context.Context, prepareID uuid.UUID, keys []deltastore.GenerationKey) error {
	for _, key := range keys {
		f.joins = append(f.joins, model.DeltaPrepareGeneration{
			PrepareID:       prepareID,
			OrgID:           key.OrgID,
			ImageRepository: key.ImageRepository,
			SourceDigest:    key.SourceDigest,
			TargetDigest:    key.TargetDigest,
		})
	}
	return nil
}

func (f *fakePrepareStore) GetGeneration(_ context.Context, key deltastore.GenerationKey) (*model.DeltaGeneration, error) {
	gen, ok := f.generations[key]
	if !ok {
		return nil, flterrors.ErrResourceNotFound
	}
	cp := *gen
	return &cp, nil
}

func (f *fakePrepareStore) ListWaitingPreparesByGeneration(_ context.Context, key deltastore.GenerationKey) ([]model.DeltaPrepare, error) {
	var out []model.DeltaPrepare
	for _, join := range f.joins {
		if join.OrgID != key.OrgID || join.ImageRepository != key.ImageRepository || join.SourceDigest != key.SourceDigest || join.TargetDigest != key.TargetDigest {
			continue
		}
		prep, ok := f.prepares[join.PrepareID]
		if !ok || prep.Status != model.DeltaPrepareWaiting {
			continue
		}
		cp := *prep
		out = append(out, cp)
	}
	return out, nil
}

func (f *fakePrepareStore) ListPrepareGenerationKeys(_ context.Context, prepareID uuid.UUID) ([]deltastore.GenerationKey, error) {
	var keys []deltastore.GenerationKey
	for _, join := range f.joins {
		if join.PrepareID != prepareID {
			continue
		}
		keys = append(keys, deltastore.GenerationKey{
			OrgID:           join.OrgID,
			ImageRepository: join.ImageRepository,
			SourceDigest:    join.SourceDigest,
			TargetDigest:    join.TargetDigest,
		})
	}
	return keys, nil
}

type statusSpy struct {
	sets   []statusCall
	clears []statusCall
}

type statusCall struct {
	kind, name       string
	completed, total int
}

func (s *statusSpy) Set(_ context.Context, _ uuid.UUID, kind, name string, completed, total int) error {
	s.sets = append(s.sets, statusCall{kind: kind, name: name, completed: completed, total: total})
	return nil
}

func (s *statusSpy) Clear(_ context.Context, _ uuid.UUID, kind, name string) error {
	s.clears = append(s.clears, statusCall{kind: kind, name: name})
	return nil
}

type emitSpy struct {
	events []*domain.Event
	err    error
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
	return nil
}

type resumeSpy struct {
	n int
}

func (r *resumeSpy) resume(_ context.Context, _ worker_client.EventWithOrgId) error {
	r.n++
	return nil
}

func newTestPreparer(t *testing.T, store *fakePrepareStore, resolver *Resolver, status PreparingStatus, resume *resumeSpy, emit *emitSpy) *Preparer {
	t.Helper()
	ctrl := gomock.NewController(t)
	fleetSvc := fleetservice.NewMockService(ctrl)
	deviceSvc := deviceservice.NewMockService(ctrl)
	if resolver != nil && resolver.Fleet != nil {
		fleetSvc.EXPECT().GetFleet(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
			func(ctx context.Context, orgId uuid.UUID, name string, _ domain.GetFleetParams) (*domain.Fleet, domain.Status) {
				fleet, err := resolver.Fleet(ctx, orgId, name)
				if err != nil {
					return nil, domain.StatusInternalServerError(err.Error())
				}
				return fleet, domain.StatusOK()
			},
		).AnyTimes()
	}
	if resolver != nil && resolver.Device != nil {
		deviceSvc.EXPECT().GetDevice(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
			func(ctx context.Context, orgId uuid.UUID, name string) (*domain.Device, domain.Status) {
				device, err := resolver.Device(ctx, orgId, name)
				if err != nil {
					return nil, domain.StatusInternalServerError(err.Error())
				}
				return device, domain.StatusOK()
			},
		).AnyTimes()
	}
	fleetSvc.EXPECT().UpdateFleetAnnotations(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(domain.StatusOK()).AnyTimes()
	deviceSvc.EXPECT().SetOutOfDate(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	rec := &eventRecorder{}
	p := &Preparer{
		Resolver: resolver,
		Store:    store,
		Now:      time.Now,
		MaxWait:  func(*domain.Fleet) *time.Duration { return nil },
		JobTimeout: func(*domain.Fleet) time.Duration {
			return 30 * time.Minute
		},
		Status:    status,
		Resume:    resume.resume,
		Events:    rec,
		FleetSvc:  fleetSvc,
		DeviceSvc: deviceSvc,
	}
	if emit != nil {
		p.Emit = emit.emit
	}
	return p
}

func newCompleteNowPreparer(t *testing.T, store *fakePrepareStore, resolver *Resolver, status PreparingStatus, onAnnotate func(map[string]string), onOutOfDate func(string)) *Preparer {
	t.Helper()
	p := newTestPreparer(t, store, resolver, status, &resumeSpy{}, nil)
	ctrl := gomock.NewController(t)
	fleetSvc := fleetservice.NewMockService(ctrl)
	deviceSvc := deviceservice.NewMockService(ctrl)
	fleetSvc.EXPECT().GetFleet(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
		func(ctx context.Context, orgId uuid.UUID, name string, _ domain.GetFleetParams) (*domain.Fleet, domain.Status) {
			fleet, err := resolver.Fleet(ctx, orgId, name)
			if err != nil {
				return nil, domain.StatusInternalServerError(err.Error())
			}
			return fleet, domain.StatusOK()
		},
	).AnyTimes()
	fleetSvc.EXPECT().UpdateFleetAnnotations(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, _ uuid.UUID, _ string, annotations map[string]string, _ []string) domain.Status {
			if onAnnotate != nil {
				onAnnotate(annotations)
			}
			return domain.StatusOK()
		},
	)
	deviceSvc.EXPECT().SetOutOfDate(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, _ uuid.UUID, owner string) error {
			if onOutOfDate != nil {
				onOutOfDate(owner)
			}
			return nil
		},
	)
	p.FleetSvc = fleetSvc
	p.DeviceSvc = deviceSvc
	return p
}

func recordedEvents(p *Preparer) []*domain.Event {
	rec, ok := p.Events.(*eventRecorder)
	if !ok {
		return nil
	}
	return rec.events
}

type eventRecorder struct {
	events []*domain.Event
}

func (e *eventRecorder) CreateEvent(_ context.Context, _ uuid.UUID, event *domain.Event) {
	if event == nil {
		return
	}
	cp := *event
	e.events = append(e.events, &cp)
}

func (e *eventRecorder) HandleGenericResourceDeletedEvents(context.Context, domain.ResourceKind, uuid.UUID, string, interface{}, interface{}, bool, error) {
}

func resumeEventReasons(p *Preparer) []domain.EventReason {
	rec, ok := p.Events.(*eventRecorder)
	if !ok {
		return nil
	}
	out := make([]domain.EventReason, 0, len(rec.events))
	for _, ev := range rec.events {
		out = append(out, ev.Reason)
	}
	return out
}

func firstPrepare(store *fakePrepareStore) *model.DeltaPrepare {
	for _, prep := range store.prepares {
		return prep
	}
	return nil
}

func fleetWithTV(name, tv string) *domain.Fleet {
	return &domain.Fleet{
		Metadata: domain.ObjectMeta{
			Name:        lo.ToPtr(name),
			Annotations: &map[string]string{domain.FleetAnnotationTemplateVersion: tv},
		},
		Spec: domain.FleetSpec{},
	}
}

func skipGenerateDeltaResolver() *Resolver {
	return &Resolver{
		Fleet: func(_ context.Context, _ uuid.UUID, _ string) (*domain.Fleet, error) {
			f := fleetWithTV("fleet-1", "tv-1")
			f.Spec.RolloutPolicy = &domain.RolloutPolicy{GenerateDelta: lo.ToPtr(false)}
			return f, nil
		},
		WriteTarget: func(_ context.Context, _ uuid.UUID) (*domain.OciRepoSpec, error) {
			return &domain.OciRepoSpec{Registry: "quay.io"}, nil
		},
	}
}

func eligibleFleetResolver(fleet *domain.Fleet, devices ...*domain.Device) *Resolver {
	return &Resolver{
		Fleet: func(_ context.Context, _ uuid.UUID, _ string) (*domain.Fleet, error) {
			return fleet, nil
		},
		TemplateVersion: func(_ context.Context, _ uuid.UUID, _, name string) (*domain.TemplateVersion, error) {
			return &domain.TemplateVersion{Metadata: domain.ObjectMeta{Name: lo.ToPtr(name)}}, nil
		},
		Devices: func(_ context.Context, _ uuid.UUID, _ string) ([]*domain.Device, error) {
			return devices, nil
		},
		WriteTarget: func(_ context.Context, _ uuid.UUID) (*domain.OciRepoSpec, error) {
			return &domain.OciRepoSpec{Registry: "quay.io"}, nil
		},
		DesiredSpec: func(_ *domain.Device, _ *domain.TemplateVersion) (*domain.DeviceSpec, error) {
			return &domain.DeviceSpec{Os: &domain.DeviceOsSpec{Image: prepareTestImage}}, nil
		},
		Render: func(_ context.Context, spec *domain.DeviceSpec) (tasks.RenderedSpec, error) {
			return tasks.RenderedSpec{OsImage: spec.Os.Image}, nil
		},
		Inspect: func(_ context.Context, _ string) (string, error) {
			return prepareTestTgt, nil
		},
	}
}

func eligibleDeviceResolver(device *domain.Device) *Resolver {
	return &Resolver{
		Device: func(_ context.Context, _ uuid.UUID, _ string) (*domain.Device, error) {
			return device, nil
		},
		WriteTarget: func(_ context.Context, _ uuid.UUID) (*domain.OciRepoSpec, error) {
			return &domain.OciRepoSpec{Registry: "quay.io"}, nil
		},
		DesiredSpec: func(_ *domain.Device, _ *domain.TemplateVersion) (*domain.DeviceSpec, error) {
			return &domain.DeviceSpec{Os: &domain.DeviceOsSpec{Image: prepareTestImage}}, nil
		},
		Render: func(_ context.Context, spec *domain.DeviceSpec) (tasks.RenderedSpec, error) {
			return tasks.RenderedSpec{OsImage: spec.Os.Image}, nil
		},
		Inspect: func(_ context.Context, _ string) (string, error) {
			return prepareTestTgt, nil
		},
	}
}
