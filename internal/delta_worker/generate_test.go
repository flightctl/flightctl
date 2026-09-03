package delta_worker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
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

type runnerFunc func(ctx context.Context, name string, args ...string) error

func (f runnerFunc) Run(ctx context.Context, name string, args ...string) error {
	return f(ctx, name, args...)
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
	packed, err := pushLayoutAsReferrer(ctx, loaded, dest, subject)
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
