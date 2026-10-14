package applications

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/device/applications/helm"
	"github.com/flightctl/flightctl/internal/agent/device/applications/provider"
	"github.com/flightctl/flightctl/internal/agent/device/deltastatus"
	"github.com/flightctl/flightctl/internal/agent/device/dependency"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
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
	require := require.New(t)
	root := t.TempDir()
	rw := fileio.NewReadWriter(fileio.NewReader(fileio.WithReaderRootDir(root)), fileio.NewWriter(fileio.WithWriterRootDir(root)))
	logger := log.NewPrefixLogger("test")
	store := deltastatus.New(rw, "/var/lib/flightctl", logger)
	image := deltastatus.ApplicationImage{Image: "quay.io/acme/web:latest", Owner: "root", OCIType: string(dependency.OCITypePodmanImage), Digest: "sha256:old"}
	targetID := deltastatus.Fingerprint(image.Owner, image.Image, image.OCIType)
	require.NoError(store.ReconcileApplicationSpecs(map[string]string{"web": "spec"}))
	require.NoError(store.ReconcileApplicationTargets(map[string]map[string]string{"web": {targetID: "target"}}, true))
	require.NoError(store.RecordApplicationImageResult("web", "spec", targetID, "target", image, v1beta1.DeviceDeltaApplyStatus{Outcome: v1beta1.DeviceDeltaApplyOutcomeApplied}))
	volume, err := provider.NewVolumeManager(logger, "web", v1beta1.AppTypeContainer, "root", nil)
	require.NoError(err)
	app := &application{id: "web-id", imageRef: image.Image, requiredImages: []string{image.Image}, imageCollectionComplete: true, volume: volume, status: &v1beta1.DeviceApplicationStatus{Name: "web", RunAs: "root", AppType: v1beta1.AppTypeContainer}, workloads: []Workload{{Name: "web", Status: StatusRunning, Images: []WorkloadImage{{Image: image.Image, Digest: image.Digest}}}}}
	manager := &manager{log: logger, deltaStatusStore: store, ociTargetCache: provider.NewOCITargetCache(), podmanMonitor: &PodmanMonitor{apps: map[string]Application{app.ID(): app}}, kubernetesMonitor: &KubernetesMonitor{monitor: &monitor{apps: map[string]Application{}}}}
	reported := &v1beta1.DeviceStatus{}
	require.NoError(manager.Status(context.Background(), reported))
	require.Equal(image.Digest, (*reported.Applications[0].ImageDigests)[0].Digest)
	firstReport := reported.Applications[0]
	require.NotNil(reported.Applications[0].LastDelta)
	require.Equal(v1beta1.DeviceDeltaApplyOutcomeApplied, reported.Applications[0].LastDelta.Outcome)
	// A missing expected image must not be inferred from a flattened known set.
	app.requiredImages = append(app.requiredImages, "quay.io/acme/sidecar:latest")
	require.NoError(manager.Status(context.Background(), reported))
	require.Nil(reported.Applications[0].LastDelta)
	app.requiredImages = app.requiredImages[:1]
	app.workloads = append(app.workloads, Workload{Name: "unknown-replica", Status: StatusRunning, Images: []WorkloadImage{{Image: image.Image}}})
	require.NoError(manager.Status(context.Background(), reported))
	require.Nil(reported.Applications[0].LastDelta)
	// An installed provider observation must not replace an unknown running image.
	app.workloads = app.workloads[1:]
	app.imageDigest = "sha256:desired"
	require.NoError(manager.Status(context.Background(), reported))
	require.Nil(reported.Applications[0].LastDelta)
	require.Empty((*reported.Applications[0].ImageDigests)[0].Digest)
	require.Equal(image.Digest, (*firstReport.ImageDigests)[0].Digest)
	require.Equal(v1beta1.DeviceDeltaApplyOutcomeApplied, firstReport.LastDelta.Outcome)
}

func TestWhenHelmChartReferenceIsPresentItShouldReportWorkloadOutcome(t *testing.T) {
	require := require.New(t)
	const (
		appName       = "web"
		chart         = "oci://quay.io/acme/charts/web:1.0.0"
		workloadImage = "quay.io/acme/web:latest"
		digest        = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	)

	ctx := context.Background()
	logger := log.NewPrefixLogger("test")
	root := t.TempDir()
	rw := fileio.NewReadWriter(fileio.NewReader(fileio.WithReaderRootDir(root)), fileio.NewWriter(fileio.WithWriterRootDir(root)))
	store := deltastatus.New(rw, "/var/lib/flightctl", logger)
	image := deltastatus.ApplicationImage{
		Image: workloadImage, Owner: string(v1beta1.RootUsername), OCIType: string(dependency.OCITypeCRIImage), Digest: digest,
	}
	targetID := deltastatus.Fingerprint(image.Owner, image.Image, image.OCIType)
	require.NoError(store.ReconcileApplicationSpecs(map[string]string{appName: "spec"}))
	require.NoError(store.ReconcileApplicationTargets(map[string]map[string]string{appName: {targetID: "target"}}, true))
	require.NoError(store.RecordApplicationImageResult(appName, "spec", targetID, "target", image,
		v1beta1.DeviceDeltaApplyStatus{Outcome: v1beta1.DeviceDeltaApplyOutcomeApplied}))

	volume, err := provider.NewVolumeManager(logger, appName, v1beta1.AppTypeHelm, v1beta1.RootUsername, nil)
	require.NoError(err)
	appSpec := &provider.ApplicationSpec{
		ID: appName, Name: appName, AppType: v1beta1.AppTypeHelm, User: v1beta1.RootUsername,
		Image: chart, Volume: volume,
	}
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	appProvider := provider.NewMockProvider(ctrl)
	appProvider.EXPECT().EnsureDependencies(gomock.Any()).Return(nil)
	appProvider.EXPECT().Install(gomock.Any()).Return(nil)
	appProvider.EXPECT().Spec().Return(appSpec).AnyTimes()
	appProvider.EXPECT().ID().Return(appName).AnyTimes()

	targetCache := provider.NewOCITargetCache()
	targetCache.Set(provider.CacheEntry{
		Name:   appName,
		Parent: dependency.OCIPullTarget{Reference: chart},
		Children: []dependency.OCIPullTarget{{
			Type: dependency.OCITypeCRIImage, Reference: workloadImage, Digest: digest,
		}},
	})
	kubernetesMonitor := &KubernetesMonitor{monitor: newMonitor(logger, "kubernetes")}
	manager := &manager{
		log:               logger,
		deltaStatusStore:  store,
		ociTargetCache:    targetCache,
		podmanMonitor:     &PodmanMonitor{apps: make(map[string]Application)},
		kubernetesMonitor: kubernetesMonitor,
	}
	require.NoError(manager.Ensure(ctx, appProvider))

	event := kubernetesWatchEvent{Type: "ADDED"}
	event.Object.Kind = "Pod"
	event.Object.Metadata.Name = "web"
	event.Object.Metadata.UID = "web-pod"
	event.Object.Metadata.Labels = map[string]string{helm.AppLabelKey: appName}
	event.Object.Spec.Containers = []kubernetesContainerSpec{{Name: "web", Image: workloadImage}}
	event.Object.Status.ContainerStatuses = []kubernetesContainerStatus{{
		Name: "web", ImageID: "docker-pullable://quay.io/acme/web@" + digest, Ready: true,
	}}
	eventData, err := json.Marshal(event)
	require.NoError(err)
	kubernetesMonitor.HandleEvent(ctx, eventData)

	reported := &v1beta1.DeviceStatus{}
	require.NoError(manager.Status(ctx, reported))
	require.Len(reported.Applications, 1)
	require.Contains(*reported.Applications[0].ImageDigests, v1beta1.ApplicationImageDigest{Image: chart})
	require.Contains(*reported.Applications[0].ImageDigests, v1beta1.ApplicationImageDigest{Image: workloadImage, Digest: digest})
	require.NotNil(reported.Applications[0].LastDelta)
	require.Equal(v1beta1.DeviceDeltaApplyOutcomeApplied, reported.Applications[0].LastDelta.Outcome)
}

func TestWhenManySharedWorkloadImagesHaveAppliedOrNotRequiredResultsItShouldReportApplied(t *testing.T) {
	require := require.New(t)
	const appName = "web"

	store, _, logger := newApplicationOutcomeStore(t)
	images := make([]deltastatus.ApplicationImage, 12)
	for i := range images {
		images[i] = applicationOutcomeImage(
			fmt.Sprintf("quay.io/acme/workload-%d:latest", i),
			fmt.Sprintf("sha256:workload-%d", i),
		)
	}
	targets := prepareApplicationOutcomeTargets(t, store, appName, append(images, images[0]))
	for i, image := range images {
		outcome := v1beta1.DeviceDeltaApplyOutcomeApplied
		if i == 0 {
			outcome = v1beta1.DeviceDeltaApplyOutcomeNotRequired
		}
		recordApplicationOutcome(t, store, appName, targets, image, v1beta1.DeviceDeltaApplyStatus{Outcome: outcome})
	}

	manager, wantImageDigests := newApplicationOutcomeManager(t, appName, [][]deltastatus.ApplicationImage{
		images[:6],
		append([]deltastatus.ApplicationImage{images[0]}, images[6:]...),
	}, store, logger)
	reported := &v1beta1.DeviceStatus{}
	require.NoError(manager.Status(context.Background(), reported))
	require.Len(reported.Applications, 1)
	require.ElementsMatch(wantImageDigests, *reported.Applications[0].ImageDigests)
	require.NotNil(reported.Applications[0].LastDelta)
	require.Equal(v1beta1.DeviceDeltaApplyOutcomeApplied, reported.Applications[0].LastDelta.Outcome)
}

func TestWhenMixedWorkloadVersionsHaveDifferentOutcomesItShouldReportPartial(t *testing.T) {
	require := require.New(t)
	const (
		appName = "web"
		image   = "quay.io/acme/web:latest"
	)

	store, _, logger := newApplicationOutcomeStore(t)
	oldImage := applicationOutcomeImage(image, "sha256:old")
	newImage := applicationOutcomeImage(image, "sha256:new")
	targets := prepareApplicationOutcomeTargets(t, store, appName, []deltastatus.ApplicationImage{oldImage, newImage})
	recordApplicationOutcome(t, store, appName, targets, oldImage, v1beta1.DeviceDeltaApplyStatus{Outcome: v1beta1.DeviceDeltaApplyOutcomeApplied})
	reason := "full pull"
	recordApplicationOutcome(t, store, appName, targets, newImage, v1beta1.DeviceDeltaApplyStatus{
		Outcome:        v1beta1.DeviceDeltaApplyOutcomeFallback,
		FallbackReason: &reason,
	})

	manager, wantImageDigests := newApplicationOutcomeManager(t, appName, [][]deltastatus.ApplicationImage{{oldImage}, {newImage}}, store, logger)
	reported := &v1beta1.DeviceStatus{}
	require.NoError(manager.Status(context.Background(), reported))
	require.Len(reported.Applications, 1)
	require.ElementsMatch(wantImageDigests, *reported.Applications[0].ImageDigests)
	require.NotNil(reported.Applications[0].LastDelta)
	require.Equal(v1beta1.DeviceDeltaApplyOutcomePartial, reported.Applications[0].LastDelta.Outcome)
	require.Equal(reason, *reported.Applications[0].LastDelta.FallbackReason)
}

func TestWhenWorkloadOutcomeArrivesLateAndSurvivesRestartItShouldReportTheCompleteAggregate(t *testing.T) {
	require := require.New(t)
	const appName = "web"

	store, rw, logger := newApplicationOutcomeStore(t)
	firstImage := applicationOutcomeImage("quay.io/acme/web:latest", "sha256:web")
	secondImage := applicationOutcomeImage("quay.io/acme/sidecar:latest", "sha256:sidecar")
	images := []deltastatus.ApplicationImage{firstImage, secondImage}
	targets := prepareApplicationOutcomeTargets(t, store, appName, images)
	recordApplicationOutcome(t, store, appName, targets, firstImage, v1beta1.DeviceDeltaApplyStatus{Outcome: v1beta1.DeviceDeltaApplyOutcomeApplied})

	workloads := [][]deltastatus.ApplicationImage{{firstImage, secondImage}}
	manager, wantImageDigests := newApplicationOutcomeManager(t, appName, workloads, store, logger)
	reported := &v1beta1.DeviceStatus{}
	require.NoError(manager.Status(context.Background(), reported))
	require.ElementsMatch(wantImageDigests, *reported.Applications[0].ImageDigests)
	require.Nil(reported.Applications[0].LastDelta)

	reason := "full pull"
	recordApplicationOutcome(t, store, appName, targets, secondImage, v1beta1.DeviceDeltaApplyStatus{
		Outcome:        v1beta1.DeviceDeltaApplyOutcomeFallback,
		FallbackReason: &reason,
	})
	reported = &v1beta1.DeviceStatus{}
	require.NoError(manager.Status(context.Background(), reported))
	require.NotNil(reported.Applications[0].LastDelta)
	require.Equal(v1beta1.DeviceDeltaApplyOutcomePartial, reported.Applications[0].LastDelta.Outcome)
	require.Equal(reason, *reported.Applications[0].LastDelta.FallbackReason)

	restartedStore := deltastatus.New(rw, "/var/lib/flightctl", logger)
	restartedManager, _ := newApplicationOutcomeManager(t, appName, workloads, restartedStore, logger)
	reported = &v1beta1.DeviceStatus{}
	require.NoError(restartedManager.Status(context.Background(), reported))
	require.ElementsMatch(wantImageDigests, *reported.Applications[0].ImageDigests)
	require.NotNil(reported.Applications[0].LastDelta)
	require.Equal(v1beta1.DeviceDeltaApplyOutcomePartial, reported.Applications[0].LastDelta.Outcome)
	require.Equal(reason, *reported.Applications[0].LastDelta.FallbackReason)
}

func newApplicationOutcomeStore(t *testing.T) (*deltastatus.Store, fileio.ReadWriter, *log.PrefixLogger) {
	t.Helper()
	root := t.TempDir()
	rw := fileio.NewReadWriter(fileio.NewReader(fileio.WithReaderRootDir(root)), fileio.NewWriter(fileio.WithWriterRootDir(root)))
	logger := log.NewPrefixLogger("test")
	return deltastatus.New(rw, "/var/lib/flightctl", logger), rw, logger
}

func applicationOutcomeImage(image, digest string) deltastatus.ApplicationImage {
	return deltastatus.ApplicationImage{
		Image:   image,
		Owner:   string(v1beta1.RootUsername),
		OCIType: string(dependency.OCITypePodmanImage),
		Digest:  digest,
	}
}

func prepareApplicationOutcomeTargets(t *testing.T, store *deltastatus.Store, appName string, images []deltastatus.ApplicationImage) map[string]string {
	t.Helper()
	require := require.New(t)
	const specKey = "spec"
	require.NoError(store.ReconcileApplicationSpecs(map[string]string{appName: specKey}))
	targets := make(map[string]string, len(images))
	for _, image := range images {
		targetID := deltastatus.Fingerprint(image.Owner, image.Image, image.OCIType)
		targets[targetID] = "target-" + targetID
	}
	require.NoError(store.ReconcileApplicationTargets(map[string]map[string]string{appName: targets}, true))
	return targets
}

func recordApplicationOutcome(t *testing.T, store *deltastatus.Store, appName string, targets map[string]string, image deltastatus.ApplicationImage, result v1beta1.DeviceDeltaApplyStatus) {
	t.Helper()
	require := require.New(t)
	const specKey = "spec"
	targetID := deltastatus.Fingerprint(image.Owner, image.Image, image.OCIType)
	require.NoError(store.RecordApplicationImageResult(appName, specKey, targetID, targets[targetID], image, result))
}

func newApplicationOutcomeManager(t *testing.T, appName string, workloadImages [][]deltastatus.ApplicationImage, store *deltastatus.Store, logger *log.PrefixLogger) (*manager, []v1beta1.ApplicationImageDigest) {
	t.Helper()
	require := require.New(t)
	volume, err := provider.NewVolumeManager(logger, appName, v1beta1.AppTypeContainer, v1beta1.RootUsername, nil)
	require.NoError(err)
	workloads := make([]Workload, len(workloadImages))
	requiredImages := make(map[string]struct{})
	wantImageDigests := make(map[v1beta1.ApplicationImageDigest]struct{})
	for i, images := range workloadImages {
		workload := Workload{Name: fmt.Sprintf("workload-%d", i), Status: StatusRunning}
		for _, image := range images {
			workload.Images = append(workload.Images, WorkloadImage{Image: image.Image, Digest: image.Digest})
			requiredImages[image.Image] = struct{}{}
			wantImageDigests[v1beta1.ApplicationImageDigest{Image: image.Image, Digest: image.Digest}] = struct{}{}
		}
		workloads[i] = workload
	}
	app := &application{
		id:                      appName,
		requiredImages:          make([]string, 0, len(requiredImages)),
		imageCollectionComplete: true,
		workloads:               workloads,
		volume:                  volume,
		status:                  &v1beta1.DeviceApplicationStatus{Name: appName, RunAs: v1beta1.RootUsername, AppType: v1beta1.AppTypeContainer},
	}
	for image := range requiredImages {
		app.requiredImages = append(app.requiredImages, image)
	}
	want := make([]v1beta1.ApplicationImageDigest, 0, len(wantImageDigests))
	for imageDigest := range wantImageDigests {
		want = append(want, imageDigest)
	}
	manager := &manager{
		log:               logger,
		deltaStatusStore:  store,
		ociTargetCache:    provider.NewOCITargetCache(),
		podmanMonitor:     &PodmanMonitor{apps: map[string]Application{app.ID(): app}},
		kubernetesMonitor: &KubernetesMonitor{monitor: newMonitor(logger, "kubernetes")},
	}
	return manager, want
}
