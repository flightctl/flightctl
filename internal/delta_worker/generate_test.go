package delta_worker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
)

type recordingRunner struct {
	calls [][]string
	errAt map[string]error
}

func (r *recordingRunner) Run(_ context.Context, name string, args ...string) error {
	r.calls = append(r.calls, append([]string{name}, args...))
	if r.errAt != nil {
		if err, ok := r.errAt[name]; ok {
			return err
		}
	}
	return nil
}

func TestCreateAndPushDelta_WhenToolsSucceedItShouldPushReferrerSubjectTarget(t *testing.T) {
	req := require.New(t)
	runner := &recordingRunner{}
	g := generator{
		run: runner,
		layoutPayloadSize: func(string) (int64, error) {
			return 42, nil
		},
		workDir: t.TempDir(),
	}

	deltaRef, size, err := g.createAndPushDelta(context.Background(), "quay.io/team-a/os@sha256:src", "quay.io/team-a/os@sha256:tgt", "registry.example.com/deltas/os")
	req.NoError(err)
	req.Equal(int64(42), size)
	req.Equal("registry.example.com/deltas/os", deltaRef)

	req.GreaterOrEqual(len(runner.calls), 4)
	req.Equal("skopeo", runner.calls[0][0])
	req.Equal("copy", runner.calls[0][1])
	req.Contains(runner.calls[0], "docker://quay.io/team-a/os@sha256:src")
	req.Equal("skopeo", runner.calls[1][0])
	req.Contains(runner.calls[1], "docker://quay.io/team-a/os@sha256:tgt")
	req.Equal("oci-delta", runner.calls[2][0])
	req.Equal("create", runner.calls[2][1])
	req.Equal("oras", runner.calls[3][0])
	req.Equal("push", runner.calls[3][1])
	req.Contains(runner.calls[3], "--subject")
	req.Contains(runner.calls[3], "quay.io/team-a/os@sha256:tgt")
	req.Contains(runner.calls[3], ociDeltaArtifactType)
	req.NotContains(runner.calls[3], "apply")
}

func TestCreateAndPushDelta_WhenLayerDoesNotShrinkItShouldStillSucceed(t *testing.T) {
	req := require.New(t)
	g := generator{
		run: &recordingRunner{},
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
	g := generator{
		run: &recordingRunner{errAt: map[string]error{"oci-delta": errors.New("create failed")}},
		layoutPayloadSize: func(string) (int64, error) {
			return 0, nil
		},
		workDir: t.TempDir(),
	}

	_, _, err := g.createAndPushDelta(context.Background(), "quay.io/a@sha256:s", "quay.io/a@sha256:t", "reg.example/delta")
	req.Error(err)
}

func TestCreateAndPushDelta_WhenContextIsCancelledItShouldReturnError(t *testing.T) {
	req := require.New(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	g := generator{
		run: runnerFunc(func(ctx context.Context, _ string, _ ...string) error {
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
		run:               runner,
		layoutPayloadSize: func(dir string) (int64, error) { return 1, nil },
		workDir:           work,
	}

	_, _, err := g.createAndPushDelta(context.Background(), "quay.io/a@sha256:s", "quay.io/a@sha256:t", "reg.example/delta")
	req.NoError(err)
	req.Contains(runner.calls[0], "oci:"+filepath.Join(work, "source")+":img")
}

type runnerFunc func(ctx context.Context, name string, args ...string) error

func (f runnerFunc) Run(ctx context.Context, name string, args ...string) error {
	return f(ctx, name, args...)
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
	req.NoError(os.WriteFile(filepath.Join(blobDir, hex), manifestBytes, 0o644))

	index := ocispec.Index{Manifests: []ocispec.Descriptor{{
		Digest: digest.NewDigestFromEncoded(digest.SHA256, hex),
	}}}
	indexBytes, err := json.Marshal(index)
	req.NoError(err)
	req.NoError(os.WriteFile(filepath.Join(dir, "index.json"), indexBytes, 0o644))

	size, err := readLayoutPayloadSize(dir)
	req.NoError(err)
	req.Equal(int64(23), size)
}
