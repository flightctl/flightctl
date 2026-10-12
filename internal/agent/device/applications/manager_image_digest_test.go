package applications

import (
	"context"
	"testing"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/device/applications/provider"
	"github.com/flightctl/flightctl/internal/agent/device/deltastatus"
	"github.com/flightctl/flightctl/internal/agent/device/dependency"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/stretchr/testify/require"
)

func TestAddCachedParentImageDigests(t *testing.T) {
	const (
		appID          = "app-id"
		parentImage    = "quay.io/example/parent:v1"
		parentDigest   = "sha256:parent"
		workloadImage  = "quay.io/example/workload:v1"
		workloadDigest = "sha256:workload"
	)

	imageDigests := func(digests ...v1beta1.ApplicationImageDigest) *[]v1beta1.ApplicationImageDigest {
		return &digests
	}
	cacheEntry := func(id, image, digest string) provider.CacheEntry {
		return provider.CacheEntry{
			Name: id,
			Parent: dependency.OCIPullTarget{
				Reference: image,
				Digest:    digest,
			},
		}
	}

	testCases := []struct {
		name         string
		result       AppStatusResult
		cacheEntries []provider.CacheEntry
		want         []v1beta1.ApplicationImageDigest
	}{
		{
			name: "When the status list is nil, it should include the cached parent image",
			result: AppStatusResult{
				ID:     appID,
				Status: v1beta1.DeviceApplicationStatus{Name: "parent-app"},
			},
			cacheEntries: []provider.CacheEntry{cacheEntry(appID, parentImage, parentDigest)},
			want:         []v1beta1.ApplicationImageDigest{{Image: parentImage, Digest: parentDigest}},
		},
		{
			name: "When the parent ref is absent, it should append the cached parent image",
			result: AppStatusResult{
				ID: appID,
				Status: v1beta1.DeviceApplicationStatus{
					Name:         "parent-app",
					ImageDigests: imageDigests(v1beta1.ApplicationImageDigest{Image: workloadImage, Digest: workloadDigest}),
				},
			},
			cacheEntries: []provider.CacheEntry{cacheEntry(appID, parentImage, parentDigest)},
			want: []v1beta1.ApplicationImageDigest{
				{Image: parentImage, Digest: parentDigest},
				{Image: workloadImage, Digest: workloadDigest},
			},
		},
		{
			name: "When the parent digest is empty, it should fill the cached digest",
			result: AppStatusResult{
				ID: appID,
				Status: v1beta1.DeviceApplicationStatus{
					Name:         "parent-app",
					ImageDigests: imageDigests(v1beta1.ApplicationImageDigest{Image: parentImage}),
				},
			},
			cacheEntries: []provider.CacheEntry{cacheEntry(appID, parentImage, parentDigest)},
			want:         []v1beta1.ApplicationImageDigest{{Image: parentImage, Digest: parentDigest}},
		},
		{
			name: "When the cached digest is empty, it should leave other status entries unchanged",
			result: AppStatusResult{
				ID: appID,
				Status: v1beta1.DeviceApplicationStatus{
					Name:         "parent-app",
					ImageDigests: imageDigests(v1beta1.ApplicationImageDigest{Image: workloadImage, Digest: workloadDigest}),
				},
			},
			cacheEntries: []provider.CacheEntry{cacheEntry(appID, parentImage, "")},
			want:         []v1beta1.ApplicationImageDigest{{Image: workloadImage, Digest: workloadDigest}},
		},
		{
			name: "When result and display names differ, it should match cache by application ID",
			result: AppStatusResult{
				ID: appID,
				Status: v1beta1.DeviceApplicationStatus{
					Name: "parent-app",
				},
			},
			cacheEntries: []provider.CacheEntry{
				cacheEntry(appID, parentImage, parentDigest),
				cacheEntry("parent-app", "quay.io/example/wrong:v1", "sha256:wrong"),
			},
			want: []v1beta1.ApplicationImageDigest{{Image: parentImage, Digest: parentDigest}},
		},
		{
			name: "When the workload reports a newer parent digest it should preserve the runtime observation",
			result: AppStatusResult{ID: appID, Status: v1beta1.DeviceApplicationStatus{
				ImageDigests: imageDigests(v1beta1.ApplicationImageDigest{Image: parentImage, Digest: workloadDigest}),
			}},
			cacheEntries: []provider.CacheEntry{cacheEntry(appID, parentImage, parentDigest)},
			want:         []v1beta1.ApplicationImageDigest{{Image: parentImage, Digest: workloadDigest}},
		},
		{
			name: "When the cached parent pair is already present, it should not duplicate it",
			result: AppStatusResult{
				ID: appID,
				Status: v1beta1.DeviceApplicationStatus{
					Name:         "parent-app",
					ImageDigests: imageDigests(v1beta1.ApplicationImageDigest{Image: parentImage, Digest: parentDigest}),
				},
			},
			cacheEntries: []provider.CacheEntry{cacheEntry(appID, parentImage, parentDigest)},
			want:         []v1beta1.ApplicationImageDigest{{Image: parentImage, Digest: parentDigest}},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			cache := provider.NewOCITargetCache()
			for _, entry := range tc.cacheEntries {
				cache.Set(entry)
			}

			results := []AppStatusResult{tc.result}
			addCachedParentImageDigests(results, cache)

			require.NotNil(t, results[0].Status.ImageDigests)
			require.Equal(t, tc.want, *results[0].Status.ImageDigests)
		})
	}
}

func TestManagerReportsMatchingWorkloadOutcome(t *testing.T) {
	root := t.TempDir()
	rw := fileio.NewReadWriter(fileio.NewReader(fileio.WithReaderRootDir(root)), fileio.NewWriter(fileio.WithWriterRootDir(root)))
	logger := log.NewPrefixLogger("test")
	store := deltastatus.New(rw, "/var/lib/flightctl", logger)
	image := deltastatus.ApplicationImage{Image: "quay.io/acme/web:latest", Owner: "root", OCIType: string(dependency.OCITypePodmanImage), Digest: "sha256:old"}
	targetID := deltastatus.Fingerprint(image.Owner, image.Image, image.OCIType)
	require.NoError(t, store.ReconcileApplicationSpecs(map[string]string{"web": "spec"}))
	require.NoError(t, store.ReconcileApplicationTargets(map[string]map[string]string{"web": {targetID: "target"}}, true))
	require.NoError(t, store.RecordApplicationImageResult("web", "spec", targetID, "target", image, v1beta1.DeviceDeltaApplyStatus{Outcome: v1beta1.DeviceDeltaApplyOutcomeApplied}))
	volume, err := provider.NewVolumeManager(logger, "web", v1beta1.AppTypeContainer, "root", nil)
	require.NoError(t, err)
	app := &application{id: "web-id", imageRef: image.Image, requiredImages: []string{image.Image}, imageCollectionComplete: true, volume: volume, status: &v1beta1.DeviceApplicationStatus{Name: "web", RunAs: "root", AppType: v1beta1.AppTypeContainer}, workloads: []Workload{{Name: "web", Status: StatusRunning, Images: []WorkloadImage{{Image: image.Image, Digest: image.Digest}}}}}
	manager := &manager{log: logger, deltaStatusStore: store, ociTargetCache: provider.NewOCITargetCache(), podmanMonitor: &PodmanMonitor{apps: map[string]Application{app.ID(): app}}, kubernetesMonitor: &KubernetesMonitor{monitor: &monitor{apps: map[string]Application{}}}}
	reported := &v1beta1.DeviceStatus{}
	require.NoError(t, manager.Status(context.Background(), reported))
	require.Equal(t, image.Digest, (*reported.Applications[0].ImageDigests)[0].Digest)
	firstReport := reported.Applications[0]
	require.NotNil(t, reported.Applications[0].LastDelta)
	require.Equal(t, v1beta1.DeviceDeltaApplyOutcomeApplied, reported.Applications[0].LastDelta.Outcome)
	// A missing expected image must not be inferred from a flattened known set.
	app.requiredImages = append(app.requiredImages, "quay.io/acme/sidecar:latest")
	require.NoError(t, manager.Status(context.Background(), reported))
	require.Nil(t, reported.Applications[0].LastDelta)
	app.requiredImages = app.requiredImages[:1]
	app.workloads = append(app.workloads, Workload{Name: "unknown-replica", Status: StatusRunning, Images: []WorkloadImage{{Image: image.Image}}})
	require.NoError(t, manager.Status(context.Background(), reported))
	require.Nil(t, reported.Applications[0].LastDelta)
	// An installed provider observation must not replace an unknown running image.
	app.workloads = app.workloads[1:]
	app.imageDigest = "sha256:desired"
	require.NoError(t, manager.Status(context.Background(), reported))
	require.Nil(t, reported.Applications[0].LastDelta)
	require.Empty(t, (*reported.Applications[0].ImageDigests)[0].Digest)
	require.Equal(t, image.Digest, (*firstReport.ImageDigests)[0].Digest)
	require.Equal(t, v1beta1.DeviceDeltaApplyOutcomeApplied, firstReport.LastDelta.Outcome)
}
