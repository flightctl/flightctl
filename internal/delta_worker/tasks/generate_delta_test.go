package tasks

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	deltastore "github.com/flightctl/flightctl/internal/store/delta"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/worker_client"
	"github.com/google/uuid"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	"oras.land/oras-go/v2"
	ocistore "oras.land/oras-go/v2/content/oci"
)

type fakeGenerationStore struct {
	rejected     []*model.DeltaGeneration
	inserted     []*model.DeltaGeneration
	claimed      int
	claimErr     error
	cas          []deltastore.GenerationCAS
	casErr       error
	casFailN     int
	waiting      []model.DeltaPrepare
	claimedRV    int64
	listWaitingN int
}

func (f *fakeGenerationStore) InitialMigration(context.Context) error { return nil }

func (f *fakeGenerationStore) GetGeneration(context.Context, deltastore.GenerationKey, ...deltastore.GenerationGetOption) (*model.DeltaGeneration, error) {
	return nil, nil
}

func (f *fakeGenerationStore) InsertPrepare(context.Context, *model.DeltaPrepare) error { return nil }

func (f *fakeGenerationStore) GetPrepare(context.Context, uuid.UUID) (*model.DeltaPrepare, error) {
	return nil, nil
}

func (f *fakeGenerationStore) CASPrepareStatus(context.Context, uuid.UUID, string) error { return nil }

func (f *fakeGenerationStore) ListWaitingPastDeadline(context.Context, int, time.Time) ([]model.DeltaPrepare, error) {
	return nil, nil
}

func (f *fakeGenerationStore) InsertPrepareGenerations(context.Context, uuid.UUID, []deltastore.GenerationKey) error {
	return nil
}

func (f *fakeGenerationStore) GetWaitingPrepare(context.Context, uuid.UUID, string, string) (*model.DeltaPrepare, error) {
	return nil, nil
}

func (f *fakeGenerationStore) CountPreparePairs(context.Context, uuid.UUID) (int, int, error) {
	return 0, 0, nil
}

func (f *fakeGenerationStore) SetGenerationPhase(context.Context, deltastore.GenerationKey, string) error {
	return nil
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

func TestHandleGenerateDelta(t *testing.T) {
	org := uuid.New()
	log := logrus.New()
	log.SetLevel(logrus.ErrorLevel)
	src := "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	tgt := "sha256:2222222222222222222222222222222222222222222222222222222222222222"
	repo := "quay.io/team-a/os"

	t.Run("When PrepareDeltas it should not generate", func(t *testing.T) {
		req := require.New(t)
		store := &fakeGenerationStore{}
		c := &Consumer{
			store:      store,
			jobTimeout: time.Minute,
			existenceCheck: func(context.Context, uuid.UUID, string, string, string) (existenceResult, error) {
				t.Fatal("existence check must not run")
				return existenceResult{}, nil
			},
		}
		err := c.handleGenerateDelta(context.Background(), worker_client.EventWithOrgId{
			OrgId: org,
			Event: domain.Event{Reason: domain.EventReasonPrepareDeltas},
		}, log)
		req.NoError(err)
		req.Empty(store.inserted)
	})

	t.Run("When existence is found it should insert rejected and not generate", func(t *testing.T) {
		req := require.New(t)
		store := &fakeGenerationStore{waiting: []model.DeltaPrepare{{Name: "fleet-a"}}}
		var generated bool
		c := &Consumer{
			store:      store,
			jobTimeout: time.Minute,
			existenceCheck: func(context.Context, uuid.UUID, string, string, string) (existenceResult, error) {
				return existenceResult{Status: existenceFound, SizeBytes: 77}, nil
			},
			generateDelta: func(context.Context, uuid.UUID, string, string, string) (string, int64, error) {
				generated = true
				return "", 0, nil
			},
		}
		req.NoError(c.handleGenerateDelta(context.Background(), generateEvent(org, repo, src, tgt), log))
		req.False(generated)
		req.Len(store.rejected, 1)
		req.Equal(int64(77), *store.rejected[0].SizeBytes)
		req.Equal(1, store.listWaitingN)
		req.Empty(store.inserted)
	})

	t.Run("When existence is inconclusive it should return retryable error", func(t *testing.T) {
		req := require.New(t)
		store := &fakeGenerationStore{}
		c := &Consumer{
			store:      store,
			jobTimeout: time.Minute,
			existenceCheck: func(context.Context, uuid.UUID, string, string, string) (existenceResult, error) {
				return existenceResult{Status: existenceInconclusive}, nil
			},
			generateDelta: func(context.Context, uuid.UUID, string, string, string) (string, int64, error) {
				t.Fatal("generate must not run")
				return "", 0, nil
			},
		}
		err := c.handleGenerateDelta(context.Background(), generateEvent(org, repo, src, tgt), log)
		req.Error(err)
		req.Contains(err.Error(), "inconclusive")
		req.Empty(store.inserted)
		req.Empty(store.rejected)
	})

	t.Run("When miss it should claim generate and CAS succeeded", func(t *testing.T) {
		req := require.New(t)
		store := &fakeGenerationStore{claimedRV: 4}
		c := &Consumer{
			store:      store,
			jobTimeout: time.Minute,
			existenceCheck: func(context.Context, uuid.UUID, string, string, string) (existenceResult, error) {
				return existenceResult{Status: existenceNotFound}, nil
			},
			generateDelta: func(_ context.Context, _ uuid.UUID, sourceRef, targetRef, pushPath string) (string, int64, error) {
				req.Equal(repo+"@"+src, sourceRef)
				req.Equal(repo+"@"+tgt, targetRef)
				req.Equal("write.example/os", pushPath)
				return "write.example/os@sha256:delta", 12, nil
			},
			pushPath: func(context.Context, uuid.UUID, string) (string, error) { return "write.example/os", nil },
		}
		req.NoError(c.handleGenerateDelta(context.Background(), generateEvent(org, repo, src, tgt), log))
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
		c := &Consumer{
			store:      store,
			jobTimeout: time.Minute,
			existenceCheck: func(context.Context, uuid.UUID, string, string, string) (existenceResult, error) {
				return existenceResult{Status: existenceNotFound}, nil
			},
			generateDelta: func(context.Context, uuid.UUID, string, string, string) (string, int64, error) {
				return "", 0, errors.New("oci-delta exploded")
			},
		}
		req.NoError(c.handleGenerateDelta(context.Background(), generateEvent(org, repo, src, tgt), log))
		req.Len(store.cas, 1)
		req.Equal(model.DeltaGenerationFailed, store.cas[0].Status)
		req.Equal(1, store.listWaitingN)
	})

	t.Run("When claim is in_progress it should not steal", func(t *testing.T) {
		req := require.New(t)
		store := &fakeGenerationStore{claimErr: flterrors.ErrNoRowsUpdated}
		c := &Consumer{
			store:      store,
			jobTimeout: time.Minute,
			existenceCheck: func(context.Context, uuid.UUID, string, string, string) (existenceResult, error) {
				return existenceResult{Status: existenceNotFound}, nil
			},
			generateDelta: func(context.Context, uuid.UUID, string, string, string) (string, int64, error) {
				t.Fatal("generate must not run")
				return "", 0, nil
			},
		}
		req.NoError(c.handleGenerateDelta(context.Background(), generateEvent(org, repo, src, tgt), log))
		req.Empty(store.cas)
	})

	t.Run("When persist succeeded fails it should mark failed and resume", func(t *testing.T) {
		req := require.New(t)
		store := &fakeGenerationStore{casFailN: 1}
		c := &Consumer{
			store:      store,
			jobTimeout: time.Minute,
			existenceCheck: func(context.Context, uuid.UUID, string, string, string) (existenceResult, error) {
				return existenceResult{Status: existenceNotFound}, nil
			},
			generateDelta: func(context.Context, uuid.UUID, string, string, string) (string, int64, error) {
				return "ref", 1, nil
			},
		}
		req.NoError(c.handleGenerateDelta(context.Background(), generateEvent(org, repo, src, tgt), log))
		req.Len(store.cas, 1)
		req.Equal(model.DeltaGenerationFailed, store.cas[0].Status)
		req.Equal(1, store.listWaitingN)
	})

	t.Run("When generate context times out it should CAS failed", func(t *testing.T) {
		req := require.New(t)
		store := &fakeGenerationStore{}
		c := &Consumer{
			store:      store,
			jobTimeout: time.Nanosecond,
			existenceCheck: func(context.Context, uuid.UUID, string, string, string) (existenceResult, error) {
				return existenceResult{Status: existenceNotFound}, nil
			},
			generateDelta: func(ctx context.Context, _ uuid.UUID, _, _, _ string) (string, int64, error) {
				<-ctx.Done()
				return "", 0, ctx.Err()
			},
		}
		req.NoError(c.handleGenerateDelta(context.Background(), generateEvent(org, repo, src, tgt), log))
		req.Len(store.cas, 1)
		req.Equal(model.DeltaGenerationFailed, store.cas[0].Status)
		req.Equal(1, store.listWaitingN)
	})

	t.Run("When CAS is stale it should not overwrite", func(t *testing.T) {
		req := require.New(t)
		store := &fakeGenerationStore{casErr: flterrors.ErrNoRowsUpdated}
		c := &Consumer{
			store:      store,
			jobTimeout: time.Minute,
			existenceCheck: func(context.Context, uuid.UUID, string, string, string) (existenceResult, error) {
				return existenceResult{Status: existenceNotFound}, nil
			},
			generateDelta: func(context.Context, uuid.UUID, string, string, string) (string, int64, error) {
				return "ref", 1, nil
			},
		}
		req.NoError(c.handleGenerateDelta(context.Background(), generateEvent(org, repo, src, tgt), log))
		req.Equal(0, store.listWaitingN)
	})
}

const (
	testSourceDigest = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	testTargetDigest = "sha256:2222222222222222222222222222222222222222222222222222222222222222"
	testDeltaDigest  = "sha256:3333333333333333333333333333333333333333333333333333333333333333"
)

func TestCheckExistingDelta(t *testing.T) {
	deltaManifest := ocispec.Manifest{
		Config: ocispec.Descriptor{Size: 10},
		Layers: []ocispec.Descriptor{{Size: 20}, {Size: 30}},
	}
	indexSize := int64(9999)
	referrer := ocispec.Descriptor{
		MediaType:    ocispec.MediaTypeImageManifest,
		Digest:       testDeltaDigest,
		Size:         indexSize,
		ArtifactType: ociDeltaArtifactType,
		Annotations: map[string]string{
			ociDeltaSourceAnnotation: testSourceDigest,
		},
	}

	tests := []struct {
		name     string
		handler  http.HandlerFunc
		want     existenceStatus
		wantSize int64
	}{
		{
			name: "When Referrers returns a matching delta it should be found with manifest size_bytes",
			handler: func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.Contains(r.URL.Path, "/referrers/"):
					writeJSON(w, http.StatusOK, ocispec.Index{Manifests: []ocispec.Descriptor{referrer}})
				case strings.Contains(r.URL.Path, "/manifests/"+testDeltaDigest):
					writeJSON(w, http.StatusOK, deltaManifest)
				default:
					http.NotFound(w, r)
				}
			},
			want:     existenceFound,
			wantSize: 60,
		},
		{
			name: "When Referrers 404 and Tag Schema has a matching delta it should be found",
			handler: func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.Contains(r.URL.Path, "/referrers/"):
					http.NotFound(w, r)
				case strings.Contains(r.URL.Path, "/manifests/"+tagSchemaRef(testTargetDigest)):
					writeJSON(w, http.StatusOK, ocispec.Index{Manifests: []ocispec.Descriptor{referrer}})
				case strings.Contains(r.URL.Path, "/manifests/"+testDeltaDigest):
					writeJSON(w, http.StatusOK, deltaManifest)
				default:
					http.NotFound(w, r)
				}
			},
			want:     existenceFound,
			wantSize: 60,
		},
		{
			name: "When Referrers 200 is empty it should be not found without Tag Schema",
			handler: func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.Contains(r.URL.Path, "/referrers/"):
					writeJSON(w, http.StatusOK, ocispec.Index{Manifests: []ocispec.Descriptor{}})
				case strings.Contains(r.URL.Path, "/manifests/"+tagSchemaRef(testTargetDigest)):
					t.Errorf("Tag Schema must not be queried after a Referrers 200")
					http.NotFound(w, r)
				default:
					http.NotFound(w, r)
				}
			},
			want: existenceNotFound,
		},
		{
			name: "When Referrers and Tag Schema both 404 it should be not found",
			handler: func(w http.ResponseWriter, r *http.Request) {
				http.NotFound(w, r)
			},
			want: existenceNotFound,
		},
		{
			name: "When Referrers returns 401 it should be inconclusive",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusUnauthorized)
			},
			want: existenceInconclusive,
		},
		{
			name: "When Referrers returns 500 it should be inconclusive",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
			},
			want: existenceInconclusive,
		},
		{
			name: "When Referrers lists a non-matching source digest it should be not found",
			handler: func(w http.ResponseWriter, r *http.Request) {
				other := referrer
				other.Annotations = map[string]string{ociDeltaSourceAnnotation: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
				writeJSON(w, http.StatusOK, ocispec.Index{Manifests: []ocispec.Descriptor{other}})
			},
			want: existenceNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := require.New(t)
			srv := httptest.NewServer(tt.handler)
			t.Cleanup(srv.Close)

			u, err := url.Parse(srv.URL)
			req.NoError(err)
			imageRepository := u.Host + "/team-a/os"

			got, err := checkExistingDelta(context.Background(), imageRepository, testSourceDigest, testTargetDigest, ExistenceConfig{
				Client: srv.Client(),
				Scheme: "http",
			})
			req.NoError(err)
			req.Equal(tt.want, got.Status)
			if tt.want == existenceFound {
				req.Equal(tt.wantSize, got.SizeBytes)
			}
		})
	}
}

func TestCheckExistingDelta_WhenRegistryUnreachableItShouldBeInconclusive(t *testing.T) {
	req := require.New(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	req.NoError(err)
	addr := ln.Addr().String()
	req.NoError(ln.Close())

	got, err := checkExistingDelta(context.Background(), addr+"/team-a/os", testSourceDigest, testTargetDigest, ExistenceConfig{
		Client: &http.Client{Timeout: 2 * time.Second},
		Scheme: "http",
	})
	req.NoError(err)
	req.Equal(existenceInconclusive, got.Status)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

type recordingRunner struct {
	calls [][]string
	errAt map[string]error
}

func (r *recordingRunner) Run(_ context.Context, name string, args []string, _ func(string)) error {
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
		run: runnerFunc(func(ctx context.Context, _ string, _ []string, _ func(string)) error {
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

type runnerFunc func(ctx context.Context, name string, args []string, onLine func(string)) error

func (f runnerFunc) Run(ctx context.Context, name string, args []string, onLine func(string)) error {
	return f(ctx, name, args, onLine)
}

func TestReferenceForResolve_WhenDigestRefItShouldReturnDigest(t *testing.T) {
	req := require.New(t)
	const dgst = "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	got, err := referenceForResolve("quay.io/team-a/os@" + dgst)
	req.NoError(err)
	req.Equal(dgst, got)
}

func TestPushLayoutAsReferrer_WhenDestHasSubjectItShouldPackWithSubject(t *testing.T) {
	req := require.New(t)
	ctx := context.Background()
	layoutDir := t.TempDir()
	destDir := t.TempDir()

	dest, err := ocistore.New(destDir)
	req.NoError(err)
	subjectPayload := []byte("os-layer")
	subjectLayer := ocispec.Descriptor{
		MediaType: ocispec.MediaTypeImageLayer,
		Digest:    digest.FromBytes(subjectPayload),
		Size:      int64(len(subjectPayload)),
	}
	const sourceDigest = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	req.NoError(dest.Push(ctx, subjectLayer, bytes.NewReader(subjectPayload)))
	subject, err := oras.PackManifest(ctx, dest, oras.PackManifestVersion1_1, ocispec.MediaTypeImageManifest, oras.PackManifestOptions{
		Layers: []ocispec.Descriptor{subjectLayer},
	})
	req.NoError(err)

	layout, err := ocistore.New(layoutDir)
	req.NoError(err)
	deltaPayload := []byte("delta-layer")
	deltaLayer := ocispec.Descriptor{
		MediaType: ocispec.MediaTypeImageLayer,
		Digest:    digest.FromBytes(deltaPayload),
		Size:      int64(len(deltaPayload)),
	}
	req.NoError(layout.Push(ctx, deltaLayer, bytes.NewReader(deltaPayload)))
	layoutManifest, err := oras.PackManifest(ctx, layout, oras.PackManifestVersion1_1, ociDeltaArtifactType, oras.PackManifestOptions{
		Subject: &subject,
		Layers:  []ocispec.Descriptor{deltaLayer},
		ManifestAnnotations: map[string]string{
			ociDeltaSourceAnnotation: sourceDigest,
		},
	})
	req.NoError(err)
	req.NoError(layout.Tag(ctx, layoutManifest, layoutTag))

	loaded, err := loadDeltaLayout(ctx, layoutDir)
	req.NoError(err)
	req.NoError(loaded.matchesPair(sourceDigest, subject.Digest.String()))
	packed, err := pushLayoutAsReferrer(ctx, loaded, dest, subject, dest)
	req.NoError(err)

	rc, err := dest.Fetch(ctx, packed)
	req.NoError(err)
	defer rc.Close()
	b, err := io.ReadAll(rc)
	req.NoError(err)
	var manifest ocispec.Manifest
	req.NoError(json.Unmarshal(b, &manifest))
	req.NotNil(manifest.Subject)
	req.Equal(subject.Digest, manifest.Subject.Digest)
	req.Equal(sourceDigest, manifest.Annotations[ociDeltaSourceAnnotation])
	req.Equal(ociDeltaArtifactType, manifest.ArtifactType)
}

func TestPushLayoutAsReferrer_WhenSubjectSrcDiffersFromDestItShouldPackReferrer(t *testing.T) {
	req := require.New(t)
	ctx := context.Background()
	layoutDir := t.TempDir()
	srcDir := t.TempDir()
	destDir := t.TempDir()

	src, err := ocistore.New(srcDir)
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

	dest, err := ocistore.New(destDir)
	req.NoError(err)

	layout, err := ocistore.New(layoutDir)
	req.NoError(err)
	deltaPayload := []byte("delta-layer")
	deltaLayer := ocispec.Descriptor{
		MediaType: ocispec.MediaTypeImageLayer,
		Digest:    digest.FromBytes(deltaPayload),
		Size:      int64(len(deltaPayload)),
	}
	req.NoError(layout.Push(ctx, deltaLayer, bytes.NewReader(deltaPayload)))
	layoutManifest, err := oras.PackManifest(ctx, layout, oras.PackManifestVersion1_1, ociDeltaArtifactType, oras.PackManifestOptions{
		Subject: &subject,
		Layers:  []ocispec.Descriptor{deltaLayer},
		ManifestAnnotations: map[string]string{
			ociDeltaSourceAnnotation: sourceDigest,
		},
	})
	req.NoError(err)
	req.NoError(layout.Tag(ctx, layoutManifest, layoutTag))

	loaded, err := loadDeltaLayout(ctx, layoutDir)
	req.NoError(err)
	req.NoError(loaded.matchesPair(sourceDigest, subject.Digest.String()))
	packed, err := pushLayoutAsReferrer(ctx, loaded, dest, subject, src)
	req.NoError(err)

	rc, err := dest.Fetch(ctx, packed)
	req.NoError(err)
	defer rc.Close()
	b, err := io.ReadAll(rc)
	req.NoError(err)
	var manifest ocispec.Manifest
	req.NoError(json.Unmarshal(b, &manifest))
	req.NotNil(manifest.Subject)
	req.Equal(subject.Digest, manifest.Subject.Digest)
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

func TestCopyOCILayout_WhenLayoutHasTaggedManifestItShouldCopyToDestination(t *testing.T) {
	req := require.New(t)
	ctx := context.Background()
	srcDir := t.TempDir()
	dstDir := t.TempDir()

	src, err := ocistore.New(srcDir)
	req.NoError(err)
	payload := []byte("delta-layer")
	layerDesc := ocispec.Descriptor{
		MediaType: ocispec.MediaTypeImageLayer,
		Digest:    digest.FromBytes(payload),
		Size:      int64(len(payload)),
	}
	req.NoError(src.Push(ctx, layerDesc, bytes.NewReader(payload)))
	manifestDesc, err := oras.PackManifest(ctx, src, oras.PackManifestVersion1_1, ociDeltaArtifactType, oras.PackManifestOptions{
		Layers: []ocispec.Descriptor{layerDesc},
	})
	req.NoError(err)
	req.NoError(src.Tag(ctx, manifestDesc, layoutTag))

	dst, err := ocistore.New(dstDir)
	req.NoError(err)
	desc, err := copyOCILayout(ctx, srcDir, dst)
	req.NoError(err)
	req.Equal(manifestDesc.Digest, desc.Digest)
	ok, err := dst.Exists(ctx, manifestDesc)
	req.NoError(err)
	req.True(ok)
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

func TestReadLayoutPayloadSize_WhenManifestHasConfigAndLayersItShouldSumSizes(t *testing.T) {
	req := require.New(t)
	dir := t.TempDir()
	blobDir := filepath.Join(dir, "blobs", "sha256")
	req.NoError(os.MkdirAll(blobDir, 0o755))

	manifest := ocispec.Manifest{
		Config: ocispec.Descriptor{Size: 5},
		Layers: []ocispec.Descriptor{{Size: 7}, {Size: 11}},
	}
	manifestBytes, err := json.Marshal(manifest)
	req.NoError(err)
	sum := sha256.Sum256(manifestBytes)
	hex := hex.EncodeToString(sum[:])
	req.NoError(os.WriteFile(filepath.Join(blobDir, hex), manifestBytes, 0o600))

	index := ocispec.Index{Manifests: []ocispec.Descriptor{{
		Digest: digest.NewDigestFromEncoded(digest.SHA256, hex),
	}}}
	indexBytes, err := json.Marshal(index)
	req.NoError(err)
	req.NoError(os.WriteFile(filepath.Join(dir, "index.json"), indexBytes, 0o600))

	size, err := readLayoutPayloadSize(dir)
	req.NoError(err)
	req.Equal(int64(23), size)
}
