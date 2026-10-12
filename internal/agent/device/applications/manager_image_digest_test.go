package applications

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
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
		name       string
		runtimeOut string
		runtimeErr int
		label      string
		want       bool
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
			name:  "When the source image ID is malformed it should reject the observation without inspecting it",
			label: digestValue,
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
			if tc.name != "When the source image ID is malformed it should reject the observation without inspecting it" {
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

func TestManagerReportsMatchingWorkloadOutcome(t *testing.T) {
	root := t.TempDir()
	rw := fileio.NewReadWriter(fileio.NewReader(fileio.WithReaderRootDir(root)), fileio.NewWriter(fileio.WithWriterRootDir(root)))
	logger := log.NewPrefixLogger("test")
	store := deltastatus.New(rw, "/var/lib/flightctl", logger)
	image := deltastatus.ApplicationImage{Image: "quay.io/acme/web:latest", Owner: "root", OCIType: string(dependency.OCITypePodmanImage), Digest: "sha256:old"}
	targetID := deltastatus.Fingerprint(image.Owner, image.Image, image.OCIType)
	require.NoError(t, store.ReconcileApplicationSpecs(map[string]string{"web": "spec"}))
	lifetime := store.ApplicationLifetime("web")
	require.NoError(t, store.ReconcileApplicationTargets(map[string]map[string]string{"web": {targetID: "target"}}, true))
	require.NoError(t, store.RecordApplicationImageResult("web", "spec", targetID, "target", image, v1beta1.DeviceDeltaApplyStatus{Outcome: v1beta1.DeviceDeltaApplyOutcomeApplied}))
	volume, err := provider.NewVolumeManager(logger, "web", v1beta1.AppTypeContainer, "root", nil)
	require.NoError(t, err)
	app := &application{id: "web-id", imageRef: image.Image, lifetime: lifetime, requiredImages: []string{image.Image}, imageCollectionComplete: true, volume: volume, status: &v1beta1.DeviceApplicationStatus{Name: "web", RunAs: "root", AppType: v1beta1.AppTypeContainer}, workloads: []Workload{{Name: "web", Status: StatusRunning, Images: []WorkloadImage{{Image: image.Image, Digest: image.Digest}}}}}
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
		{name: "complete workload, image volume, and artifact parent should aggregate", recordedWorkloadDigest: workloadIndexDigest, wantDelta: true},
		{name: "index and platform manifest mismatch should withhold the exact match", recordedWorkloadDigest: workloadPlatformDigest},
		{name: "a snapshot collected before application recreation should be rejected", recordedWorkloadDigest: workloadIndexDigest, recreateDuringVolumeRead: true},
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
			volumeManager, err := provider.NewVolumeManager(logger, appName, v1beta1.AppTypeCompose, v1beta1.RootUsername, nil)
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
				parentRequired:          true,
				requiredImages:          []string{workloadRef},
				imageCollectionComplete: true,
				volume:                  volumeManager,
				status:                  &v1beta1.DeviceApplicationStatus{Name: appName, RunAs: v1beta1.RootUsername, AppType: v1beta1.AppTypeCompose},
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
			mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "volume", "inspect", volumeID, "--format", "{{json .}}").Return(string(inspectionJSON), "", 0)
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
	require.NoError(t, store.RecordApplicationImageResultForLifetime(application, currentLifetime, "spec", workloadTargetID, targets[workloadTargetID], workload, outcome))
	require.NoError(t, store.RecordApplicationImageResultForLifetime(application, currentLifetime, "spec", volumeTargetID, targets[volumeTargetID], volumeBefore, outcome))
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
		require.NoError(t, store.RecordApplicationImageResultForLifetime(
			application, lifetime, specKey, targetID, targets[targetID], image,
			v1beta1.DeviceDeltaApplyStatus{Outcome: v1beta1.DeviceDeltaApplyOutcomeApplied},
		))
	}
	return lifetime
}
