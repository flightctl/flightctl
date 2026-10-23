package generate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	deltaconfig "github.com/flightctl/flightctl/internal/delta_worker/config"
	"github.com/flightctl/flightctl/internal/delta_worker/model"
	"github.com/flightctl/flightctl/internal/delta_worker/service/deltageneration"
	"github.com/flightctl/flightctl/internal/delta_worker/service/deltapreparegeneration"
	deltastore "github.com/flightctl/flightctl/internal/delta_worker/store/deltageneration"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	repositoryservice "github.com/flightctl/flightctl/internal/service/repository"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/internal/worker_client"
	"github.com/google/uuid"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"oras.land/oras-go/v2"
	ocistore "oras.land/oras-go/v2/content/oci"
)

type fakeGenerationService struct {
	createCalls int
	claimed     int
	claimErr    error
	updates     []*model.DeltaGeneration
	casErr      error
	casFailN    int
	claimedRV   int64
	generations map[deltastore.GenerationKey]*model.DeltaGeneration
}

func (f *fakeGenerationService) CreateDeltaGenerations(_ context.Context, gens []*model.DeltaGeneration) ([]model.DeltaGeneration, error) {
	f.createCalls++
	return nil, nil
}

func (f *fakeGenerationService) GetDeltaGeneration(_ context.Context, key deltastore.GenerationKey, _ ...deltastore.GenerationGetOption) (*model.DeltaGeneration, error) {
	generation := f.generations[key]
	if generation == nil {
		generation = &model.DeltaGeneration{
			OrgID:           key.OrgID,
			ImageRepository: key.ImageRepository,
			SourceDigest:    key.SourceDigest,
			TargetDigest:    key.TargetDigest,
			Status:          model.DeltaGenerationPending,
			ResourceVersion: f.claimedRV,
		}
		if f.generations == nil {
			f.generations = make(map[deltastore.GenerationKey]*model.DeltaGeneration)
		}
		f.generations[key] = generation
	}
	copy := *generation
	return &copy, nil
}

func (f *fakeGenerationService) ListDeltaGenerations(_ context.Context, keys []deltastore.GenerationKey) ([]model.DeltaGeneration, error) {
	result := make([]model.DeltaGeneration, 0, len(keys))
	for _, key := range keys {
		if generation := f.generations[key]; generation != nil {
			result = append(result, *generation)
		}
	}
	return result, nil
}

func (f *fakeGenerationService) UpdateDeltaGeneration(ctx context.Context, _ int64, generation *model.DeltaGeneration) (*model.DeltaGeneration, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if generation.Status == model.DeltaGenerationInProgress && f.claimed == 0 && f.claimErr != nil {
		return nil, f.claimErr
	}
	if generation.Status != model.DeltaGenerationInProgress && f.casErr != nil {
		return nil, f.casErr
	}
	if f.casFailN > 0 && f.claimed > 0 {
		f.casFailN--
		return nil, errors.New("persist failed")
	}
	key := generationKey(*generation)
	copy := *generation
	if copy.Status == model.DeltaGenerationInProgress {
		f.claimed++
	}
	copy.ResourceVersion++
	f.generations[key] = &copy
	snapshot := copy
	f.updates = append(f.updates, &snapshot)
	return &copy, nil
}

func generationKey(g model.DeltaGeneration) deltastore.GenerationKey {
	return deltastore.GenerationKey{OrgID: g.OrgID, ImageRepository: g.ImageRepository, SourceDigest: g.SourceDigest, TargetDigest: g.TargetDigest}
}

var _ deltageneration.Service = (*fakeGenerationService)(nil)

func generateEvent(org uuid.UUID, repo, src, tgt string) worker_client.EventWithOrgId {
	payload, _ := json.Marshal(GenerateDeltaPayload{
		ImageRepository: repo,
		SourceDigest:    src,
		TargetDigest:    tgt,
	})
	return worker_client.EventWithOrgId{
		OrgId: org,
		Event: domain.Event{Reason: domain.EventReasonGenerateDelta, Message: string(payload)},
	}
}

func emptyRepositoryService(t *testing.T) repositoryservice.Service {
	t.Helper()
	mock := repositoryservice.NewMockService(gomock.NewController(t))
	mock.EXPECT().ListRepositories(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, domain.Status{Code: http.StatusOK}).AnyTimes()
	return mock
}

func newTestHandler(t *testing.T, store *fakeGenerationService, cfg *deltaconfig.DeltaGenerationConfig, check func(context.Context, uuid.UUID, string, string, string) (*existingDelta, error), generate func(context.Context, uuid.UUID, string, string, string) (string, int64, error), emit EventEmitter) *Handler {
	t.Helper()
	var existenceCheck existenceChecker
	if check != nil {
		existenceCheck = func(ctx context.Context, orgID uuid.UUID, imageRepository, sourceDigest, targetDigest string, _ *domain.OciRepoSpec) (*existingDelta, error) {
			return check(ctx, orgID, imageRepository, sourceDigest, targetDigest)
		}
	}
	var deltaGenerator deltaGenerator
	if generate != nil {
		deltaGenerator = func(ctx context.Context, generation *model.DeltaGeneration, _ *domain.OciRepoSpec, sourceRef, targetRef, pushPath string) (string, int64, error) {
			return generate(ctx, generation.OrgID, sourceRef, targetRef, pushPath)
		}
	}
	progress := deltapreparegeneration.NewProgressHandler(nil, nil, nil)
	handler, err := newHandler(cfg, logrus.New(), emptyRepositoryService(t), store, existenceCheck, deltaGenerator, progress, emit, nil)
	require.NoError(t, err)
	return handler
}

func noOpEventEmitter(context.Context, uuid.UUID, *domain.Event) error {
	return nil
}

type recordingEventEmitter struct {
	events []*domain.Event
}

func (r *recordingEventEmitter) Emit(_ context.Context, _ uuid.UUID, event *domain.Event) error {
	r.events = append(r.events, event)
	return nil
}

func assertGenerationCompleteEvent(t *testing.T, orgID uuid.UUID, event *domain.Event, key deltastore.GenerationKey, status domain.DeltaGenerationProgressDetailsGenerationStatus) {
	t.Helper()
	req := require.New(t)
	req.Equal(domain.EventReasonDeltaGenerationComplete, event.Reason)
	parsedKey, parsedStatus, err := deltageneration.ParseGenerationCompleteEvent(orgID, event.Message)
	req.NoError(err)
	req.Equal(key, parsedKey)
	req.Equal(status, parsedStatus)
}

func TestHandleGenerateDelta(t *testing.T) {
	org := uuid.New()
	log := logrus.New()
	log.SetLevel(logrus.ErrorLevel)
	src := "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	tgt := "sha256:2222222222222222222222222222222222222222222222222222222222222222"
	repo := "quay.io/team-a/os"

	t.Run("When PrepareDeltas it should not generate", func(t *testing.T) {
		req := require.New(t)
		store := &fakeGenerationService{}
		c := newTestHandler(t, store, &deltaconfig.DeltaGenerationConfig{Timeout: util.Duration(time.Minute)}, func(context.Context, uuid.UUID, string, string, string) (*existingDelta, error) {
			t.Fatal("existence check must not run")
			return nil, nil
		}, nil, noOpEventEmitter)
		err := c.Handle(context.Background(), worker_client.EventWithOrgId{
			OrgId: org,
			Event: domain.Event{Reason: domain.EventReasonPrepareDeltas},
		}, log)
		req.NoError(err)
		req.Zero(store.createCalls)
	})

	t.Run("When existence is found it should update succeeded and not generate", func(t *testing.T) {
		req := require.New(t)
		store := &fakeGenerationService{}
		var generated bool
		c := newTestHandler(t, store, &deltaconfig.DeltaGenerationConfig{Timeout: util.Duration(time.Minute)}, func(context.Context, uuid.UUID, string, string, string) (*existingDelta, error) {
			return &existingDelta{Ref: "write.example/os@sha256:existing", SizeBytes: 77}, nil
		}, func(context.Context, uuid.UUID, string, string, string) (string, int64, error) {
			generated = true
			return "", 0, nil
		}, noOpEventEmitter)
		req.NoError(c.Handle(context.Background(), generateEvent(org, repo, src, tgt), log))
		req.False(generated)
		req.Len(store.updates, 2)
		req.Equal(model.DeltaGenerationSucceeded, store.updates[1].Status)
		req.Equal("write.example/os@sha256:existing", *store.updates[1].DeltaRef)
		req.Equal(int64(77), *store.updates[1].SizeBytes)
		req.Zero(store.createCalls)
	})

	t.Run("When generation completes it should emit a terminal generation event", func(t *testing.T) {
		req := require.New(t)
		store := &fakeGenerationService{}
		emitter := &recordingEventEmitter{}
		c := newTestHandler(t, store, &deltaconfig.DeltaGenerationConfig{Timeout: util.Duration(time.Minute)}, func(context.Context, uuid.UUID, string, string, string) (*existingDelta, error) {
			return &existingDelta{Ref: "write.example/os@sha256:existing", SizeBytes: 77}, nil
		}, nil, emitter.Emit)

		req.NoError(c.Handle(context.Background(), generateEvent(org, repo, src, tgt), log))
		req.Len(emitter.events, 1)
		assertGenerationCompleteEvent(t, org, emitter.events[0], deltastore.GenerationKey{OrgID: org, ImageRepository: repo, SourceDigest: src, TargetDigest: tgt}, domain.DeltaGenerationProgressSucceeded)
	})

	t.Run("When generation is already terminal it should re-emit the terminal event", func(t *testing.T) {
		tests := []struct {
			name       string
			status     string
			wantStatus domain.DeltaGenerationProgressDetailsGenerationStatus
		}{
			{
				name:       "When generation already succeeded it should re-emit the terminal event",
				status:     model.DeltaGenerationSucceeded,
				wantStatus: domain.DeltaGenerationProgressSucceeded,
			},
			{
				name:       "When generation already failed it should re-emit the terminal event",
				status:     model.DeltaGenerationFailed,
				wantStatus: domain.DeltaGenerationProgressFailed,
			},
			{
				name:       "When generation was already rejected it should re-emit the terminal event",
				status:     model.DeltaGenerationRejected,
				wantStatus: domain.DeltaGenerationProgressRejected,
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				req := require.New(t)
				key := deltastore.GenerationKey{OrgID: org, ImageRepository: repo, SourceDigest: src, TargetDigest: tgt}
				store := &fakeGenerationService{generations: map[deltastore.GenerationKey]*model.DeltaGeneration{
					key: {
						OrgID:           org,
						ImageRepository: repo,
						SourceDigest:    src,
						TargetDigest:    tgt,
						Status:          tt.status,
					},
				}}
				emitter := &recordingEventEmitter{}
				c := newTestHandler(t, store, &deltaconfig.DeltaGenerationConfig{Timeout: util.Duration(time.Minute)}, nil, nil, emitter.Emit)

				req.NoError(c.Handle(context.Background(), generateEvent(org, repo, src, tgt), log))
				req.Len(emitter.events, 1)
				assertGenerationCompleteEvent(t, org, emitter.events[0], key, tt.wantStatus)
			})
		}
	})

	t.Run("When existence check fails it should fail generation and emit terminal event", func(t *testing.T) {
		req := require.New(t)
		store := &fakeGenerationService{}
		emitter := &recordingEventEmitter{}
		c := newTestHandler(t, store, &deltaconfig.DeltaGenerationConfig{Timeout: util.Duration(time.Minute)}, func(context.Context, uuid.UUID, string, string, string) (*existingDelta, error) {
			return nil, errors.New("registry unavailable")
		}, func(context.Context, uuid.UUID, string, string, string) (string, int64, error) {
			t.Fatal("generate must not run")
			return "", 0, nil
		}, emitter.Emit)
		err := c.Handle(context.Background(), generateEvent(org, repo, src, tgt), log)
		req.NoError(err)
		req.Zero(store.createCalls)
		req.Len(store.updates, 2)
		req.Equal(model.DeltaGenerationInProgress, store.updates[0].Status)
		req.Equal(model.DeltaGenerationFailed, store.updates[1].Status)
		req.Len(emitter.events, 1)
		assertGenerationCompleteEvent(t, org, emitter.events[0], deltastore.GenerationKey{OrgID: org, ImageRepository: repo, SourceDigest: src, TargetDigest: tgt}, domain.DeltaGenerationProgressFailed)
	})

	t.Run("When miss it should claim generate and update succeeded", func(t *testing.T) {
		req := require.New(t)
		store := &fakeGenerationService{claimedRV: 4}
		c := newTestHandler(t, store, &deltaconfig.DeltaGenerationConfig{
			Timeout: util.Duration(time.Minute),
			DefaultRepository: &deltaconfig.DefaultRepositoryConfig{
				Registry:   "write.example",
				Repository: lo.ToPtr("os"),
			},
		}, func(context.Context, uuid.UUID, string, string, string) (*existingDelta, error) {
			return nil, nil
		}, func(_ context.Context, _ uuid.UUID, sourceRef, targetRef, pushPath string) (string, int64, error) {
			req.Equal(repo+"@"+src, sourceRef)
			req.Equal(repo+"@"+tgt, targetRef)
			req.Equal("write.example/os", pushPath)
			return "write.example/os@sha256:delta", 12, nil
		}, noOpEventEmitter)
		req.NoError(c.Handle(context.Background(), generateEvent(org, repo, src, tgt), log))
		req.Zero(store.createCalls)
		req.Equal(1, store.claimed)
		req.Len(store.updates, 2)
		req.Equal(model.DeltaGenerationSucceeded, store.updates[1].Status)
		req.Equal("write.example/os@sha256:delta", *store.updates[1].DeltaRef)
		req.Equal(int64(12), *store.updates[1].SizeBytes)
	})

	t.Run("When generate fails it should update failed", func(t *testing.T) {
		req := require.New(t)
		store := &fakeGenerationService{}
		var logOutput bytes.Buffer
		failureLog := logrus.New()
		failureLog.SetOutput(&logOutput)
		failureLog.SetLevel(logrus.ErrorLevel)
		c := newTestHandler(t, store, &deltaconfig.DeltaGenerationConfig{Timeout: util.Duration(time.Minute)}, func(context.Context, uuid.UUID, string, string, string) (*existingDelta, error) {
			return nil, nil
		}, func(context.Context, uuid.UUID, string, string, string) (string, int64, error) {
			return "", 0, errors.New("oci-delta exploded")
		}, noOpEventEmitter)
		c.log = failureLog
		req.NoError(c.Handle(context.Background(), generateEvent(org, repo, src, tgt), log))
		req.Len(store.updates, 2)
		req.Equal(model.DeltaGenerationFailed, store.updates[1].Status)
		req.Contains(logOutput.String(), "delta generation failed")
		req.Contains(logOutput.String(), "oci-delta exploded")
		req.Contains(logOutput.String(), repo)
		req.Contains(logOutput.String(), src)
		req.Contains(logOutput.String(), tgt)
		req.Contains(logOutput.String(), "phase=checkingExisting")
	})

	t.Run("When claim is in_progress it should not steal", func(t *testing.T) {
		req := require.New(t)
		store := &fakeGenerationService{claimErr: flterrors.ErrNoRowsUpdated}
		c := newTestHandler(t, store, &deltaconfig.DeltaGenerationConfig{Timeout: util.Duration(time.Minute)}, func(context.Context, uuid.UUID, string, string, string) (*existingDelta, error) {
			return nil, nil
		}, func(context.Context, uuid.UUID, string, string, string) (string, int64, error) {
			t.Fatal("generate must not run")
			return "", 0, nil
		}, noOpEventEmitter)
		req.NoError(c.Handle(context.Background(), generateEvent(org, repo, src, tgt), log))
		req.Empty(store.updates)
	})

	t.Run("When claiming fails it should return the error without marking failed", func(t *testing.T) {
		req := require.New(t)
		store := &fakeGenerationService{claimErr: errors.New("claim store unavailable")}
		generated := false
		c := newTestHandler(t, store, &deltaconfig.DeltaGenerationConfig{Timeout: util.Duration(time.Minute)}, func(context.Context, uuid.UUID, string, string, string) (*existingDelta, error) {
			return nil, nil
		}, func(context.Context, uuid.UUID, string, string, string) (string, int64, error) {
			generated = true
			return "ref", 1, nil
		}, noOpEventEmitter)
		err := c.Handle(context.Background(), generateEvent(org, repo, src, tgt), log)
		req.ErrorContains(err, "claim generation: claim store unavailable")
		req.False(generated)
		req.Empty(store.updates)
	})

	t.Run("When persist succeeded fails it should mark failed", func(t *testing.T) {
		req := require.New(t)
		store := &fakeGenerationService{casFailN: 1}
		generated := false
		c := newTestHandler(t, store, &deltaconfig.DeltaGenerationConfig{Timeout: util.Duration(time.Minute)}, func(context.Context, uuid.UUID, string, string, string) (*existingDelta, error) {
			return nil, nil
		}, func(context.Context, uuid.UUID, string, string, string) (string, int64, error) {
			generated = true
			return "ref", 1, nil
		}, noOpEventEmitter)
		req.NoError(c.Handle(context.Background(), generateEvent(org, repo, src, tgt), log))
		req.True(generated)
		req.Len(store.updates, 2)
		req.Equal(model.DeltaGenerationFailed, store.updates[1].Status)
	})

	t.Run("When generate context times out it should update failed", func(t *testing.T) {
		req := require.New(t)
		store := &fakeGenerationService{}
		c := newTestHandler(t, store, &deltaconfig.DeltaGenerationConfig{Timeout: util.Duration(100 * time.Millisecond)}, func(context.Context, uuid.UUID, string, string, string) (*existingDelta, error) {
			return nil, nil
		}, func(ctx context.Context, _ uuid.UUID, _, _, _ string) (string, int64, error) {
			<-ctx.Done()
			return "", 0, ctx.Err()
		}, noOpEventEmitter)
		req.NoError(c.Handle(context.Background(), generateEvent(org, repo, src, tgt), log))
		req.Len(store.updates, 2)
		req.Equal(model.DeltaGenerationFailed, store.updates[1].Status)
	})

	t.Run("When update is stale it should not overwrite", func(t *testing.T) {
		req := require.New(t)
		store := &fakeGenerationService{casErr: flterrors.ErrNoRowsUpdated}
		generated := false
		c := newTestHandler(t, store, &deltaconfig.DeltaGenerationConfig{Timeout: util.Duration(time.Minute)}, func(context.Context, uuid.UUID, string, string, string) (*existingDelta, error) {
			return nil, nil
		}, func(context.Context, uuid.UUID, string, string, string) (string, int64, error) {
			generated = true
			return "ref", 1, nil
		}, noOpEventEmitter)
		req.NoError(c.Handle(context.Background(), generateEvent(org, repo, src, tgt), log))
		req.True(generated)
		req.Len(store.updates, 1)
		req.Equal(model.DeltaGenerationInProgress, store.updates[0].Status)
	})
}

const (
	testSourceDigest = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	testTargetDigest = "sha256:2222222222222222222222222222222222222222222222222222222222222222"
)

func TestCheckExistingDelta(t *testing.T) {
	deltaManifest := ocispec.Manifest{
		ArtifactType: ociDeltaArtifactType,
		Config:       ocispec.Descriptor{Digest: digest.FromString("config"), Size: 10},
		Layers: []ocispec.Descriptor{
			{Digest: digest.FromString("layer-1"), Size: 20},
			{Digest: digest.FromString("layer-2"), Size: 30},
		},
		Subject:     &ocispec.Descriptor{Digest: digest.NewDigestFromEncoded(digest.SHA256, strings.TrimPrefix(testTargetDigest, "sha256:"))},
		Annotations: map[string]string{ociDeltaSourceAnnotation: testSourceDigest},
	}
	deltaManifestBytes, err := json.Marshal(deltaManifest)
	require.NoError(t, err)
	deltaDigest := digest.FromBytes(deltaManifestBytes)
	referrer := ocispec.Descriptor{
		MediaType:    ocispec.MediaTypeImageManifest,
		Digest:       deltaDigest,
		Size:         int64(len(deltaManifestBytes)),
		ArtifactType: ociDeltaArtifactType,
		Annotations: map[string]string{
			ociDeltaSourceAnnotation: testSourceDigest,
		},
	}

	tests := []struct {
		name            string
		repository      string
		handler         http.HandlerFunc
		wantFound       bool
		wantSize        int64
		wantError       bool
		wantErrorSubstr string
	}{
		{
			name: "When Referrers returns a matching delta it should be found with manifest size_bytes",
			handler: func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.Contains(r.URL.Path, "/referrers/"):
					writeJSON(w, http.StatusOK, ocispec.Index{Manifests: []ocispec.Descriptor{referrer}})
				case strings.Contains(r.URL.Path, "/manifests/"+deltaDigest.String()):
					writeJSON(w, http.StatusOK, deltaManifest)
				default:
					http.NotFound(w, r)
				}
			},
			wantFound: true,
			wantSize:  60,
		},
		{
			name: "When Referrers 404 and Tag Schema has a matching delta it should be found",
			handler: func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.Contains(r.URL.Path, "/referrers/"):
					http.NotFound(w, r)
				case strings.Contains(r.URL.Path, "/manifests/"+strings.Replace(testTargetDigest, ":", "-", 1)):
					writeJSON(w, http.StatusOK, ocispec.Index{Manifests: []ocispec.Descriptor{referrer}})
				case strings.Contains(r.URL.Path, "/manifests/"+deltaDigest.String()):
					writeJSON(w, http.StatusOK, deltaManifest)
				default:
					http.NotFound(w, r)
				}
			},
			wantFound: true,
			wantSize:  60,
		},
		{
			name: "When Referrers 200 is empty it should be not found without Tag Schema",
			handler: func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.Contains(r.URL.Path, "/referrers/"):
					writeJSON(w, http.StatusOK, ocispec.Index{Manifests: []ocispec.Descriptor{}})
				case strings.Contains(r.URL.Path, "/manifests/"+strings.Replace(testTargetDigest, ":", "-", 1)):
					t.Errorf("Tag Schema must not be queried after a Referrers 200")
					http.NotFound(w, r)
				default:
					http.NotFound(w, r)
				}
			},
			wantFound: false,
		},
		{
			name: "When Referrers and Tag Schema both 404 it should be not found",
			handler: func(w http.ResponseWriter, r *http.Request) {
				http.NotFound(w, r)
			},
			wantFound: false,
		},
		{
			name: "When Referrers returns manifest invalid and Tag Schema is missing it should be not found",
			handler: func(w http.ResponseWriter, r *http.Request) {
				if strings.Contains(r.URL.Path, "/referrers/") {
					writeRegistryError(w, http.StatusBadRequest, "MANIFEST_INVALID", "manifest invalid")
					return
				}
				http.NotFound(w, r)
			},
			wantFound: false,
		},
		{
			name: "When Referrers returns manifest invalid and Tag Schema has a matching delta it should be found",
			handler: func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.Contains(r.URL.Path, "/referrers/"):
					writeRegistryError(w, http.StatusBadRequest, "MANIFEST_INVALID", "manifest invalid")
				case strings.Contains(r.URL.Path, "/manifests/"+strings.Replace(testTargetDigest, ":", "-", 1)):
					writeJSON(w, http.StatusOK, ocispec.Index{Manifests: []ocispec.Descriptor{referrer}})
				case strings.Contains(r.URL.Path, "/manifests/"+deltaDigest.String()):
					writeJSON(w, http.StatusOK, deltaManifest)
				default:
					http.NotFound(w, r)
				}
			},
			wantFound: true,
			wantSize:  60,
		},
		{
			name: "When Referrers returns another 400 it should return an error",
			handler: func(w http.ResponseWriter, r *http.Request) {
				if strings.Contains(r.URL.Path, "/manifests/"+strings.Replace(testTargetDigest, ":", "-", 1)) {
					t.Errorf("Tag Schema must not be queried after a non-manifest-invalid referrers error")
				}
				writeRegistryError(w, http.StatusBadRequest, "DENIED", "denied")
			},
			wantError:       true,
			wantErrorSubstr: "400",
		},
		{
			name: "When the delta manifest fetch returns manifest invalid it should return an error",
			handler: func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.Contains(r.URL.Path, "/referrers/"):
					writeJSON(w, http.StatusOK, ocispec.Index{Manifests: []ocispec.Descriptor{referrer}})
				case strings.Contains(r.URL.Path, "/manifests/"+deltaDigest.String()):
					writeRegistryError(w, http.StatusBadRequest, "MANIFEST_INVALID", "manifest invalid")
				default:
					http.NotFound(w, r)
				}
			},
			wantError:       true,
			wantErrorSubstr: "manifest invalid",
		},
		{
			name:       "When the repository path contains referrers and the manifest fetch returns manifest invalid it should keep that error",
			repository: "team-a/referrers/os",
			handler: func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.HasSuffix(r.URL.Path, "/referrers/"+testTargetDigest):
					writeJSON(w, http.StatusOK, ocispec.Index{Manifests: []ocispec.Descriptor{referrer}})
				case strings.Contains(r.URL.Path, "/manifests/"+deltaDigest.String()):
					writeRegistryError(w, http.StatusBadRequest, "MANIFEST_INVALID", "manifest invalid")
				default:
					http.NotFound(w, r)
				}
			},
			wantError:       true,
			wantErrorSubstr: "response status code 400: manifest invalid",
		},
		{
			name: "When Referrers returns 401 it should return an error",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusUnauthorized)
			},
			wantError: true,
		},
		{
			name: "When Referrers returns 500 it should return an error",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
			},
			wantError: true,
		},
		{
			name: "When Referrers lists a non-matching source digest it should be not found",
			handler: func(w http.ResponseWriter, r *http.Request) {
				other := referrer
				other.Annotations = map[string]string{ociDeltaSourceAnnotation: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
				writeJSON(w, http.StatusOK, ocispec.Index{Manifests: []ocispec.Descriptor{other}})
			},
			wantFound: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := require.New(t)
			srv := httptest.NewServer(tt.handler)
			t.Cleanup(srv.Close)

			u, err := url.Parse(srv.URL)
			req.NoError(err)
			repository := "team-a/os"
			if tt.repository != "" {
				repository = tt.repository
			}
			imageRepository := u.Host + "/" + repository
			scheme := domain.OciRepoSchemeHttp
			spec := &domain.OciRepoSpec{Registry: u.Host, Scheme: &scheme}

			got, err := checkExistingDelta(context.Background(), imageRepository, testSourceDigest, testTargetDigest, spec)
			if tt.wantError {
				req.Error(err)
				if tt.wantErrorSubstr != "" {
					req.ErrorContains(err, tt.wantErrorSubstr)
				}
				return
			}
			req.NoError(err)
			if tt.wantFound {
				req.NotNil(got)
				req.Equal(tt.wantSize, got.SizeBytes)
				req.Equal(imageRepository+"@"+deltaDigest.String(), got.Ref)
				return
			}
			req.Nil(got)
		})
	}
}

func TestReferrersMissingSubjectClient_WhenResponseIsPassedThroughOrReplaced(t *testing.T) {
	oversized := append(bytes.Repeat([]byte("x"), maxRegistryResponseBytes+1), []byte("-remainder")...)
	invalid := []byte(`{"errors":[{"code":"MANIFEST_INVALID","message":"manifest invalid"}]}`)
	denied := []byte(`{"errors":[{"code":"DENIED","message":"denied"}]}`)

	referrersURL := "https://registry.example/v2/team-a/os/referrers/" + testTargetDigest
	tests := []struct {
		name           string
		url            string
		body           []byte
		wantStatus     int
		wantBody       []byte
		wantContains   string
		closedOnReturn bool
	}{
		{
			name:           "When an oversized 400 is passed through it should retain the unread remainder",
			body:           oversized,
			wantStatus:     http.StatusBadRequest,
			wantBody:       oversized,
			closedOnReturn: false,
		},
		{
			name:           "When a small non-invalid 400 is passed through it should retain the full body",
			body:           denied,
			wantStatus:     http.StatusBadRequest,
			wantBody:       denied,
			closedOnReturn: false,
		},
		{
			name:           "When manifest invalid is rewritten it should close the original body",
			body:           invalid,
			wantStatus:     http.StatusNotFound,
			wantContains:   errCodeManifestUnknown,
			closedOnReturn: true,
		},
		{
			name:           "When a manifest GET for a repository containing referrers returns manifest invalid it should not be rewritten",
			url:            "https://registry.example/v2/team-a/referrers/os/manifests/" + testTargetDigest,
			body:           invalid,
			wantStatus:     http.StatusBadRequest,
			wantBody:       invalid,
			closedOnReturn: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := require.New(t)
			original := &closeTrackingBody{Reader: bytes.NewReader(tt.body)}
			client := referrersMissingSubjectClient{base: staticRegistryClient{resp: &http.Response{
				StatusCode: http.StatusBadRequest,
				Header:     make(http.Header),
				Body:       original,
			}}}
			requestURL := tt.url
			if requestURL == "" {
				requestURL = referrersURL
			}
			httpReq := httptest.NewRequest(http.MethodGet, requestURL, nil)

			resp, err := client.Do(httpReq)
			req.NoError(err)
			req.Equal(tt.closedOnReturn, original.closed)
			req.Equal(tt.wantStatus, resp.StatusCode)

			got, err := io.ReadAll(resp.Body)
			req.NoError(err)
			if tt.wantBody != nil {
				req.Equal(tt.wantBody, got)
			}
			if tt.wantContains != "" {
				req.Contains(string(got), tt.wantContains)
			}
			req.NoError(resp.Body.Close())
			req.True(original.closed)
		})
	}
}

type staticRegistryClient struct {
	resp *http.Response
}

func (c staticRegistryClient) Do(*http.Request) (*http.Response, error) {
	return c.resp, nil
}

type closeTrackingBody struct {
	io.Reader
	closed bool
}

func (b *closeTrackingBody) Close() error {
	b.closed = true
	return nil
}

func TestCheckExistingDelta_WhenRegistryUnreachableItShouldReturnAnError(t *testing.T) {
	req := require.New(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	req.NoError(err)
	addr := ln.Addr().String()
	req.NoError(ln.Close())

	scheme := domain.OciRepoSchemeHttp
	got, err := checkExistingDelta(context.Background(), addr+"/team-a/os", testSourceDigest, testTargetDigest, &domain.OciRepoSpec{
		Registry: addr,
		Scheme:   &scheme,
	})
	req.Error(err)
	req.Nil(got)
}

func writeRegistryError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	body, err := json.Marshal(map[string]any{
		"errors": []map[string]any{{
			"code":    code,
			"message": message,
			"detail":  map[string]any{},
		}},
	})
	if err != nil {
		return
	}
	_, _ = w.Write(body)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	contentType := "application/json"
	switch v.(type) {
	case ocispec.Index:
		contentType = ocispec.MediaTypeImageIndex
	case ocispec.Manifest:
		contentType = ocispec.MediaTypeImageManifest
	}
	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(status)
	body, err := json.Marshal(v)
	if err != nil {
		return
	}
	_, _ = w.Write(body)
}

type recordingRunner struct {
	calls [][]string
	errAt map[string]error
}

func (r *recordingRunner) Run(_ context.Context, name string, args []string) error {
	r.calls = append(r.calls, append([]string{name}, args...))
	if r.errAt != nil {
		if err, ok := r.errAt[name]; ok {
			return err
		}
	}
	return nil
}

func stubPull() func(context.Context, string, string) error {
	return func(context.Context, string, string) error { return nil }
}

func TestCreateAndPushDelta_WhenToolsSucceedItShouldPushLayoutNotOrasCLI(t *testing.T) {
	req := require.New(t)
	runner := &recordingRunner{}
	var pushedDir, pushedDest string
	g := generator{
		run:       runner,
		pullImage: stubPull(),
		pushLayout: func(_ context.Context, layoutDir, destRef, _, _ string) (string, error) {
			pushedDir = layoutDir
			pushedDest = destRef
			return destRef, nil
		},
		layoutPayloadSize: func(string) (int64, error) {
			return 42, nil
		},
		workDir: t.TempDir(),
	}

	deltaRef, size, err := g.createAndPushDelta(context.Background(), "quay.io/team-a/os@sha256:src", "quay.io/team-a/os@sha256:tgt", "registry.example.com/deltas/os")
	req.NoError(err)
	req.Equal(int64(42), size)
	req.Equal("registry.example.com/deltas/os", deltaRef)
	req.Equal("registry.example.com/deltas/os", pushedDest)
	req.Equal(filepath.Join(g.workDir, "delta"), pushedDir)

	req.Len(runner.calls, 1)
	req.Equal("oci-delta", runner.calls[0][0])
	req.Equal("create", runner.calls[0][1])
}

func TestCreateAndPushDelta_WhenLayerDoesNotShrinkItShouldStillSucceed(t *testing.T) {
	req := require.New(t)
	g := generator{
		run:       &recordingRunner{},
		pullImage: stubPull(),
		pushLayout: func(_ context.Context, _, destRef, _, _ string) (string, error) {
			return destRef, nil
		},
		layoutPayloadSize: func(string) (int64, error) {
			return 1 << 40, nil
		},
		workDir: t.TempDir(),
	}

	_, size, err := g.createAndPushDelta(context.Background(), "quay.io/a@sha256:s", "quay.io/a@sha256:t", "reg.example/delta")
	req.NoError(err)
	req.Equal(int64(1<<40), size)
}

func TestCreateAndPushDelta_WhenOciDeltaFailsItShouldReturnError(t *testing.T) {
	req := require.New(t)
	var pushed bool
	g := generator{
		run:       &recordingRunner{errAt: map[string]error{"oci-delta": errors.New("create failed")}},
		pullImage: stubPull(),
		pushLayout: func(_ context.Context, _, destRef, _, _ string) (string, error) {
			pushed = true
			return destRef, nil
		},
		layoutPayloadSize: func(string) (int64, error) {
			return 0, nil
		},
		workDir: t.TempDir(),
	}

	_, _, err := g.createAndPushDelta(context.Background(), "quay.io/a@sha256:s", "quay.io/a@sha256:t", "reg.example/delta")
	req.Error(err)
	req.False(pushed)
}

func TestCreateAndPushDelta_WhenPushLayoutFailsItShouldReturnError(t *testing.T) {
	req := require.New(t)
	g := generator{
		run:       &recordingRunner{},
		pullImage: stubPull(),
		pushLayout: func(context.Context, string, string, string, string) (string, error) {
			return "", errors.New("copy failed")
		},
		layoutPayloadSize: func(string) (int64, error) { return 1, nil },
		workDir:           t.TempDir(),
	}

	_, _, err := g.createAndPushDelta(context.Background(), "quay.io/a@sha256:s", "quay.io/a@sha256:t", "reg.example/delta")
	req.Error(err)
}

func TestCreateAndPushDelta_WhenContextIsCancelledItShouldReturnError(t *testing.T) {
	req := require.New(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	g := generator{
		run: runnerFunc(func(ctx context.Context, _ string, _ []string) error {
			return ctx.Err()
		}),
		layoutPayloadSize: func(string) (int64, error) { return 0, nil },
		workDir:           t.TempDir(),
	}

	_, _, err := g.createAndPushDelta(ctx, "quay.io/a@sha256:s", "quay.io/a@sha256:t", "reg.example/delta")
	req.Error(err)
}

func TestCreateAndPushDelta_WhenDeltaDirItShouldUseWorkSubdir(t *testing.T) {
	req := require.New(t)
	runner := &recordingRunner{}
	work := t.TempDir()
	g := generator{
		run:       runner,
		pullImage: stubPull(),
		pushLayout: func(_ context.Context, _, destRef, _, _ string) (string, error) {
			return destRef, nil
		},
		layoutPayloadSize: func(dir string) (int64, error) { return 1, nil },
		workDir:           work,
	}

	_, _, err := g.createAndPushDelta(context.Background(), "quay.io/a@sha256:s", "quay.io/a@sha256:t", "reg.example/delta")
	req.NoError(err)
	req.Contains(runner.calls[0], "oci:"+filepath.Join(work, "source")+":img")
}

type runnerFunc func(ctx context.Context, name string, args []string) error

func (f runnerFunc) Run(ctx context.Context, name string, args []string) error {
	return f(ctx, name, args)
}

func TestReferenceForResolve_WhenDigestRefItShouldReturnDigest(t *testing.T) {
	req := require.New(t)
	const dgst = "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	got, err := referenceForResolve("quay.io/team-a/os@" + dgst)
	req.NoError(err)
	req.Equal(dgst, got)
}

func TestCopyDeltaGraph_WhenSubjectManifestIsAlsoLayerItShouldPublishLayerWithoutSubjectGraph(t *testing.T) {
	req := require.New(t)
	ctx := context.Background()
	layoutDir := t.TempDir()
	srcDir := t.TempDir()
	destDir := t.TempDir()

	src, err := ocistore.New(srcDir)
	req.NoError(err)
	dest, err := ocistore.New(destDir)
	req.NoError(err)
	subjectPayload := []byte("os-layer")
	subjectLayer := ocispec.Descriptor{
		MediaType: ocispec.MediaTypeImageLayer,
		Digest:    digest.FromBytes(subjectPayload),
		Size:      int64(len(subjectPayload)),
	}
	const sourceDigest = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	req.NoError(src.Push(ctx, subjectLayer, bytes.NewReader(subjectPayload)))
	subject, err := oras.PackManifest(ctx, src, oras.PackManifestVersion1_1, ocispec.MediaTypeImageManifest, oras.PackManifestOptions{
		Layers: []ocispec.Descriptor{subjectLayer},
	})
	req.NoError(err)
	subjectReader, err := src.Fetch(ctx, subject)
	req.NoError(err)
	subjectManifestBytes, err := io.ReadAll(subjectReader)
	req.NoError(err)
	req.NoError(subjectReader.Close())

	layout, err := ocistore.New(layoutDir)
	req.NoError(err)
	req.NoError(layout.Push(ctx, subject, bytes.NewReader(subjectManifestBytes)))
	targetManifestLayer := subject
	targetManifestLayer.Annotations = map[string]string{"io.github.containers.delta.content": "image-manifest"}
	deltaPayload := []byte("delta-layer")
	deltaLayer := ocispec.Descriptor{
		MediaType: ocispec.MediaTypeImageLayer,
		Digest:    digest.FromBytes(deltaPayload),
		Size:      int64(len(deltaPayload)),
	}
	req.NoError(layout.Push(ctx, deltaLayer, bytes.NewReader(deltaPayload)))
	layoutManifest, err := oras.PackManifest(ctx, layout, oras.PackManifestVersion1_1, ociDeltaArtifactType, oras.PackManifestOptions{
		Subject: &subject,
		Layers:  []ocispec.Descriptor{targetManifestLayer, deltaLayer},
		ManifestAnnotations: map[string]string{
			ociDeltaSourceAnnotation: sourceDigest,
		},
	})
	req.NoError(err)
	req.NoError(layout.Tag(ctx, layoutManifest, layoutTag))

	loaded, err := loadDeltaLayout(ctx, layoutDir)
	req.NoError(err)
	req.NoError(loaded.matchesPair(sourceDigest, subject.Digest.String()))
	req.NoError(copyDeltaGraph(ctx, loaded, dest, dest))

	rc, err := dest.Fetch(ctx, loaded.root)
	req.NoError(err)
	defer rc.Close()
	b, err := io.ReadAll(rc)
	req.NoError(err)
	var manifest ocispec.Manifest
	req.NoError(json.Unmarshal(b, &manifest))
	req.NotNil(manifest.Subject)
	req.Equal(subject.Digest, manifest.Subject.Digest)
	req.Equal(loaded.root.Digest, digest.FromBytes(b))
	req.Len(manifest.Layers, 2)
	req.Equal(subject.Digest, manifest.Layers[0].Digest)
	req.Equal("image-manifest", manifest.Layers[0].Annotations["io.github.containers.delta.content"])

	rc, err = dest.Fetch(ctx, subject)
	req.NoError(err)
	gotSubjectManifest, err := io.ReadAll(rc)
	req.NoError(err)
	req.NoError(rc.Close())
	req.Equal(subjectManifestBytes, gotSubjectManifest)

	exists, err := dest.Exists(ctx, subjectLayer)
	req.NoError(err)
	req.False(exists)
}

func TestDeltaLayout_WhenPairDoesNotMatchItShouldError(t *testing.T) {
	req := require.New(t)
	layout := &deltaLayout{
		subject: ocispec.Descriptor{Digest: digest.FromBytes([]byte("tgt"))},
		annotations: map[string]string{
			ociDeltaSourceAnnotation: "sha256:src",
		},
	}
	req.Error(layout.matchesPair("sha256:other", layout.subject.Digest.String()))
	req.Error(layout.matchesPair("sha256:src", "sha256:other"))
	req.NoError(layout.matchesPair("sha256:src", layout.subject.Digest.String()))
}

func TestCopyImageToLayout_WhenSourceHasTaggedManifestItShouldTagLayout(t *testing.T) {
	req := require.New(t)
	ctx := context.Background()
	srcDir := t.TempDir()
	dstDir := t.TempDir()

	src, err := ocistore.New(srcDir)
	req.NoError(err)
	payload := []byte("os-layer")
	layerDesc := ocispec.Descriptor{
		MediaType: ocispec.MediaTypeImageLayer,
		Digest:    digest.FromBytes(payload),
		Size:      int64(len(payload)),
	}
	req.NoError(src.Push(ctx, layerDesc, bytes.NewReader(payload)))
	manifestDesc, err := oras.PackManifest(ctx, src, oras.PackManifestVersion1_1, ocispec.MediaTypeImageManifest, oras.PackManifestOptions{
		Layers: []ocispec.Descriptor{layerDesc},
	})
	req.NoError(err)
	req.NoError(src.Tag(ctx, manifestDesc, "v1"))

	req.NoError(copyImageToLayout(ctx, src, "v1", dstDir))
	dst, err := ocistore.New(dstDir)
	req.NoError(err)
	got, err := dst.Resolve(ctx, layoutTag)
	req.NoError(err)
	req.Equal(manifestDesc.Digest, got.Digest)
}

func TestRemoteImageRepository_WhenRefHasNoTagOrDigestItShouldReturnError(t *testing.T) {
	req := require.New(t)
	_, _, err := remoteImageRepository(context.Background(), nil, "quay.io/team-a/os")
	req.Error(err)
}

func TestRemoteImageRepository_WhenRefHasDigestItShouldReturnRepositoryAndDigest(t *testing.T) {
	req := require.New(t)
	const dgst = "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	repo, ref, err := remoteImageRepository(context.Background(), nil, "quay.io/team-a/os@"+dgst)
	req.NoError(err)
	req.Equal(dgst, ref)
	req.Equal("quay.io/team-a/os", repo.Reference.Registry+"/"+repo.Reference.Repository)
}

func TestRemoteImageRepository_WhenDockerPrefixItShouldParse(t *testing.T) {
	req := require.New(t)
	_, ref, err := remoteImageRepository(context.Background(), nil, "docker://quay.io/team-a/os:v1")
	req.NoError(err)
	req.Equal("v1", ref)
}

func TestPushOCILayout_WhenWriteSpecIsMissingItShouldReturnError(t *testing.T) {
	req := require.New(t)
	_, err := pushOCILayout(context.Background(), nil, t.TempDir(), "registry.example.com/os", "registry.example.com/os@sha256:src", "registry.example.com/os@sha256:tgt")
	req.Error(err)
}
