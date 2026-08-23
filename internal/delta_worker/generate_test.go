package delta_worker

import (
	"bytes"
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
	"oras.land/oras-go/v2"
	ocistore "oras.land/oras-go/v2/content/oci"
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

func TestCreateAndPushDelta_WhenToolsSucceedItShouldPushLayoutNotOrasCLI(t *testing.T) {
	req := require.New(t)
	runner := &recordingRunner{}
	var pushedDir, pushedDest string
	g := generator{
		run: runner,
		pushLayout: func(_ context.Context, layoutDir, destRef string) (string, error) {
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

	req.Len(runner.calls, 3)
	req.Equal("skopeo", runner.calls[0][0])
	req.Equal("copy", runner.calls[0][1])
	req.Contains(runner.calls[0], "docker://quay.io/team-a/os@sha256:src")
	req.Equal("skopeo", runner.calls[1][0])
	req.Contains(runner.calls[1], "docker://quay.io/team-a/os@sha256:tgt")
	req.Equal("oci-delta", runner.calls[2][0])
	req.Equal("create", runner.calls[2][1])
	req.NotEqual("oras", runner.calls[0][0])
}

func TestCreateAndPushDelta_WhenLayerDoesNotShrinkItShouldStillSucceed(t *testing.T) {
	req := require.New(t)
	g := generator{
		run: &recordingRunner{},
		pushLayout: func(_ context.Context, _, destRef string) (string, error) {
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
		run: &recordingRunner{errAt: map[string]error{"oci-delta": errors.New("create failed")}},
		pushLayout: func(_ context.Context, _, destRef string) (string, error) {
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
		run: &recordingRunner{},
		pushLayout: func(context.Context, string, string) (string, error) {
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
		run: runner,
		pushLayout: func(_ context.Context, _, destRef string) (string, error) {
			return destRef, nil
		},
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

func TestPushOCILayout_WhenWriteSpecIsMissingItShouldReturnError(t *testing.T) {
	req := require.New(t)
	_, err := pushOCILayout(context.Background(), nil, t.TempDir(), "registry.example.com/os")
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
