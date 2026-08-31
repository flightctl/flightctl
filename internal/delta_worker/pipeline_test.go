package delta_worker

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	deltastore "github.com/flightctl/flightctl/internal/store/delta"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/worker_client"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

type fakeGenerationStore struct {
	rejected       []*model.DeltaGeneration
	inserted       []*model.DeltaGeneration
	claimed        int
	claimErr       error
	cas            []deltastore.GenerationCAS
	casErr         error
	casFailN       int
	waiting        []model.DeltaPrepare
	claimedRV      int64
	listWaitingN   int
	countCompleted int
	countTotal     int
	countErr       error
}

func (f *fakeGenerationStore) InsertRejectedGeneration(_ context.Context, gen *model.DeltaGeneration) error {
	f.rejected = append(f.rejected, gen)
	return nil
}

func (f *fakeGenerationStore) InsertGenerations(_ context.Context, gens []*model.DeltaGeneration) ([]deltastore.GenerationKey, error) {
	f.inserted = append(f.inserted, gens...)
	return nil, nil
}

func (f *fakeGenerationStore) ClaimGeneration(_ context.Context, _ deltastore.GenerationKey) (*model.DeltaGeneration, error) {
	if f.claimErr != nil {
		return nil, f.claimErr
	}
	f.claimed++
	return &model.DeltaGeneration{Status: model.DeltaGenerationInProgress, ResourceVersion: f.claimedRV}, nil
}

func (f *fakeGenerationStore) CASGeneration(ctx context.Context, _ deltastore.GenerationKey, _ int64, update deltastore.GenerationCAS) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if f.casErr != nil {
		return f.casErr
	}
	if f.casFailN > 0 {
		f.casFailN--
		return errors.New("persist failed")
	}
	f.cas = append(f.cas, update)
	return nil
}

func (f *fakeGenerationStore) ListWaitingPreparesByGeneration(_ context.Context, _ deltastore.GenerationKey) ([]model.DeltaPrepare, error) {
	f.listWaitingN++
	return f.waiting, nil
}

func (f *fakeGenerationStore) CountPreparePairs(_ context.Context, _ uuid.UUID) (int, int, error) {
	return f.countCompleted, f.countTotal, f.countErr
}

func generateEvent(org uuid.UUID, repo, src, tgt string) worker_client.EventWithOrgId {
	return generateEventWithTimeout(org, repo, src, tgt, "")
}

func generateEventWithTimeout(org uuid.UUID, repo, src, tgt, timeout string) worker_client.EventWithOrgId {
	payload, _ := json.Marshal(generateDeltaPayload{
		ImageRepository: repo,
		SourceDigest:    src,
		TargetDigest:    tgt,
		Timeout:         timeout,
	})
	return worker_client.EventWithOrgId{
		OrgId: org,
		Event: domain.Event{Reason: domain.EventReasonGenerateDelta, Message: string(payload)},
	}
}

func TestPipelineProcess(t *testing.T) {
	org := uuid.New()
	log := logrus.New()
	log.SetLevel(logrus.ErrorLevel)
	src := "sha256:src"
	tgt := "sha256:tgt"
	repo := "quay.io/team-a/os"

	t.Run("When PrepareDeltas it should call Prepare and not generate", func(t *testing.T) {
		req := require.New(t)
		store := &fakeGenerationStore{}
		var prepared worker_client.EventWithOrgId
		p := &pipeline{
			store:   store,
			timeout: time.Minute,
			check: func(context.Context, uuid.UUID, string, string, string) (existenceResult, error) {
				t.Fatal("existence check must not run")
				return existenceResult{}, nil
			},
			generate: func(context.Context, uuid.UUID, string, string, string) (string, int64, error) {
				t.Fatal("generate must not run")
				return "", 0, nil
			},
			prepare: func(_ context.Context, ev worker_client.EventWithOrgId) error {
				prepared = ev
				return nil
			},
		}
		ev := worker_client.EventWithOrgId{
			OrgId: org,
			Event: domain.Event{Reason: domain.EventReasonPrepareDeltas},
		}
		err := p.process(context.Background(), ev, log)
		req.NoError(err)
		req.Equal(domain.EventReasonPrepareDeltas, prepared.Event.Reason)
		req.Empty(store.inserted)
		req.Equal(0, store.claimed)
	})

	t.Run("When GenerateDelta payload timeout is set it should override pipeline timeout", func(t *testing.T) {
		req := require.New(t)
		store := &fakeGenerationStore{}
		p := &pipeline{
			store:   store,
			timeout: time.Hour,
			check: func(context.Context, uuid.UUID, string, string, string) (existenceResult, error) {
				return existenceResult{Status: existenceNotFound}, nil
			},
			generate: func(ctx context.Context, _ uuid.UUID, _, _, _ string) (string, int64, error) {
				<-ctx.Done()
				return "", 0, ctx.Err()
			},
		}
		req.NoError(p.process(context.Background(), generateEventWithTimeout(org, repo, src, tgt, time.Nanosecond.String()), log))
		req.Len(store.cas, 1)
		req.Equal(model.DeltaGenerationFailed, store.cas[0].Status)
	})

	t.Run("When existence is found it should insert rejected and not generate", func(t *testing.T) {
		req := require.New(t)
		store := &fakeGenerationStore{waiting: []model.DeltaPrepare{{Name: "fleet-a"}}}
		var generated bool
		p := &pipeline{
			store:   store,
			timeout: time.Minute,
			check: func(context.Context, uuid.UUID, string, string, string) (existenceResult, error) {
				return existenceResult{Status: existenceFound, SizeBytes: 77}, nil
			},
			generate: func(context.Context, uuid.UUID, string, string, string) (string, int64, error) {
				generated = true
				return "", 0, nil
			},
		}
		req.NoError(p.process(context.Background(), generateEvent(org, repo, src, tgt), log))
		req.False(generated)
		req.Len(store.rejected, 1)
		req.Equal(int64(77), *store.rejected[0].SizeBytes)
		req.Equal(1, store.listWaitingN)
		req.Empty(store.inserted)
	})

	t.Run("When existence is inconclusive it should skip insert and generate", func(t *testing.T) {
		req := require.New(t)
		store := &fakeGenerationStore{}
		p := &pipeline{
			store:   store,
			timeout: time.Minute,
			check: func(context.Context, uuid.UUID, string, string, string) (existenceResult, error) {
				return existenceResult{Status: existenceInconclusive}, nil
			},
			generate: func(context.Context, uuid.UUID, string, string, string) (string, int64, error) {
				t.Fatal("generate must not run")
				return "", 0, nil
			},
		}
		req.NoError(p.process(context.Background(), generateEvent(org, repo, src, tgt), log))
		req.Empty(store.inserted)
		req.Empty(store.rejected)
	})

	t.Run("When miss it should claim generate and CAS succeeded", func(t *testing.T) {
		req := require.New(t)
		store := &fakeGenerationStore{claimedRV: 4}
		p := &pipeline{
			store:   store,
			timeout: time.Minute,
			check: func(context.Context, uuid.UUID, string, string, string) (existenceResult, error) {
				return existenceResult{Status: existenceNotFound}, nil
			},
			generate: func(_ context.Context, _ uuid.UUID, sourceRef, targetRef, pushPath string) (string, int64, error) {
				req.Equal(repo+"@"+src, sourceRef)
				req.Equal(repo+"@"+tgt, targetRef)
				req.Equal("write.example/os", pushPath)
				return "write.example/os@sha256:delta", 12, nil
			},
			pushPath: func(context.Context, uuid.UUID, string) (string, error) { return "write.example/os", nil },
		}
		req.NoError(p.process(context.Background(), generateEvent(org, repo, src, tgt), log))
		req.Len(store.inserted, 1)
		req.Equal(1, store.claimed)
		req.Len(store.cas, 1)
		req.Equal(model.DeltaGenerationSucceeded, store.cas[0].Status)
		req.Equal("write.example/os@sha256:delta", *store.cas[0].DeltaRef)
		req.Equal(int64(12), *store.cas[0].SizeBytes)
		req.Equal(1, store.listWaitingN)
	})

	t.Run("When generate fails it should CAS failed and resume", func(t *testing.T) {
		req := require.New(t)
		store := &fakeGenerationStore{}
		p := &pipeline{
			store:   store,
			timeout: time.Minute,
			check: func(context.Context, uuid.UUID, string, string, string) (existenceResult, error) {
				return existenceResult{Status: existenceNotFound}, nil
			},
			generate: func(context.Context, uuid.UUID, string, string, string) (string, int64, error) {
				return "", 0, errors.New("oci-delta exploded")
			},
		}
		req.NoError(p.process(context.Background(), generateEvent(org, repo, src, tgt), log))
		req.Len(store.cas, 1)
		req.Equal(model.DeltaGenerationFailed, store.cas[0].Status)
		req.Equal(1, store.listWaitingN)
	})

	t.Run("When claim is in_progress it should not steal", func(t *testing.T) {
		req := require.New(t)
		store := &fakeGenerationStore{claimErr: flterrors.ErrNoRowsUpdated}
		p := &pipeline{
			store:   store,
			timeout: time.Minute,
			check: func(context.Context, uuid.UUID, string, string, string) (existenceResult, error) {
				return existenceResult{Status: existenceNotFound}, nil
			},
			generate: func(context.Context, uuid.UUID, string, string, string) (string, int64, error) {
				t.Fatal("generate must not run")
				return "", 0, nil
			},
		}
		req.NoError(p.process(context.Background(), generateEvent(org, repo, src, tgt), log))
		req.Empty(store.cas)
	})

	t.Run("When persist succeeded fails it should mark failed and resume", func(t *testing.T) {
		req := require.New(t)
		store := &fakeGenerationStore{casFailN: 1}
		p := &pipeline{
			store:   store,
			timeout: time.Minute,
			check: func(context.Context, uuid.UUID, string, string, string) (existenceResult, error) {
				return existenceResult{Status: existenceNotFound}, nil
			},
			generate: func(context.Context, uuid.UUID, string, string, string) (string, int64, error) {
				return "ref", 1, nil
			},
		}
		req.NoError(p.process(context.Background(), generateEvent(org, repo, src, tgt), log))
		req.Len(store.cas, 1)
		req.Equal(model.DeltaGenerationFailed, store.cas[0].Status)
		req.Equal(1, store.listWaitingN)
	})

	t.Run("When generate context times out it should CAS failed", func(t *testing.T) {
		req := require.New(t)
		store := &fakeGenerationStore{}
		p := &pipeline{
			store:   store,
			timeout: time.Nanosecond,
			check: func(context.Context, uuid.UUID, string, string, string) (existenceResult, error) {
				return existenceResult{Status: existenceNotFound}, nil
			},
			generate: func(ctx context.Context, _ uuid.UUID, _, _, _ string) (string, int64, error) {
				<-ctx.Done()
				return "", 0, ctx.Err()
			},
		}
		req.NoError(p.process(context.Background(), generateEvent(org, repo, src, tgt), log))
		req.Len(store.cas, 1)
		req.Equal(model.DeltaGenerationFailed, store.cas[0].Status)
		req.Equal(1, store.listWaitingN)
	})

	t.Run("When CAS is stale it should not overwrite", func(t *testing.T) {
		req := require.New(t)
		store := &fakeGenerationStore{casErr: flterrors.ErrNoRowsUpdated}
		p := &pipeline{
			store:   store,
			timeout: time.Minute,
			check: func(context.Context, uuid.UUID, string, string, string) (existenceResult, error) {
				return existenceResult{Status: existenceNotFound}, nil
			},
			generate: func(context.Context, uuid.UUID, string, string, string) (string, int64, error) {
				return "ref", 1, nil
			},
		}
		req.NoError(p.process(context.Background(), generateEvent(org, repo, src, tgt), log))
		req.Equal(0, store.listWaitingN)
	})

	t.Run("When persist is set it should emit phase changes not percent and write completed/total", func(t *testing.T) {
		req := require.New(t)
		prepID := uuid.New()
		store := &fakeGenerationStore{
			waiting: []model.DeltaPrepare{{
				ID:              prepID,
				OrgID:           org,
				Kind:            domain.FleetKind,
				Name:            "fleet-a",
				TemplateVersion: lo.ToPtr("tv-1"),
			}},
			countCompleted: 1,
			countTotal:     3,
		}
		status := &statusSpy{}
		persist := &emitSpy{}
		p := &pipeline{
			store:   store,
			timeout: time.Minute,
			check: func(context.Context, uuid.UUID, string, string, string) (existenceResult, error) {
				return existenceResult{Status: existenceNotFound}, nil
			},
			generate: func(ctx context.Context, _ uuid.UUID, _, _, _ string) (string, int64, error) {
				emitGenerationProgress(ctx, GenerationProgress{Phase: domain.DeltaGenerationPhasePullSource})
				emitGenerationProgress(ctx, GenerationProgress{Phase: domain.DeltaGenerationPhasePullSource, Percent: lo.ToPtr(int64(40))})
				emitGenerationProgress(ctx, GenerationProgress{Phase: domain.DeltaGenerationPhasePullTarget})
				return "ref", 1, nil
			},
			status:  status,
			persist: persist.persist,
		}
		req.NoError(p.process(context.Background(), generateEvent(org, repo, src, tgt), log))
		req.Len(persist.events, 4)
		req.Equal(domain.EventReasonDeltaGenerationProgress, persist.events[0].Reason)
		req.Equal(domain.EventTypeNormal, persist.events[0].Type)
		req.Equal("fleet-a", persist.events[0].InvolvedObject.Name)
		statuses := make([]domain.DeltaGenerationProgressDetailsGenerationStatus, 0, 4)
		phases := make([]string, 0, 4)
		for _, ev := range persist.events {
			d, err := ev.Details.AsDeltaGenerationProgressDetails()
			req.NoError(err)
			statuses = append(statuses, d.GenerationStatus)
			if d.Phase != nil {
				phases = append(phases, string(*d.Phase))
			}
		}
		req.Equal([]domain.DeltaGenerationProgressDetailsGenerationStatus{
			domain.DeltaGenerationProgressInProgress,
			domain.DeltaGenerationProgressInProgress,
			domain.DeltaGenerationProgressInProgress,
			domain.DeltaGenerationProgressSucceeded,
		}, statuses)
		req.Equal([]string{"checkingExisting", "pullSource", "pullTarget"}, phases)
		req.Equal(persist.events[3].Type, domain.EventTypeNormal)
		req.Len(status.sets, 1)
		req.Equal(statusCall{kind: domain.FleetKind, name: "fleet-a", completed: 1, total: 3}, status.sets[0])
	})

	t.Run("When generate fails it should emit a Warning progress event", func(t *testing.T) {
		req := require.New(t)
		store := &fakeGenerationStore{waiting: []model.DeltaPrepare{{
			OrgID: org,
			Kind:  domain.DeviceKind,
			Name:  "dev-1",
		}}}
		persist := &emitSpy{}
		p := &pipeline{
			store:   store,
			timeout: time.Minute,
			check: func(context.Context, uuid.UUID, string, string, string) (existenceResult, error) {
				return existenceResult{Status: existenceNotFound}, nil
			},
			generate: func(context.Context, uuid.UUID, string, string, string) (string, int64, error) {
				return "", 0, errors.New("oci-delta exploded")
			},
			persist: persist.persist,
		}
		req.NoError(p.process(context.Background(), generateEvent(org, repo, src, tgt), log))
		req.GreaterOrEqual(len(persist.events), 2)
		last := persist.events[len(persist.events)-1]
		req.Equal(domain.EventReasonDeltaGenerationProgress, last.Reason)
		req.Equal(domain.EventTypeWarning, last.Type)
		d, err := last.Details.AsDeltaGenerationProgressDetails()
		req.NoError(err)
		req.Equal(domain.DeltaGenerationProgressFailed, d.GenerationStatus)
		req.Nil(d.Phase)
	})
}
