package tasks

import (
	"context"
	"errors"
	"sync"
	"testing"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store/delta"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

type stubGenerationLookup struct {
	mu    sync.Mutex
	calls int
	gen   *model.DeltaGeneration
	err   error
}

func (s *stubGenerationLookup) GetGeneration(_ context.Context, _ delta.GenerationKey, _ ...delta.GenerationGetOption) (*model.DeltaGeneration, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	return s.gen, nil
}

func (s *stubGenerationLookup) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func testGenerationKey() delta.GenerationKey {
	return delta.GenerationKey{
		OrgID:           uuid.MustParse("11111111-1111-1111-1111-111111111111"),
		ImageRepository: "quay.io/acme/os",
		SourceDigest:    "sha256:aaa",
		TargetDigest:    "sha256:bbb",
	}
}

func TestImageRepositoryFromRef(t *testing.T) {
	tests := []struct {
		name    string
		ref     string
		want    string
		wantErr bool
	}{
		{name: "When the ref is a tag it should strip the tag", ref: "quay.io/acme/os:latest", want: "quay.io/acme/os"},
		{name: "When the ref is digested it should strip the digest", ref: "quay.io/acme/os@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", want: "quay.io/acme/os"},
		{name: "When the ref is empty it should return an error", ref: "", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ImageRepositoryFromRef(tt.ref)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestHintFromGeneration(t *testing.T) {
	deltaRef := "quay.io/acme/os@sha256:delta"
	size := int64(47185920)
	full := int64(1 << 30)

	t.Run("When generation succeeded it should hint deltaRef and IEC size_bytes", func(t *testing.T) {
		img, sz := hintFromGeneration(&model.DeltaGeneration{
			Status:    model.DeltaGenerationSucceeded,
			DeltaRef:  &deltaRef,
			SizeBytes: &size,
		}, nil)
		require.Equal(t, &deltaRef, img)
		require.Equal(t, lo.ToPtr("45 MiB"), sz)
	})

	t.Run("When generation is rejected it should not hint and should use size_bytes", func(t *testing.T) {
		img, sz := hintFromGeneration(&model.DeltaGeneration{
			Status:    model.DeltaGenerationRejected,
			SizeBytes: &size,
		}, nil)
		require.Nil(t, img)
		require.Equal(t, lo.ToPtr("45 MiB"), sz)
	})

	t.Run("When generation is missing it should not hint and should use fallback size", func(t *testing.T) {
		img, sz := hintFromGeneration(nil, &full)
		require.Nil(t, img)
		require.Equal(t, lo.ToPtr("1 GiB"), sz)
	})

	t.Run("When generation failed without size_bytes it should use fallback size", func(t *testing.T) {
		img, sz := hintFromGeneration(&model.DeltaGeneration{Status: model.DeltaGenerationFailed}, &full)
		require.Nil(t, img)
		require.Equal(t, lo.ToPtr("1 GiB"), sz)
	})
}

func TestFormatIECBytes(t *testing.T) {
	tests := []struct {
		name string
		n    int64
		want string
	}{
		{name: "When the size is zero it should report 0 KiB", n: 0, want: "0 KiB"},
		{name: "When the size is negative it should report 0 KiB", n: -1, want: "0 KiB"},
		{name: "When the size is below one KiB it should report 1 KiB", n: 1, want: "1 KiB"},
		{name: "When the size is exactly 1024 it should report 1 KiB", n: 1024, want: "1 KiB"},
		{name: "When the size is 45 MiB it should report 45 MiB", n: 47185920, want: "45 MiB"},
		{name: "When the size is 1 GiB it should report 1 GiB", n: 1 << 30, want: "1 GiB"},
		{name: "When the size is 1 TiB it should report 1 TiB", n: 1 << 40, want: "1 TiB"},
		{name: "When the size is fractional it should report one decimal", n: 257200538, want: "245.3 MiB"},
		{name: "When the size is 1.5 GiB it should report 1.5 GiB", n: 1610612736, want: "1.5 GiB"},
		{name: "When the size is 500 KiB it should report 500 KiB", n: 512000, want: "500 KiB"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, FormatIECBytes(tt.n))
		})
	}
}

func TestLookupCachedGeneration(t *testing.T) {
	ctx := context.Background()
	key := testGenerationKey()
	row := &model.DeltaGeneration{
		OrgID:           key.OrgID,
		ImageRepository: key.ImageRepository,
		SourceDigest:    key.SourceDigest,
		TargetDigest:    key.TargetDigest,
		Status:          model.DeltaGenerationSucceeded,
		DeltaRef:        lo.ToPtr("quay.io/acme/os@sha256:delta"),
		SizeBytes:       lo.ToPtr(int64(47185920)),
	}

	t.Run("When the cache is empty it should load from the store and return the row", func(t *testing.T) {
		kv := newTestKVStore()
		store := &stubGenerationLookup{gen: row}

		got, err := lookupCachedGeneration(ctx, kv, store, key, "")
		require.NoError(t, err)
		require.Equal(t, row, got)
		require.Equal(t, 1, store.callCount())
		require.True(t, kv.has(generationMemoKey(key, "")))
	})

	t.Run("When the cache is populated it should not query the store again", func(t *testing.T) {
		kv := newTestKVStore()
		store := &stubGenerationLookup{gen: row}

		_, err := lookupCachedGeneration(ctx, kv, store, key, "")
		require.NoError(t, err)
		got, err := lookupCachedGeneration(ctx, kv, store, key, "")
		require.NoError(t, err)
		require.Equal(t, row.Status, got.Status)
		require.Equal(t, row.DeltaRef, got.DeltaRef)
		require.Equal(t, row.SizeBytes, got.SizeBytes)
		require.Equal(t, 1, store.callCount())
	})

	t.Run("When the store has no row it should cache the miss", func(t *testing.T) {
		kv := newTestKVStore()
		store := &stubGenerationLookup{err: flterrors.ErrResourceNotFound}

		got, err := lookupCachedGeneration(ctx, kv, store, key, "")
		require.NoError(t, err)
		require.Nil(t, got)
		got, err = lookupCachedGeneration(ctx, kv, store, key, "")
		require.NoError(t, err)
		require.Nil(t, got)
		require.Equal(t, 1, store.callCount())
	})

	t.Run("When the store fails it should not cache the error", func(t *testing.T) {
		kv := newTestKVStore()
		store := &stubGenerationLookup{err: errors.New("db down")}

		_, err := lookupCachedGeneration(ctx, kv, store, key, "")
		require.Error(t, err)
		_, err = lookupCachedGeneration(ctx, kv, store, key, "")
		require.Error(t, err)
		require.Equal(t, 2, store.callCount())
	})
}

// ---------------------------------------------------------------------------
// App delta hint tests
// ---------------------------------------------------------------------------

type mapGenerationLookup struct {
	mu    sync.Mutex
	gens  map[string]*model.DeltaGeneration
	calls int
}

func (m *mapGenerationLookup) GetGeneration(_ context.Context, key delta.GenerationKey, _ ...delta.GenerationGetOption) (*model.DeltaGeneration, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	k := key.SourceDigest + "→" + key.TargetDigest
	gen, ok := m.gens[k]
	if !ok {
		return nil, flterrors.ErrResourceNotFound
	}
	return gen, nil
}

func staticDigestLookup(digests map[string]string) func(context.Context, string) (string, error) {
	return func(_ context.Context, imageRef string) (string, error) {
		d, ok := digests[imageRef]
		if !ok {
			return "", errors.New("unknown image: " + imageRef)
		}
		return d, nil
	}
}

func newTestResolver(gens map[string]*model.DeltaGeneration, digests map[string]string) *appDeltaResolver {
	return &appDeltaResolver{
		log:           logrus.New(),
		orgID:         uuid.MustParse("11111111-1111-1111-1111-111111111111"),
		deltaLookup:   &mapGenerationLookup{gens: gens},
		kvStore:       newTestKVStore(),
		resolveDigest: staticDigestLookup(digests),
	}
}

func makeDeltaContainerApp(t *testing.T, name, image string, volumeImages ...string) domain.ApplicationProviderSpec {
	t.Helper()
	var app domain.ApplicationProviderSpec
	var volumes []api.ApplicationVolume
	for i, vi := range volumeImages {
		vol := api.ApplicationVolume{Name: "vol-" + string(rune('a'+i))}
		require.NoError(t, vol.FromImageVolumeProviderSpec(api.ImageVolumeProviderSpec{
			Image: api.ImageVolumeSource{Reference: vi},
		}))
		volumes = append(volumes, vol)
	}
	container := api.ContainerApplication{
		Name:    lo.ToPtr(name),
		AppType: api.AppTypeContainer,
		Volumes: lo.Ternary(len(volumes) > 0, &volumes, nil),
	}
	require.NoError(t, container.FromImageApplicationProviderSpec(api.ImageSpec{Image: image}))
	require.NoError(t, app.FromContainerApplication(container))
	return app
}

func makeDeltaComposeApp(t *testing.T, name, image string, volumeImages ...string) domain.ApplicationProviderSpec {
	t.Helper()
	var app domain.ApplicationProviderSpec
	var volumes []api.ApplicationVolume
	for i, vi := range volumeImages {
		vol := api.ApplicationVolume{Name: "vol-" + string(rune('a'+i))}
		require.NoError(t, vol.FromImageVolumeProviderSpec(api.ImageVolumeProviderSpec{
			Image: api.ImageVolumeSource{Reference: vi},
		}))
		volumes = append(volumes, vol)
	}
	compose := api.ComposeApplication{
		Name:    lo.ToPtr(name),
		AppType: api.AppTypeCompose,
		Volumes: lo.Ternary(len(volumes) > 0, &volumes, nil),
	}
	require.NoError(t, compose.FromImageApplicationProviderSpec(api.ImageSpec{Image: image}))
	require.NoError(t, app.FromComposeApplication(compose))
	return app
}

func makeDeltaQuadletApp(t *testing.T, name, image string) domain.ApplicationProviderSpec {
	t.Helper()
	var app domain.ApplicationProviderSpec
	quadlet := api.QuadletApplication{Name: lo.ToPtr(name), AppType: api.AppTypeQuadlet}
	require.NoError(t, quadlet.FromImageApplicationProviderSpec(api.ImageSpec{Image: image}))
	require.NoError(t, app.FromQuadletApplication(quadlet))
	return app
}

func makeDeltaHelmApp(t *testing.T, name, image string) domain.ApplicationProviderSpec {
	t.Helper()
	var app domain.ApplicationProviderSpec
	helm := api.HelmApplication{Name: lo.ToPtr(name), AppType: api.AppTypeHelm}
	require.NoError(t, helm.FromImageApplicationProviderSpec(api.ImageSpec{Image: image}))
	require.NoError(t, app.FromHelmApplication(helm))
	return app
}

func makeDeviceWithAppDigests(appName string, digests map[string]string) *domain.Device {
	var imageDigests []api.ApplicationImageDigest
	for img, dig := range digests {
		imageDigests = append(imageDigests, api.ApplicationImageDigest{Image: img, Digest: dig})
	}
	status := domain.NewDeviceStatus()
	status.Applications = []domain.DeviceApplicationStatus{
		{Name: appName, ImageDigests: &imageDigests},
	}
	return &domain.Device{Status: &status}
}

func TestCollectContainerAppPairs(t *testing.T) {
	t.Run("When the container has an image and volumes it should return parent and nested pairs", func(t *testing.T) {
		app := makeDeltaContainerApp(t, "web", "quay.io/acme/web:v2", "quay.io/acme/data:v1")
		container, err := app.AsContainerApplication()
		require.NoError(t, err)
		digests := map[string]string{
			"quay.io/acme/web:v2":  "sha256:aaa",
			"quay.io/acme/data:v1": "sha256:bbb",
		}
		parent, nested := collectContainerAppPairs(container, digests)
		require.NotNil(t, parent)
		require.Equal(t, "quay.io/acme/web:v2", parent.imageRef)
		require.Equal(t, "sha256:aaa", parent.currentDigest)
		require.Len(t, nested, 1)
		require.Equal(t, "quay.io/acme/data:v1", nested[0].imageRef)
	})
}

func TestCollectComposeAppPairs(t *testing.T) {
	t.Run("When the compose has an image provider it should return parent and nested pairs", func(t *testing.T) {
		app := makeDeltaComposeApp(t, "stack", "quay.io/acme/compose:v3", "quay.io/acme/vol:v1")
		compose, err := app.AsComposeApplication()
		require.NoError(t, err)
		digests := map[string]string{
			"quay.io/acme/compose:v3": "sha256:ccc",
			"quay.io/acme/vol:v1":     "sha256:ddd",
		}
		parent, nested := collectComposeAppPairs(compose, digests)
		require.NotNil(t, parent)
		require.Equal(t, "quay.io/acme/compose:v3", parent.imageRef)
		require.Len(t, nested, 1)
	})
}

func TestCollectQuadletAppPairs(t *testing.T) {
	t.Run("When the quadlet has an image provider it should return a parent pair", func(t *testing.T) {
		app := makeDeltaQuadletApp(t, "svc", "quay.io/acme/svc:v1")
		quadlet, err := app.AsQuadletApplication()
		require.NoError(t, err)
		digests := map[string]string{"quay.io/acme/svc:v1": "sha256:eee"}
		parent, nested := collectQuadletAppPairs(quadlet, digests)
		require.NotNil(t, parent)
		require.Empty(t, nested)
	})
}

func TestCollectHelmAppPairs(t *testing.T) {
	t.Run("When the helm app has a chart image it should return a parent pair", func(t *testing.T) {
		app := makeDeltaHelmApp(t, "chart", "quay.io/acme/chart:v1")
		helm, err := app.AsHelmApplication()
		require.NoError(t, err)
		digests := map[string]string{"quay.io/acme/chart:v1": "sha256:fff"}
		parent, nested := collectHelmAppPairs(helm, digests)
		require.NotNil(t, parent)
		require.Empty(t, nested)
	})
}

func TestResolveApp(t *testing.T) {
	ctx := context.Background()
	deltaSize := int64(5 * 1024 * 1024)
	deltaRef := "quay.io/acme/web@sha256:delta123"

	t.Run("When delta generation succeeded it should set parent delta and size", func(t *testing.T) {
		resolver := newTestResolver(
			map[string]*model.DeltaGeneration{
				"sha256:old→sha256:new": {Status: model.DeltaGenerationSucceeded, DeltaRef: lo.ToPtr(deltaRef), SizeBytes: &deltaSize},
			},
			map[string]string{"quay.io/acme/web:v2": "sha256:new"},
		)
		parent := &appImagePair{imageRef: "quay.io/acme/web:v2", currentDigest: "sha256:old"}
		hints := resolver.resolveApp(ctx, parent, nil)
		require.NotNil(t, hints)
		require.Equal(t, &deltaRef, hints.parentDelta)
		require.Equal(t, lo.ToPtr("5 MiB"), hints.totalSize)
	})

	t.Run("When delta generation was rejected it should have size but no hint", func(t *testing.T) {
		resolver := newTestResolver(
			map[string]*model.DeltaGeneration{
				"sha256:old→sha256:new": {Status: model.DeltaGenerationRejected, SizeBytes: &deltaSize},
			},
			map[string]string{"quay.io/acme/web:v2": "sha256:new"},
		)
		parent := &appImagePair{imageRef: "quay.io/acme/web:v2", currentDigest: "sha256:old"}
		hints := resolver.resolveApp(ctx, parent, nil)
		require.NotNil(t, hints)
		require.Nil(t, hints.parentDelta)
		require.Equal(t, lo.ToPtr("5 MiB"), hints.totalSize)
	})

	t.Run("When no delta generation record exists it should return nil", func(t *testing.T) {
		resolver := newTestResolver(map[string]*model.DeltaGeneration{}, map[string]string{"quay.io/acme/web:v2": "sha256:new"})
		parent := &appImagePair{imageRef: "quay.io/acme/web:v2", currentDigest: "sha256:old"}
		hints := resolver.resolveApp(ctx, parent, nil)
		require.Nil(t, hints)
	})

	t.Run("When current and target digests are the same it should return nil", func(t *testing.T) {
		resolver := newTestResolver(map[string]*model.DeltaGeneration{}, map[string]string{"quay.io/acme/web:v2": "sha256:same"})
		parent := &appImagePair{imageRef: "quay.io/acme/web:v2", currentDigest: "sha256:same"}
		hints := resolver.resolveApp(ctx, parent, nil)
		require.Nil(t, hints)
	})

	t.Run("When parent and nested both have deltas it should accumulate size and set nested hints", func(t *testing.T) {
		parentSize := int64(10 * 1024 * 1024)
		nestedSize := int64(3 * 1024 * 1024)
		nestedRef := "quay.io/acme/vol@sha256:deltanested"
		resolver := newTestResolver(
			map[string]*model.DeltaGeneration{
				"sha256:oldp→sha256:newp": {Status: model.DeltaGenerationSucceeded, DeltaRef: lo.ToPtr(deltaRef), SizeBytes: &parentSize},
				"sha256:oldn→sha256:newn": {Status: model.DeltaGenerationSucceeded, DeltaRef: lo.ToPtr(nestedRef), SizeBytes: &nestedSize},
			},
			map[string]string{"quay.io/acme/web:v2": "sha256:newp", "quay.io/acme/vol:v1": "sha256:newn"},
		)
		parent := &appImagePair{imageRef: "quay.io/acme/web:v2", currentDigest: "sha256:oldp"}
		nested := []appImagePair{{imageRef: "quay.io/acme/vol:v1", currentDigest: "sha256:oldn"}}
		hints := resolver.resolveApp(ctx, parent, nested)
		require.NotNil(t, hints)
		require.Equal(t, &deltaRef, hints.parentDelta)
		require.Len(t, hints.nestedDeltas, 1)
		require.Equal(t, "sha256:newn", hints.nestedDeltas[0].TargetDigest)
		require.Equal(t, nestedRef, hints.nestedDeltas[0].DeltaImage)
		require.Equal(t, lo.ToPtr("13 MiB"), hints.totalSize)
	})
}

func TestCollectCurrentDigests(t *testing.T) {
	t.Run("When the device has image digests for the app it should return the map", func(t *testing.T) {
		device := makeDeviceWithAppDigests("myapp", map[string]string{"quay.io/acme/web:v2": "sha256:aaa"})
		m := collectCurrentDigests(device, "myapp")
		require.Equal(t, "sha256:aaa", m["quay.io/acme/web:v2"])
	})

	t.Run("When the device has no matching app it should return nil", func(t *testing.T) {
		device := makeDeviceWithAppDigests("other", map[string]string{"quay.io/acme/web:v2": "sha256:aaa"})
		require.Nil(t, collectCurrentDigests(device, "myapp"))
	})

	t.Run("When device is nil it should return nil", func(t *testing.T) {
		require.Nil(t, collectCurrentDigests(nil, "myapp"))
	})
}

func TestApplyDeltaHintsToImageSpec(t *testing.T) {
	deltaRef := "quay.io/acme/web@sha256:delta"
	t.Run("When hints contain parent delta and nested deltas it should set both on the spec", func(t *testing.T) {
		spec := api.ImageSpec{Image: "quay.io/acme/web:v2"}
		hints := &appDeltaHints{
			parentDelta:  &deltaRef,
			nestedDeltas: []api.ImageDeltaHint{{TargetDigest: "sha256:nested", DeltaImage: "quay.io/acme/vol@sha256:deltanested"}},
		}
		result := applyDeltaHintsToImageSpec(spec, hints)
		require.Equal(t, &deltaRef, result.DeltaImage)
		require.NotNil(t, result.DeltaImages)
		require.Len(t, *result.DeltaImages, 1)
	})

	t.Run("When hints are nil it should return spec unchanged", func(t *testing.T) {
		spec := api.ImageSpec{Image: "quay.io/acme/web:v2"}
		result := applyDeltaHintsToImageSpec(spec, nil)
		require.Nil(t, result.DeltaImage)
		require.Nil(t, result.DeltaImages)
	})
}

func TestAppNameFromProvider(t *testing.T) {
	t.Run("When the app is a container it should return the name", func(t *testing.T) {
		app := makeDeltaContainerApp(t, "web", "quay.io/acme/web:v2")
		require.Equal(t, "web", appNameFromProvider(&app))
	})
	t.Run("When the app is a compose it should return the name", func(t *testing.T) {
		app := makeDeltaComposeApp(t, "stack", "quay.io/acme/compose:v3")
		require.Equal(t, "stack", appNameFromProvider(&app))
	})
	t.Run("When the app is a quadlet it should return the name", func(t *testing.T) {
		app := makeDeltaQuadletApp(t, "svc", "quay.io/acme/svc:v1")
		require.Equal(t, "svc", appNameFromProvider(&app))
	})
	t.Run("When the app is a helm it should return the name", func(t *testing.T) {
		app := makeDeltaHelmApp(t, "chart", "quay.io/acme/chart:v1")
		require.Equal(t, "chart", appNameFromProvider(&app))
	})
}
