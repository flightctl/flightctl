package applications

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/applications/helm"
	"github.com/flightctl/flightctl/internal/agent/device/applications/provider"
	"github.com/flightctl/flightctl/internal/agent/device/deltastatus"
	"github.com/flightctl/flightctl/internal/agent/device/dependency"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/poll"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestPodmanImageVolumeObservationRequiresRuntimeDigestBinding(t *testing.T) {
	const (
		applicationID = "app-id"
		lifetime      = "lifetime-1"
		reference     = "quay.io/example/volume:v1"
		owner         = v1beta1.RootUsername
		imageID       = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
		digestValue   = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	)

	testCases := []struct {
		name        string
		runtimeOut  string
		runtimeErr  int
		label       string
		malformedID bool
		want        bool
	}{
		{
			name:       "When the source image ID resolves to the labeled digest it should accept the observation",
			runtimeOut: digestValue,
			label:      digestValue,
			want:       true,
		},
		{
			name:       "When the source image ID resolves to a different digest it should reject the observation",
			runtimeOut: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			label:      digestValue,
		},
		{
			name:       "When the source image ID cannot be inspected it should reject the observation",
			runtimeErr: 125,
			label:      digestValue,
		},
		{
			name:        "When the source image ID is malformed it should reject the observation without inspecting it",
			label:       digestValue,
			malformedID: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			mockExec := executer.NewMockExecuter(ctrl)
			inspection := &client.PodmanVolumeInspect{
				Name:   "volume-1",
				Driver: "image",
				Options: map[string]string{
					"image": imageID,
				},
				Labels: map[string]string{
					client.PodmanDeltaVolumeApplicationLabel: applicationID,
					client.PodmanDeltaVolumeLifetimeLabel:    lifetime,
					client.PodmanDeltaVolumeReferenceLabel:   reference,
					client.PodmanDeltaVolumeOwnerLabel:       string(owner),
					client.PodmanDeltaVolumeOCITypeLabel:     string(dependency.OCITypePodmanImage),
					client.PodmanDeltaVolumeDigestLabel:      tc.label,
				},
			}
			if !tc.malformedID {
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "inspect", "--format", "{{.Digest}}", imageID).
					Return(tc.runtimeOut, "source image unavailable", tc.runtimeErr)
			} else {
				inspection.Options["image"] = "mutable:tag"
			}
			podman := client.NewPodman(log.NewPrefixLogger("test"), mockExec, fileio.NewReadWriter(fileio.NewReader(), fileio.NewWriter()), poll.Config{})
			observed, ok := podmanImageVolumeObservation(context.Background(), podman, inspection, applicationID, lifetime, owner)
			require.Equal(t, tc.want, ok)
			if tc.want {
				require.Equal(t, deltastatus.ApplicationImage{
					Image: reference, Owner: string(owner), OCIType: string(dependency.OCITypePodmanImage), Digest: digestValue,
				}, observed)
			}
		})
	}
}

func TestWhenManagerObservesMatchingWorkloadImagesItShouldReportTheirOutcome(t *testing.T) {
	const (
		appName = "web"
		image   = "quay.io/acme/web:latest"
		digest  = "sha256:old"
	)
	testCases := []struct {
		name            string
		requiredImages  []string
		workloads       []Workload
		providerDigest  string
		wantImageDigest string
		wantLastDelta   v1beta1.DeviceDeltaApplyOutcomeType
	}{
		{
			name:            "When every expected image is observed it should report Applied",
			requiredImages:  []string{image},
			workloads:       []Workload{{Name: "web", Status: StatusRunning, Images: []WorkloadImage{{Image: image, Digest: digest}}}},
			wantImageDigest: digest,
			wantLastDelta:   v1beta1.DeviceDeltaApplyOutcomeApplied,
		},
		{
			name:            "When an expected image is missing it should withhold LastDelta",
			requiredImages:  []string{image, "quay.io/acme/sidecar:latest"},
			workloads:       []Workload{{Name: "web", Status: StatusRunning, Images: []WorkloadImage{{Image: image, Digest: digest}}}},
			wantImageDigest: digest,
		},
		{
			name:           "When a running replica has an unknown digest it should withhold LastDelta",
			requiredImages: []string{image},
			workloads: []Workload{
				{Name: "web", Status: StatusRunning, Images: []WorkloadImage{{Image: image, Digest: digest}}},
				{Name: "unknown-replica", Status: StatusRunning, Images: []WorkloadImage{{Image: image}}},
			},
			wantImageDigest: "",
		},
		{
			name:            "When only the provider digest is known it should not invent an observed digest",
			requiredImages:  []string{image},
			workloads:       []Workload{{Name: "web", Status: StatusRunning, Images: []WorkloadImage{{Image: image}}}},
			providerDigest:  "sha256:desired",
			wantImageDigest: "",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			store, _, logger := newApplicationOutcomeStore(t)
			observed := applicationOutcomeImage(image, digest)
			targets := prepareApplicationOutcomeTargets(t, store, appName, []deltastatus.ApplicationImage{observed})
			recordApplicationOutcome(t, store, appName, targets, observed, v1beta1.DeviceDeltaApplyStatus{Outcome: v1beta1.DeviceDeltaApplyOutcomeApplied})
			manager, _ := newApplicationOutcomeManager(t, appName, [][]deltastatus.ApplicationImage{{observed}}, store, logger)
			app := manager.podmanMonitor.apps[appName].(*application)
			app.requiredImages = tc.requiredImages
			app.workloads = tc.workloads
			app.imageDigest = tc.providerDigest

			reported := &v1beta1.DeviceStatus{}
			require.NoError(manager.Status(context.Background(), reported))
			require.Len(reported.Applications, 1)
			require.Equal(tc.wantImageDigest, (*reported.Applications[0].ImageDigests)[0].Digest)
			if tc.wantLastDelta == "" {
				require.Nil(reported.Applications[0].LastDelta)
				return
			}
			require.NotNil(reported.Applications[0].LastDelta)
			require.Equal(tc.wantLastDelta, reported.Applications[0].LastDelta.Outcome)
		})
	}
}

func TestWhenApplicationStatusIsUpdatedItShouldPreserveThePreviousReport(t *testing.T) {
	require := require.New(t)
	const (
		appName = "web"
		image   = "quay.io/acme/web:latest"
		digest  = "sha256:old"
	)
	store, _, logger := newApplicationOutcomeStore(t)
	observed := applicationOutcomeImage(image, digest)
	targets := prepareApplicationOutcomeTargets(t, store, appName, []deltastatus.ApplicationImage{observed})
	recordApplicationOutcome(t, store, appName, targets, observed, v1beta1.DeviceDeltaApplyStatus{Outcome: v1beta1.DeviceDeltaApplyOutcomeApplied})
	manager, _ := newApplicationOutcomeManager(t, appName, [][]deltastatus.ApplicationImage{{observed}}, store, logger)
	app := manager.podmanMonitor.apps[appName].(*application)

	reported := &v1beta1.DeviceStatus{}
	require.NoError(manager.Status(context.Background(), reported))
	firstReport := reported.Applications[0]

	app.workloads = []Workload{{Name: "web", Status: StatusRunning, Images: []WorkloadImage{{Image: image}}}}
	app.imageDigest = "sha256:desired"
	require.NoError(manager.Status(context.Background(), reported))
	require.Nil(reported.Applications[0].LastDelta)
	require.Empty((*reported.Applications[0].ImageDigests)[0].Digest)
	require.Equal(digest, (*firstReport.ImageDigests)[0].Digest)
	require.NotNil(firstReport.LastDelta)
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

func TestWhenInlineQuadletWorkloadsAreObservedItShouldReportOnlyCompleteOutcomes(t *testing.T) {
	const (
		appName      = "quadlet-web"
		firstImage   = "quay.io/acme/frontend:latest"
		secondImage  = "quay.io/acme/backend:latest"
		firstDigest  = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		secondDigest = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	)
	content := func(path, image string) v1beta1.ApplicationContent {
		body := "[Container]\nImage=" + image + "\n"
		return v1beta1.ApplicationContent{Path: path, Content: &body}
	}
	for _, tc := range []struct {
		name      string
		workloads []Workload
		want      bool
	}{
		{
			name: "When one of two expected workloads is observed it should withhold LastDelta",
			workloads: []Workload{{
				Name: "frontend", Status: StatusRunning,
				Images: []WorkloadImage{{Image: firstImage, Digest: firstDigest}},
			}},
		},
		{
			name: "When both expected workloads are observed it should report Applied",
			workloads: []Workload{
				{Name: "frontend", Status: StatusRunning, Images: []WorkloadImage{{Image: firstImage, Digest: firstDigest}}},
				{Name: "backend", Status: StatusRunning, Images: []WorkloadImage{{Image: secondImage, Digest: secondDigest}}},
			},
			want: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			logger := log.NewPrefixLogger("test")
			store, _, _ := newApplicationOutcomeStore(t)
			frontend := applicationOutcomeImage(firstImage, firstDigest)
			backend := applicationOutcomeImage(secondImage, secondDigest)
			targets := prepareApplicationOutcomeTargets(t, store, appName, []deltastatus.ApplicationImage{frontend, backend})
			recordApplicationOutcome(t, store, appName, targets, frontend, v1beta1.DeviceDeltaApplyStatus{Outcome: v1beta1.DeviceDeltaApplyOutcomeApplied})
			recordApplicationOutcome(t, store, appName, targets, backend, v1beta1.DeviceDeltaApplyStatus{Outcome: v1beta1.DeviceDeltaApplyOutcomeApplied})

			quadlet := v1beta1.QuadletApplication{AppType: v1beta1.AppTypeQuadlet, RunAs: v1beta1.RootUsername}
			require.NoError(quadlet.FromInlineApplicationProviderSpec(v1beta1.InlineApplicationProviderSpec{
				Inline: []v1beta1.ApplicationContent{
					content("frontend.container", firstImage),
					content("backend.container", secondImage),
				},
			}))
			volume, err := provider.NewVolumeManager(logger, appName, v1beta1.AppTypeQuadlet, v1beta1.RootUsername, nil)
			require.NoError(err)
			appSpec := &provider.ApplicationSpec{
				ID: appName, Name: appName, AppType: v1beta1.AppTypeQuadlet,
				User: v1beta1.RootUsername, Lifetime: store.ApplicationLifetime(appName), QuadletApp: &quadlet, Volume: volume,
			}
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			appProvider := provider.NewMockProvider(ctrl)
			appProvider.EXPECT().ID().Return(appName).AnyTimes()
			appProvider.EXPECT().Spec().Return(appSpec).AnyTimes()

			targetCache := provider.NewOCITargetCache()
			// Inline applications can have a successful extraction entry with no children.
			targetCache.Set(provider.CacheEntry{Name: appName})
			manager := &manager{
				log: logger, deltaStatusStore: store, ociTargetCache: targetCache,
				podmanMonitor:     &PodmanMonitor{apps: make(map[string]Application)},
				kubernetesMonitor: &KubernetesMonitor{monitor: &monitor{apps: make(map[string]Application)}},
			}
			app := manager.newAppFromProvider(appProvider)
			for _, workload := range tc.workloads {
				app.AddWorkload(&workload)
			}
			manager.podmanMonitor.apps[app.ID()] = app

			reported := &v1beta1.DeviceStatus{}
			require.NoError(manager.Status(context.Background(), reported))
			require.Len(reported.Applications, 1)
			if !tc.want {
				require.Nil(reported.Applications[0].LastDelta)
				return
			}
			require.NotNil(reported.Applications[0].LastDelta)
			require.Equal(v1beta1.DeviceDeltaApplyOutcomeApplied, reported.Applications[0].LastDelta.Outcome)
		})
	}
}

func TestWhenImageBasedComposeBundleHasNoDeltaHistoryItShouldReportWorkloadOutcome(t *testing.T) {
	require := require.New(t)
	const (
		appName       = "web"
		bundle        = "quay.io/acme/compose/web:1.0.0"
		bundleDigest  = "sha256:bundle"
		workloadImage = "quay.io/acme/web:latest"
		workloadHash  = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	)

	store, _, logger := newApplicationOutcomeStore(t)
	bundleImage := deltastatus.ApplicationImage{
		Image: bundle, Owner: string(v1beta1.RootUsername), OCIType: string(dependency.OCITypePodmanArtifact), Digest: bundleDigest,
	}
	workload := applicationOutcomeImage(workloadImage, workloadHash)
	targets := prepareApplicationOutcomeTargets(t, store, appName, []deltastatus.ApplicationImage{bundleImage, workload})
	recordApplicationOutcome(t, store, appName, targets, workload, v1beta1.DeviceDeltaApplyStatus{Outcome: v1beta1.DeviceDeltaApplyOutcomeApplied})

	volume, err := provider.NewVolumeManager(logger, appName, v1beta1.AppTypeCompose, v1beta1.RootUsername, nil)
	require.NoError(err)
	var composeApp v1beta1.ComposeApplication
	require.NoError(composeApp.FromImageApplicationProviderSpec(v1beta1.ImageApplicationProviderSpec{Image: bundle}))
	appSpec := &provider.ApplicationSpec{
		ID: appName, Name: appName, AppType: v1beta1.AppTypeCompose, User: v1beta1.RootUsername,
		Image: bundle, ImageDigest: bundleDigest, ImageOCIType: string(dependency.OCITypePodmanArtifact),
		ComposeApp: &composeApp, Volume: volume,
	}
	targetCache := provider.NewOCITargetCache()
	targetCache.Set(provider.CacheEntry{
		Name: appName,
		Parent: dependency.OCIPullTarget{
			Reference: bundle, Digest: bundleDigest, Type: dependency.OCITypePodmanArtifact,
		},
		Children: []dependency.OCIPullTarget{{
			Type: dependency.OCITypePodmanImage, Reference: workloadImage, Digest: workloadHash,
		}},
	})
	manager, app := newTestComposeOutcomeManager(t, appSpec, targetCache, store, logger)
	app.workloads = []Workload{{
		Name: "web", Status: StatusRunning, Images: []WorkloadImage{{Image: workloadImage, Digest: workloadHash}},
	}}

	reported := &v1beta1.DeviceStatus{}
	require.NoError(manager.Status(context.Background(), reported))
	require.Len(reported.Applications, 1)
	require.ElementsMatch([]v1beta1.ApplicationImageDigest{
		{Image: bundle, Digest: bundleDigest},
		{Image: workloadImage, Digest: workloadHash},
	}, *reported.Applications[0].ImageDigests)
	require.NotNil(reported.Applications[0].LastDelta)
	require.Equal(v1beta1.DeviceDeltaApplyOutcomeApplied, reported.Applications[0].LastDelta.Outcome)
}

func TestWhenImageBasedQuadletHasCompleteWorkloadOutcomesItShouldReportApplied(t *testing.T) {
	require := require.New(t)
	const (
		appName       = "web"
		bundle        = "quay.io/acme/compose/web:1.0.0"
		bundleDigest  = "sha256:bundle"
		workloadImage = "quay.io/acme/web:latest"
		workloadHash  = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	)

	store, _, logger := newApplicationOutcomeStore(t)
	bundleImage := deltastatus.ApplicationImage{
		Image: bundle, Owner: string(v1beta1.RootUsername), OCIType: string(dependency.OCITypePodmanArtifact), Digest: bundleDigest,
	}
	workload := applicationOutcomeImage(workloadImage, workloadHash)
	targets := prepareApplicationOutcomeTargets(t, store, appName, []deltastatus.ApplicationImage{bundleImage, workload})
	recordApplicationOutcome(t, store, appName, targets, bundleImage, v1beta1.DeviceDeltaApplyStatus{Outcome: v1beta1.DeviceDeltaApplyOutcomeNotRequired})
	recordApplicationOutcome(t, store, appName, targets, workload, v1beta1.DeviceDeltaApplyStatus{Outcome: v1beta1.DeviceDeltaApplyOutcomeApplied})

	volume, err := provider.NewVolumeManager(logger, appName, v1beta1.AppTypeQuadlet, v1beta1.RootUsername, nil)
	require.NoError(err)
	var quadletApp v1beta1.QuadletApplication
	require.NoError(quadletApp.FromImageApplicationProviderSpec(v1beta1.ImageApplicationProviderSpec{Image: bundle}))
	appSpec := &provider.ApplicationSpec{
		ID: appName, Name: appName, AppType: v1beta1.AppTypeQuadlet, User: v1beta1.RootUsername,
		Image: bundle, ImageDigest: bundleDigest, ImageOCIType: string(dependency.OCITypePodmanArtifact),
		QuadletApp: &quadletApp, Volume: volume,
	}
	targetCache := provider.NewOCITargetCache()
	targetCache.Set(provider.CacheEntry{
		Name: appName,
		Parent: dependency.OCIPullTarget{
			Reference: bundle, Digest: bundleDigest, Type: dependency.OCITypePodmanArtifact,
		},
		Children: []dependency.OCIPullTarget{{
			Type: dependency.OCITypePodmanImage, Reference: workloadImage, Digest: workloadHash,
		}},
	})
	manager, app := newTestComposeOutcomeManager(t, appSpec, targetCache, store, logger)
	app.workloads = []Workload{{
		Name: "web", Status: StatusRunning, Images: []WorkloadImage{{Image: workloadImage, Digest: workloadHash}},
	}}

	reported := &v1beta1.DeviceStatus{}
	require.NoError(manager.Status(context.Background(), reported))
	require.Len(reported.Applications, 1)
	require.ElementsMatch([]v1beta1.ApplicationImageDigest{
		{Image: bundle, Digest: bundleDigest},
		{Image: workloadImage, Digest: workloadHash},
	}, *reported.Applications[0].ImageDigests)
	require.NotNil(reported.Applications[0].LastDelta)
	require.Equal(v1beta1.DeviceDeltaApplyOutcomeApplied, reported.Applications[0].LastDelta.Outcome)
}

func TestWhenInlineComposeHasTwoServiceImagesItShouldRequireBothObservedImages(t *testing.T) {
	const (
		appName        = "web"
		frontendImage  = "quay.io/acme/frontend:latest"
		frontendDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		backendImage   = "quay.io/acme/backend:latest"
		backendDigest  = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	)

	for _, tc := range []struct {
		name             string
		observedWorkload []Workload
		wantOutcome      bool
	}{
		{
			name: "When all service images are observed it should report Applied",
			observedWorkload: []Workload{
				{Name: "frontend", Status: StatusRunning, Images: []WorkloadImage{{Image: frontendImage, Digest: frontendDigest}}},
				{Name: "backend", Status: StatusRunning, Images: []WorkloadImage{{Image: backendImage, Digest: backendDigest}}},
			},
			wantOutcome: true,
		},
		{
			name: "When one service image is missing it should withhold LastDelta",
			observedWorkload: []Workload{
				{Name: "frontend", Status: StatusRunning, Images: []WorkloadImage{{Image: frontendImage, Digest: frontendDigest}}},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			store, _, logger := newApplicationOutcomeStore(t)
			frontend := applicationOutcomeImage(frontendImage, frontendDigest)
			backend := applicationOutcomeImage(backendImage, backendDigest)
			targets := prepareApplicationOutcomeTargets(t, store, appName, []deltastatus.ApplicationImage{frontend, backend})
			recordApplicationOutcome(t, store, appName, targets, frontend, v1beta1.DeviceDeltaApplyStatus{Outcome: v1beta1.DeviceDeltaApplyOutcomeApplied})
			recordApplicationOutcome(t, store, appName, targets, backend, v1beta1.DeviceDeltaApplyStatus{Outcome: v1beta1.DeviceDeltaApplyOutcomeApplied})

			volume, err := provider.NewVolumeManager(logger, appName, v1beta1.AppTypeCompose, v1beta1.RootUsername, nil)
			require.NoError(err)
			composeYAML := "services:\n  frontend:\n    image: " + frontendImage + "\n  backend:\n    image: " + backendImage + "\n"
			var composeApp v1beta1.ComposeApplication
			require.NoError(composeApp.FromInlineApplicationProviderSpec(v1beta1.InlineApplicationProviderSpec{
				Inline: []v1beta1.ApplicationContent{{Path: "docker-compose.yml", Content: &composeYAML}},
			}))
			appSpec := &provider.ApplicationSpec{
				ID: appName, Name: appName, AppType: v1beta1.AppTypeCompose, User: v1beta1.RootUsername,
				ComposeApp: &composeApp, Volume: volume,
			}
			targetCache := provider.NewOCITargetCache()
			targetCache.Set(provider.CacheEntry{Name: appName})
			manager, app := newTestComposeOutcomeManager(t, appSpec, targetCache, store, logger)
			app.workloads = tc.observedWorkload

			reported := &v1beta1.DeviceStatus{}
			require.NoError(manager.Status(context.Background(), reported))
			require.Len(reported.Applications, 1)
			if !tc.wantOutcome {
				require.Nil(reported.Applications[0].LastDelta)
				return
			}
			require.NotNil(reported.Applications[0].LastDelta)
			require.Equal(v1beta1.DeviceDeltaApplyOutcomeApplied, reported.Applications[0].LastDelta.Outcome)
		})
	}
}

func newTestComposeOutcomeManager(
	t *testing.T,
	appSpec *provider.ApplicationSpec,
	targetCache *provider.OCITargetCache,
	store *deltastatus.Store,
	logger *log.PrefixLogger,
) (*manager, *application) {
	t.Helper()
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	appProvider := provider.NewMockProvider(ctrl)
	appProvider.EXPECT().EnsureDependencies(gomock.Any()).Return(nil)
	appProvider.EXPECT().Install(gomock.Any()).Return(nil)
	appProvider.EXPECT().Spec().Return(appSpec).AnyTimes()
	appProvider.EXPECT().ID().Return(appSpec.ID).AnyTimes()

	manager := &manager{
		log:              logger,
		deltaStatusStore: store,
		ociTargetCache:   targetCache,
		podmanMonitor:    &PodmanMonitor{apps: make(map[string]Application)},
		kubernetesMonitor: &KubernetesMonitor{
			monitor: newMonitor(logger, "kubernetes"),
		},
	}
	require.NoError(manager.Ensure(context.Background(), appProvider))
	app, ok := manager.podmanMonitor.apps[appSpec.ID].(*application)
	require.True(ok)
	return manager, app
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
		lifetime:                store.ApplicationLifetime(appName),
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

func TestManagerReportsCompleteOutcomeForApplicationWithMoreThanEightImages(t *testing.T) {
	const (
		applicationName = "large-app"
		applicationID   = "large-app-id"
	)
	root := t.TempDir()
	rw := fileio.NewReadWriter(fileio.NewReader(fileio.WithReaderRootDir(root)), fileio.NewWriter(fileio.WithWriterRootDir(root)))
	logger := log.NewPrefixLogger("test")
	store := deltastatus.New(rw, "/var/lib/flightctl", logger)
	images := make([]deltastatus.ApplicationImage, 12)
	workloads := make([]Workload, 12)
	requiredImages := make([]string, 12)
	for i := range images {
		image := deltastatus.ApplicationImage{
			Image:   fmt.Sprintf("quay.io/acme/workload-%02d:latest", i),
			Owner:   string(v1beta1.RootUsername),
			OCIType: string(dependency.OCITypePodmanImage),
			Digest:  fmt.Sprintf("sha256:observed-%02d", i),
		}
		images[i] = image
		requiredImages[i] = image.Image
		workloads[i] = Workload{
			Name:   fmt.Sprintf("workload-%02d", i),
			Status: StatusRunning,
			Images: []WorkloadImage{{Image: image.Image, Digest: image.Digest}},
		}
	}
	lifetime := recordApplicationImageOutcomes(t, store, applicationName, "spec", images)
	volume, err := provider.NewVolumeManager(logger, applicationName, v1beta1.AppTypeContainer, v1beta1.RootUsername, nil)
	require.NoError(t, err)
	app := &application{
		id:                      applicationID,
		lifetime:                lifetime,
		requiredImages:          requiredImages,
		imageCollectionComplete: true,
		volume:                  volume,
		status:                  &v1beta1.DeviceApplicationStatus{Name: applicationName, RunAs: v1beta1.RootUsername, AppType: v1beta1.AppTypeContainer},
		workloads:               workloads,
	}
	manager := &manager{
		log:               logger,
		deltaStatusStore:  store,
		podmanMonitor:     &PodmanMonitor{apps: map[string]Application{applicationID: app}},
		kubernetesMonitor: &KubernetesMonitor{monitor: &monitor{apps: map[string]Application{}}},
	}

	reported := &v1beta1.DeviceStatus{}
	require.NoError(t, manager.Status(context.Background(), reported))
	require.Len(t, reported.Applications, 1)
	require.Len(t, *reported.Applications[0].ImageDigests, 12)
	var expected []v1beta1.ApplicationImageDigest
	for _, image := range images {
		expected = append(expected, v1beta1.ApplicationImageDigest{Image: image.Image, Digest: image.Digest})
	}
	require.ElementsMatch(t, expected, *reported.Applications[0].ImageDigests)
	require.NotNil(t, reported.Applications[0].LastDelta)
	require.Equal(t, v1beta1.DeviceDeltaApplyOutcomeApplied, reported.Applications[0].LastDelta.Outcome)
}

func TestManagerAggregatesWorkloadVolumeAndParentWithinCapturedLifetime(t *testing.T) {
	const (
		appName                = "web"
		applicationID          = "web-id"
		appSpecKey             = "web-spec"
		workloadRef            = "quay.io/acme/web:latest"
		parentRef              = "quay.io/acme/parent:v1"
		volumeRef              = "quay.io/acme/disk:latest"
		volumeID               = "web-data"
		volumeImageID          = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
		workloadIndexDigest    = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		workloadPlatformDigest = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		parentDigest           = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
		volumeDigest           = "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	)

	for _, tc := range []struct {
		name                     string
		recordedWorkloadDigest   string
		recreateDuringVolumeRead bool
		wantDelta                bool
	}{
		{name: "When workload, image volume, and artifact parent are complete it should aggregate", recordedWorkloadDigest: workloadIndexDigest, wantDelta: true},
		{name: "When the index and platform manifest differ it should withhold the exact match", recordedWorkloadDigest: workloadPlatformDigest},
		{name: "When the application is recreated during volume inspection it should reject the stale snapshot", recordedWorkloadDigest: workloadIndexDigest, recreateDuringVolumeRead: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			rw := fileio.NewReadWriter(fileio.NewReader(fileio.WithReaderRootDir(root)), fileio.NewWriter(fileio.WithWriterRootDir(root)))
			logger := log.NewPrefixLogger("test")
			store := deltastatus.New(rw, "/var/lib/flightctl", logger)
			images := []deltastatus.ApplicationImage{
				{Image: workloadRef, Owner: "root", OCIType: string(dependency.OCITypePodmanImage), Digest: tc.recordedWorkloadDigest},
				{Image: parentRef, Owner: "root", OCIType: string(dependency.OCITypePodmanArtifact), Digest: parentDigest},
				{Image: volumeRef, Owner: "root", OCIType: string(dependency.OCITypePodmanImage), Digest: volumeDigest},
			}
			lifetime := recordApplicationImageOutcomes(t, store, appName, appSpecKey, images)
			volumeManager, err := provider.NewVolumeManager(logger, appName, v1beta1.AppTypeQuadlet, v1beta1.RootUsername, nil)
			require.NoError(t, err)
			volumeManager.Add(&provider.Volume{
				Name: "data", ID: volumeID, Reference: volumeRef,
				OCIType: string(dependency.OCITypePodmanImage), Available: true,
			})
			observedApp := &application{
				id:                      applicationID,
				imageRef:                parentRef,
				imageDigest:             parentDigest,
				imageOCIType:            string(dependency.OCITypePodmanArtifact),
				lifetime:                lifetime,
				parentOutcomeRequired:   true,
				requiredImages:          []string{workloadRef, volumeRef},
				imageCollectionComplete: true,
				volume:                  volumeManager,
				status:                  &v1beta1.DeviceApplicationStatus{Name: appName, RunAs: v1beta1.RootUsername, AppType: v1beta1.AppTypeQuadlet},
				workloads:               []Workload{{Name: "web", Status: StatusRunning, Images: []WorkloadImage{{Image: workloadRef, Digest: workloadIndexDigest}}}},
			}

			inspection := client.PodmanVolumeInspect{
				Name: volumeID, Driver: "image", CreatedAt: "2026-10-12T12:00:00Z",
				Options: map[string]string{"image": volumeImageID},
				Labels: map[string]string{
					client.PodmanDeltaVolumeApplicationLabel: applicationID,
					client.PodmanDeltaVolumeLifetimeLabel:    lifetime,
					client.PodmanDeltaVolumeReferenceLabel:   volumeRef,
					client.PodmanDeltaVolumeOwnerLabel:       "root",
					client.PodmanDeltaVolumeOCITypeLabel:     string(dependency.OCITypePodmanImage),
					client.PodmanDeltaVolumeDigestLabel:      volumeDigest,
				},
			}
			inspectionJSON, err := json.Marshal(inspection)
			require.NoError(t, err)
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			mockExec := executer.NewMockExecuter(ctrl)
			mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "volume", "inspect", volumeID, "--format", "{{json .}}").Return(string(inspectionJSON), "", 0).AnyTimes()
			imageDigestCall := mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "inspect", "--format", "{{.Digest}}", volumeImageID)
			if tc.recreateDuringVolumeRead {
				imageDigestCall.DoAndReturn(func(context.Context, string, ...string) (string, string, int) {
					require.NoError(t, store.RemoveApplication(appName))
					newLifetime := recordApplicationImageOutcomes(t, store, appName, appSpecKey, images)
					require.NotEqual(t, lifetime, newLifetime)
					return volumeDigest, "", 0
				})
			} else {
				imageDigestCall.Return(volumeDigest, "", 0)
			}
			podman := client.NewPodman(logger, mockExec, rw, poll.Config{})
			manager := &manager{
				log: logger, deltaStatusStore: store,
				podmanFactory:     func(v1beta1.Username) (*client.Podman, error) { return podman, nil },
				ociTargetCache:    provider.NewOCITargetCache(),
				podmanMonitor:     &PodmanMonitor{apps: map[string]Application{applicationID: observedApp}},
				kubernetesMonitor: &KubernetesMonitor{monitor: &monitor{apps: map[string]Application{}}},
			}

			reported := &v1beta1.DeviceStatus{}
			require.NoError(t, manager.Status(context.Background(), reported))
			require.Len(t, reported.Applications, 1)
			require.ElementsMatch(t, []v1beta1.ApplicationImageDigest{
				{Image: workloadRef, Digest: workloadIndexDigest},
				{Image: parentRef, Digest: parentDigest},
				{Image: volumeRef, Digest: volumeDigest},
			}, *reported.Applications[0].ImageDigests)
			if tc.wantDelta {
				require.NotNil(t, reported.Applications[0].LastDelta)
				require.Equal(t, v1beta1.DeviceDeltaApplyOutcomeApplied, reported.Applications[0].LastDelta.Outcome)
				// Repeated collection still inspects the volume, but should reuse its
				// verified immutable image digest.
				require.NoError(t, manager.Status(context.Background(), &v1beta1.DeviceStatus{}))
			} else {
				require.Nil(t, reported.Applications[0].LastDelta)
			}
		})
	}
}

func TestManagerWithholdsArtifactVolumeDigestWhenBindingChangesDuringInspection(t *testing.T) {
	const (
		application = "web"
		volumeID    = "web-data"
		volumeRef   = "quay.io/acme/data:latest"
		createdAt   = "2026-10-12T12:00:00Z"
	)

	root := t.TempDir()
	rw := fileio.NewReadWriter(fileio.NewReader(fileio.WithReaderRootDir(root)), fileio.NewWriter(fileio.WithWriterRootDir(root)))
	logger := log.NewPrefixLogger("test")
	store := deltastatus.New(rw, "/var/lib/flightctl", logger)
	require.NoError(t, store.ReconcileApplicationSpecs(map[string]string{application: "spec"}))
	currentLifetime := store.ApplicationLifetime(application)
	workload := deltastatus.ApplicationImage{Image: "quay.io/acme/web:latest", Owner: "root", OCIType: string(dependency.OCITypePodmanImage), Digest: "sha256:workload"}
	volumeBefore := deltastatus.ApplicationImage{Image: volumeRef, Owner: "root", OCIType: string(dependency.OCITypePodmanArtifact), Digest: "sha256:artifact-before"}
	volumeAfter := volumeBefore
	volumeAfter.Digest = "sha256:artifact-after"
	workloadTargetID := deltastatus.Fingerprint(workload.Owner, workload.Image, workload.OCIType)
	volumeTargetID := deltastatus.Fingerprint(volumeBefore.Owner, volumeBefore.Image, volumeBefore.OCIType)
	targets := map[string]string{workloadTargetID: "workload-target", volumeTargetID: "volume-target"}
	require.NoError(t, store.ReconcileApplicationTargets(map[string]map[string]string{application: targets}, true))
	outcome := v1beta1.DeviceDeltaApplyStatus{Outcome: v1beta1.DeviceDeltaApplyOutcomeApplied}
	require.NoError(t, store.RecordApplicationImageResultForAttempt(deltastatus.ApplicationAttempt{Application: application, Lifetime: currentLifetime, SpecKey: "spec", TargetID: workloadTargetID, TargetKey: targets[workloadTargetID]}, workload, outcome))
	require.NoError(t, store.RecordApplicationImageResultForAttempt(deltastatus.ApplicationAttempt{Application: application, Lifetime: currentLifetime, SpecKey: "spec", TargetID: volumeTargetID, TargetKey: targets[volumeTargetID]}, volumeBefore, outcome))
	require.NoError(t, store.RecordApplicationVolumeImage(application, currentLifetime, volumeID, createdAt, volumeBefore))

	inspection := client.PodmanVolumeInspect{Name: volumeID, Driver: "local", CreatedAt: createdAt}
	inspectionJSON, err := json.Marshal(inspection)
	require.NoError(t, err)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockExec := executer.NewMockExecuter(ctrl)
	inspectCall := mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "volume", "inspect", volumeID, "--format", "{{json .}}")
	inspectCall.DoAndReturn(func(context.Context, string, ...string) (string, string, int) {
		// Artifact content can change in place without changing the volume's
		// public CreatedAt identity. Collection must not pair the old runtime
		// inspection with this newly recorded digest.
		require.NoError(t, store.RecordApplicationVolumeImage(application, currentLifetime, volumeID, createdAt, volumeAfter))
		return string(inspectionJSON), "", 0
	})
	podman := client.NewPodman(logger, mockExec, rw, poll.Config{})
	result := AppStatusResult{
		Images: ImageSnapshot{
			ID:       "web-id",
			Lifetime: currentLifetime,
			Images:   []deltastatus.ApplicationImage{workload},
			Volumes:  []ImageVolumeSnapshot{{ID: volumeID, Reference: volumeRef, OCIType: string(dependency.OCITypePodmanArtifact), Available: true}},
			Complete: true,
		},
		Status: v1beta1.DeviceApplicationStatus{Name: application, RunAs: v1beta1.RootUsername},
	}
	manager := &manager{
		log:              logger,
		deltaStatusStore: store,
		podmanFactory:    func(v1beta1.Username) (*client.Podman, error) { return podman, nil },
	}
	results := []AppStatusResult{result}
	manager.addVolumeImageObservations(context.Background(), results)

	require.False(t, results[0].Images.Complete)
	require.Len(t, results[0].Images.Images, 1)
	require.Nil(t, store.ObservedApplicationResultForLifetime(application, currentLifetime, results[0].Images.Images, results[0].Images.Complete))
	currentBinding, ok := store.ApplicationVolumeBinding(application, currentLifetime, volumeID)
	require.True(t, ok)
	require.Equal(t, createdAt, currentBinding.CreatedAt)
	require.Equal(t, volumeAfter, currentBinding.Image)
}

func recordApplicationImageOutcomes(
	t *testing.T,
	store *deltastatus.Store,
	application, specKey string,
	images []deltastatus.ApplicationImage,
) string {
	t.Helper()
	require.NoError(t, store.ReconcileApplicationSpecs(map[string]string{application: specKey}))
	lifetime := store.ApplicationLifetime(application)
	targets := make(map[string]string, len(images))
	for _, image := range images {
		targetID := deltastatus.Fingerprint(image.Owner, image.Image, image.OCIType)
		targets[targetID] = "target:" + image.Image + ":" + image.OCIType
	}
	require.NoError(t, store.ReconcileApplicationTargets(map[string]map[string]string{application: targets}, true))
	for _, image := range images {
		targetID := deltastatus.Fingerprint(image.Owner, image.Image, image.OCIType)
		require.NoError(t, store.RecordApplicationImageResultForAttempt(
			deltastatus.ApplicationAttempt{Application: application, Lifetime: lifetime, SpecKey: specKey, TargetID: targetID, TargetKey: targets[targetID]}, image,
			v1beta1.DeviceDeltaApplyStatus{Outcome: v1beta1.DeviceDeltaApplyOutcomeApplied},
		))
	}
	return lifetime
}
