package applications

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/applications/helm"
	"github.com/flightctl/flightctl/internal/agent/device/applications/provider"
	"github.com/flightctl/flightctl/internal/agent/device/deltastatus"
	"github.com/flightctl/flightctl/internal/agent/device/dependency"
	"github.com/flightctl/flightctl/internal/agent/device/systemd"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/poll"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestWhenHelmCronJobHasNoPodItShouldReportObservedWorkloadOutcome(t *testing.T) {
	for _, suspended := range []bool{false, true} {
		name := "When every rendered image has a Pod it should report Applied"
		if suspended {
			name = "When a rendered CronJob is suspended it should report the observed workload outcome"
		}
		t.Run(name, func(t *testing.T) {
			require := require.New(t)
			const appName = "delta-helm"
			const chart = "oci://quay.io/acme/charts/web:1.0.0"
			const imageRef = "quay.io/acme/web:target"
			const digest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
			store, _, logger := newApplicationOutcomeStore(t)
			images := []deltastatus.ApplicationImage{{Image: imageRef, Owner: string(v1beta1.RootUsername), OCIType: string(dependency.OCITypeCRIImage), Digest: digest}}
			if suspended {
				images = append(images, deltastatus.ApplicationImage{Image: "quay.io/acme/cron:target", Owner: string(v1beta1.RootUsername), OCIType: string(dependency.OCITypeCRIImage), Digest: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"})
			}
			targets := prepareApplicationOutcomeTargets(t, store, appName, images)
			children := make([]dependency.OCIPullTarget, 0, len(images))
			for _, image := range images {
				recordApplicationOutcome(t, store, appName, targets, image, v1beta1.DeviceDeltaApplyStatus{Outcome: v1beta1.DeviceDeltaApplyOutcomeApplied})
				children = append(children, dependency.OCIPullTarget{Reference: image.Image, Digest: image.Digest, Type: dependency.OCITypeCRIImage})
			}
			volume, err := provider.NewVolumeManager(logger, appName, v1beta1.AppTypeHelm, v1beta1.RootUsername, nil)
			require.NoError(err)
			appSpec := &provider.ApplicationSpec{ID: appName, Name: appName, AppType: v1beta1.AppTypeHelm, User: v1beta1.RootUsername, Image: chart, Volume: volume}
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			appProvider := provider.NewMockProvider(ctrl)
			appProvider.EXPECT().EnsureDependencies(gomock.Any()).Return(nil)
			appProvider.EXPECT().Install(gomock.Any()).Return(nil)
			appProvider.EXPECT().Spec().Return(appSpec).AnyTimes()
			appProvider.EXPECT().ID().Return(appName).AnyTimes()
			cache := provider.NewOCITargetCache()
			cache.Set(provider.CacheEntry{Name: appName, Parent: dependency.OCIPullTarget{Reference: chart}, Children: children})
			monitor := &KubernetesMonitor{monitor: newMonitor(logger, "kubernetes")}
			mgr := &manager{log: logger, deltaStatusStore: store, ociTargetCache: cache, podmanMonitor: &PodmanMonitor{apps: make(map[string]Application)}, kubernetesMonitor: monitor}
			ctx := context.Background()
			require.NoError(mgr.Ensure(ctx, appProvider))
			event := kubernetesWatchEvent{Type: "ADDED"}
			event.Object.Kind = "Pod"
			event.Object.Metadata.Name = "web"
			event.Object.Metadata.UID = "web-pod"
			event.Object.Metadata.Labels = map[string]string{helm.AppLabelKey: appName}
			event.Object.Spec.Containers = []kubernetesContainerSpec{{Name: "web", Image: imageRef}}
			event.Object.Status.ContainerStatuses = []kubernetesContainerStatus{{Name: "web", ImageID: "docker-pullable://quay.io/acme/web@" + digest, Ready: true}}
			data, err := json.Marshal(event)
			require.NoError(err)
			monitor.HandleEvent(ctx, data)
			reported := &v1beta1.DeviceStatus{}
			require.NoError(mgr.Status(ctx, reported))
			require.Len(reported.Applications, 1)
			require.Contains(*reported.Applications[0].ImageDigests, v1beta1.ApplicationImageDigest{Image: imageRef, Digest: digest})
			require.NotNil(reported.Applications[0].LastDelta, "an image without a Pod must not block observed workload outcomes")
			require.Equal(v1beta1.DeviceDeltaApplyOutcomeApplied, reported.Applications[0].LastDelta.Outcome)
			if suspended {
				require.NotContains(*reported.Applications[0].ImageDigests, v1beta1.ApplicationImageDigest{Image: images[1].Image, Digest: images[1].Digest})
				// An actual Pod with an unresolved container image must still
				// block the aggregate, even though preparation knows its digest.
				event.Object.Metadata.Name = "cronjob"
				event.Object.Metadata.UID = "cronjob-pod"
				event.Object.Spec.Containers[0].Image = images[1].Image
				event.Object.Status.ContainerStatuses = nil
				data, err = json.Marshal(event)
				require.NoError(err)
				monitor.HandleEvent(ctx, data)
				require.NoError(mgr.Status(ctx, reported))
				require.Nil(reported.Applications[0].LastDelta)

				event.Type = "MODIFIED"
				event.Object.Status.ContainerStatuses = []kubernetesContainerStatus{{Name: "web", ImageID: "docker-pullable://quay.io/acme/cron@" + images[1].Digest, Ready: true}}
				data, err = json.Marshal(event)
				require.NoError(err)
				monitor.HandleEvent(ctx, data)
				require.NoError(mgr.Status(ctx, reported))
			}

			require.NotNil(reported.Applications[0].LastDelta, "healthy Helm application has not reported a delta apply outcome")
			require.Equal(v1beta1.DeviceDeltaApplyOutcomeApplied, reported.Applications[0].LastDelta.Outcome)
		})
	}
}

func TestWhenPodHasRootfsInfraContainerItShouldReportWorkloadOutcome(t *testing.T) {
	for _, appType := range []v1beta1.AppType{v1beta1.AppTypeQuadlet, v1beta1.AppTypeVm} {
		t.Run(string(appType), func(t *testing.T) {
			for _, tc := range []struct {
				name             string
				snapshot         string
				inspection       string
				replaceContainer bool
				wantOutcome      bool
			}{
				{name: "When create events identify a rootfs infra container it should retain that evidence", snapshot: `{"Id":"infra-container","Image":"","IsInfra":true}`, wantOutcome: true},
				{name: "When historical events lack snapshots it should inspect the infra container", inspection: `[{"Image":"","IsInfra":true}]`, wantOutcome: true},
				{name: "When the infra container has been removed without evidence it should withhold the outcome"},
				{name: "When an image-less container is not infra it should withhold the outcome", snapshot: `{"Id":"infra-container","Image":"","IsInfra":false}`, inspection: `[{"Image":"","IsInfra":false}]`},
				{name: "When a snapshot belongs to another container it should withhold the outcome", snapshot: `{"Id":"another-container","Image":"","IsInfra":true}`},
				{name: "When the container ID changes it should discard the rootfs evidence", snapshot: `{"Id":"infra-container","Image":"","IsInfra":true}`, replaceContainer: true},
				{name: "When an infra snapshot identifies an image it should not exempt it", snapshot: `{"Id":"infra-container","Image":"config-id","IsInfra":true}`},
			} {
				t.Run(tc.name, func(t *testing.T) {
					require := require.New(t)
					const appName = "delta-pod"
					const imageRef = "quay.io/acme/web:target"
					const digest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
					store, rw, logger := newApplicationOutcomeStore(t)
					image := applicationOutcomeImage(imageRef, digest)
					targets := prepareApplicationOutcomeTargets(t, store, appName, []deltastatus.ApplicationImage{image})
					recordApplicationOutcome(t, store, appName, targets, image, v1beta1.DeviceDeltaApplyStatus{Outcome: v1beta1.DeviceDeltaApplyOutcomeApplied})
					// A new monitor models an agent restart and replay from persisted events.
					for replay := 0; replay < 2; replay++ {
						mgr, _ := newApplicationOutcomeManager(t, appName, [][]deltastatus.ApplicationImage{{image}}, store, logger)
						app := mgr.podmanMonitor.apps[appName].(*application)
						app.status.AppType = appType
						ctrl := gomock.NewController(t)
						defer ctrl.Finish()
						systemdManager := systemd.NewMockManager(ctrl)
						systemdManager.EXPECT().Show(gomock.Any(), gomock.Any(), gomock.Any()).Return([]string{"0"}, nil).AnyTimes()
						exec := executer.NewMockExecuter(ctrl)
						if tc.snapshot == "" && tc.inspection != "" {
							exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "inspect", "infra-container").Return(tc.inspection, "", 0).Times(1)
						} else if !tc.wantOutcome {
							exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "inspect", gomock.Any()).Return(tc.inspection, "no such object", 125).AnyTimes()
						}
						podman := client.NewPodman(logger, exec, rw, poll.Config{})
						mgr.podmanMonitor.log = logger
						mgr.podmanMonitor.clientFactory = func(v1beta1.Username) (*client.Podman, error) { return podman, nil }
						mgr.podmanMonitor.systemdFactory = func(v1beta1.Username) (systemd.Manager, error) { return systemdManager, nil }
						// Real default Podman infra events have no Image, even with inspect data.
						var event client.PodmanEvent
						raw := map[string]any{"Type": "container", "ID": "infra-container", "Name": "pod-infra", "Attributes": map[string]string{"io.flightctl.quadlet.project": app.ID(), quadletSystemdLabel: "app-pod.service"}}
						if tc.snapshot != "" {
							raw["ContainerInspectData"] = tc.snapshot
						}
						data, err := json.Marshal(raw)
						require.NoError(err)
						require.NoError(json.Unmarshal(data, &event))
						for _, status := range []string{"create", "init", "start", "died", "remove"} {
							event.Status = status
							if status != "create" {
								event.ContainerInspectData = nil
							}
							exitCode := 0
							event.ContainerExitCode = &exitCode
							mgr.podmanMonitor.handleEvent(t.Context(), event)
						}
						if tc.replaceContainer {
							event.ID = "replacement-container"
							event.Status = "start"
							mgr.podmanMonitor.handleEvent(t.Context(), event)
						}
						reported := &v1beta1.DeviceStatus{}
						require.NoError(mgr.Status(t.Context(), reported))
						require.Len(reported.Applications, 1)
						require.Equal(v1beta1.ApplicationStatusRunning, reported.Applications[0].Status)
						require.Contains(*reported.Applications[0].ImageDigests, v1beta1.ApplicationImageDigest{Image: imageRef, Digest: digest})
						if !tc.wantOutcome {
							require.Nil(reported.Applications[0].LastDelta)
							continue
						}
						require.NotNil(reported.Applications[0].LastDelta, "healthy application has not reported a delta apply outcome")
						require.Equal(v1beta1.DeviceDeltaApplyOutcomeApplied, reported.Applications[0].LastDelta.Outcome)
					}
				})
			}
		})
	}
}
