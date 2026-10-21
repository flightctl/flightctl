package dependency

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/deltastatus"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/agent/device/resource"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/poll"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestPrefetchSeparatesRuntimeStores(t *testing.T) {
	const image = "quay.io/acme/workload:target"
	const hint = "quay.io/acme/delta:target"
	tests := []struct {
		name    string
		reverse bool
	}{
		{name: "When Podman and CRI share a reference and owner it should update both stores and preserve separate outcomes"},
		{name: "When CRI is collected first it should update both stores and preserve separate outcomes", reverse: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			exec := executer.NewMockExecuter(ctrl)
			logger := log.NewPrefixLogger("test")
			root := t.TempDir()
			rw := fileio.NewReadWriter(fileio.NewReader(fileio.WithReaderRootDir(root)), fileio.NewWriter(fileio.WithWriterRootDir(root)))
			podman := client.NewPodman(logger, exec, rw, poll.NewConfig(time.Millisecond, 1))
			cri := client.NewCRI(logger, exec, rw, poll.NewConfig(time.Millisecond, 1))
			skopeo := client.NewSkopeo(logger, exec, rw)
			resources := resource.NewMockManager(ctrl)
			resources.EXPECT().IsCriticalAlert(resource.MonitorType(resource.DiskMonitorType)).Return(false)
			exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "exists", image).Return("", "", 1)
			exec.EXPECT().ExecuteWithContext(gomock.Any(), "crictl", "images", image).Return("", "", 1)
			exec.EXPECT().ExecuteWithContext(gomock.Any(), "skopeo", "inspect", "--raw", "docker://"+hint).Return(podmanDeltaManifest, "", 0).Times(2)
			expectPodmanDeltaTarget(t, exec, image, 2)
			expectPodmanDeltaSource(exec, 1)
			exec.EXPECT().ExecuteWithContext(gomock.Any(), "skopeo", "copy", "docker://"+hint, gomock.Any()).Return("", "", 0)
			exec.EXPECT().ExecuteWithContext(gomock.Any(), "oci-delta", "import", gomock.Any()).Return("", "", 0)
			exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "pull", "quay.io/acme/workload@"+podmanDeltaTargetDigest).Return(podmanDeltaImageID, "", 0)
			expectPodmanDeltaActivation(exec, image)
			exec.EXPECT().ExecuteWithContext(
				gomock.Any(), "podman", "image", "inspect", "--format", "{{.Digest}}", podmanDeltaImageID,
			).Return(podmanDeltaTargetDigest, "", 0)
			// The source exists in Podman only. CRI must recover with its own pull.
			exec.EXPECT().ExecuteWithContext(gomock.Any(), "crictl", "images", "--output", "json").Return(`{"images":[]}`, "", 0)
			exec.EXPECT().ExecuteWithContextFromDir(gomock.Any(), "", "crictl", []string{"pull", image}, gomock.Any()).Return("", "", 0)
			exec.EXPECT().ExecuteWithContext(gomock.Any(), "crictl", "inspecti", "--output", "json", image).Return(`{"status":{"repoDigests":[]}}`, "", 0).AnyTimes()
			targets := []OCIPullTarget{
				{Type: OCITypePodmanImage, Reference: image, Delta: &OCIDeltaTarget{Hint: hint, Applications: []string{"podman", "mixed"}}},
				{Type: OCITypeCRIImage, Reference: image, Delta: &OCIDeltaTarget{Hint: hint, Applications: []string{"cri", "mixed"}}},
			}
			if tt.reverse {
				slices.Reverse(targets)
			}
			var apps []v1beta1.ApplicationProviderSpec
			for _, name := range []string{"podman", "cri", "mixed"} {
				app := v1beta1.ContainerApplication{Name: &name, AppType: v1beta1.AppTypeContainer}
				require.NoError(app.FromImageApplicationProviderSpec(v1beta1.ImageApplicationProviderSpec{Image: image}))
				var spec v1beta1.ApplicationProviderSpec
				require.NoError(spec.FromContainerApplication(app))
				apps = append(apps, spec)
			}
			desired := &v1beta1.DeviceSpec{Applications: &apps}
			store := deltastatus.New(rw, "/var/lib/flightctl", logger)
			manager := NewPrefetchManager(logger,
				func(v1beta1.Username) (*client.Podman, error) { return podman, nil },
				func(v1beta1.Username) (*client.Skopeo, error) { return skopeo, nil },
				client.NewCLIClients(client.WithCRIClient(cri)), rw, util.Duration(time.Minute), resources, poll.Config{},
				WithOCIDelta(client.NewOCIDelta(logger, exec, time.Minute)), WithDeltaStatusStore(store))
			defer manager.Cleanup()
			manager.RegisterOCICollector(newTestOCICollector(func(context.Context, *v1beta1.DeviceSpec, *v1beta1.DeviceSpec, ...OCICollectOpt) (*OCICollection, error) {
				return &OCICollection{Targets: OCIPullTargetsByUser{v1beta1.CurrentProcessUsername: targets}}, nil
			}))
			require.ErrorIs(manager.BeforeUpdate(t.Context(), nil, desired), errors.ErrPrefetchNotReady)
			require.Len(manager.queue, 2)
			// Completing one runtime must not make the other runtime ready.
			manager.processTarget(t.Context(), <-manager.queue)
			require.ErrorIs(manager.checkReady(t.Context()), errors.ErrPrefetchNotReady)
			manager.processTarget(t.Context(), <-manager.queue)
			require.NoError(manager.checkReady(t.Context()))
			checkStatus := func(manager *prefetchManager) {
				status := &v1beta1.DeviceStatus{Applications: []v1beta1.DeviceApplicationStatus{{Name: "podman"}, {Name: "cri"}, {Name: "mixed"}}}
				require.NoError(collectPrefetchTaskOutcomes(t, manager, t.Context(), status))
				for i, outcome := range []v1beta1.DeviceDeltaApplyOutcomeType{v1beta1.DeviceDeltaApplyOutcomeApplied, v1beta1.DeviceDeltaApplyOutcomeFallback, v1beta1.DeviceDeltaApplyOutcomePartial} {
					require.NotNil(status.Applications[i].LastDelta)
					require.Equal(outcome, status.Applications[i].LastDelta.Outcome)
				}
				require.Contains(*status.Applications[1].LastDelta.FallbackReason, "source image is unavailable")
			}
			checkStatus(manager)
			// A fresh manager restores results from disk, independently for both stores.
			restarted := &prefetchManager{
				log: logger, deltaStatusStore: deltastatus.New(rw, "/var/lib/flightctl", logger),
				deltaAppSpecKeys: manager.deltaAppSpecKeys, deltaAppTargetKeys: manager.deltaAppTargetKeys,
				deltaTargetRefs: manager.deltaTargetRefs, deltaAppLifetimes: manager.deltaAppLifetimes, deltaTargetsScheduled: true,
			}
			restarted.deltaApplyResults = restarted.restoreApplicationDeltaResults(restarted.deltaAppTargetKeys, restarted.deltaTargetRefs)
			checkStatus(restarted)
		})
	}
}

func TestWhenCachedPodmanImageMatchesItShouldInspectIdentityAndDigestOnce(t *testing.T) {
	require := require.New(t)
	const (
		appName = "web"
		image   = "quay.io/acme/web:latest"
		digest  = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		imageID = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	)

	ctx := context.Background()
	logger := log.NewPrefixLogger("test")
	root := t.TempDir()
	rw := fileio.NewReadWriter(fileio.NewReader(fileio.WithReaderRootDir(root)), fileio.NewWriter(fileio.WithWriterRootDir(root)))
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	exec := executer.NewMockExecuter(ctrl)
	resources := resource.NewMockManager(ctrl)
	resources.EXPECT().IsCriticalAlert(resource.MonitorType(resource.DiskMonitorType)).Return(false)
	podman := client.NewPodman(logger, exec, rw, poll.NewConfig(time.Millisecond, 1))
	skopeo := client.NewSkopeo(logger, exec, rw)

	firstExists := exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "exists", image).Return("", "", 0)
	secondExists := exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "exists", image).Return("", "", 0)
	localMetadata := exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "inspect", "--format", "{{json .}}", image).
		Return(fmt.Sprintf(`{"Id":%q,"Digest":%q,"RepoDigests":[%q]}`, imageID, digest, "quay.io/acme/web@"+digest), "", 0)
	gomock.InOrder(firstExists, secondExists, localMetadata)

	name := appName
	app := v1beta1.ContainerApplication{Name: &name, AppType: v1beta1.AppTypeContainer}
	require.NoError(app.FromImageApplicationProviderSpec(v1beta1.ImageApplicationProviderSpec{Image: image}))
	var appSpec v1beta1.ApplicationProviderSpec
	require.NoError(appSpec.FromContainerApplication(app))
	desired := &v1beta1.DeviceSpec{Applications: &[]v1beta1.ApplicationProviderSpec{appSpec}}
	store := deltastatus.New(rw, "/var/lib/flightctl", logger)
	manager := NewPrefetchManager(logger,
		func(v1beta1.Username) (*client.Podman, error) { return podman, nil },
		func(v1beta1.Username) (*client.Skopeo, error) { return skopeo, nil },
		client.NewCLIClients(), rw, util.Duration(time.Minute), resources, poll.Config{},
		WithDeltaStatusStore(store))
	defer manager.Cleanup()
	manager.RegisterOCICollector(newTestOCICollector(func(context.Context, *v1beta1.DeviceSpec, *v1beta1.DeviceSpec, ...OCICollectOpt) (*OCICollection, error) {
		return &OCICollection{Targets: OCIPullTargetsByUser{v1beta1.RootUsername: []OCIPullTarget{{
			Type: OCITypePodmanImage, Reference: image, Digest: digest,
			Delta: &OCIDeltaTarget{Application: appName},
		}}}}, nil
	}))

	require.NoError(manager.BeforeUpdate(ctx, nil, desired))
	require.True(manager.IsReady(ctx))
	result := store.ObservedApplicationResult(appName, []deltastatus.ApplicationImage{{
		Image: image, Owner: string(v1beta1.RootUsername), OCIType: string(OCITypePodmanImage), Digest: digest,
	}}, true)
	require.NotNil(result)
	require.Equal(v1beta1.DeviceDeltaApplyOutcomeNotRequired, result.Outcome)
}

func TestWhenAnApplicationReusesItsLauncherItShouldUpgradeObservedHistoryAcrossRestart(t *testing.T) {
	require := require.New(t)
	const (
		appName         = "vm"
		image           = "quay.io/kubevirt/virt-launcher:v1.9.0"
		digest          = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		platformDigest  = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		unrelatedDigest = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	)
	makeSpec := func(disk string) *v1beta1.DeviceSpec {
		name := appName
		content := "[Container]\nImage=" + image + "\n"
		diskContent := "[Image]\nImage=" + disk + "\n"
		quadlet := v1beta1.QuadletApplication{Name: &name, AppType: v1beta1.AppTypeQuadlet}
		require.NoError(quadlet.FromInlineApplicationProviderSpec(v1beta1.InlineApplicationProviderSpec{Inline: []v1beta1.ApplicationContent{
			{Path: "launcher.container", Content: &content}, {Path: "disk.image", Content: &diskContent},
		}}))
		var app v1beta1.ApplicationProviderSpec
		require.NoError(app.FromQuadletApplication(quadlet))
		return &v1beta1.DeviceSpec{Applications: &[]v1beta1.ApplicationProviderSpec{app}}
	}
	current := makeSpec("quay.io/containerdisks/fedora:40")
	desired := makeSpec("quay.io/containerdisks/fedora:41")
	logger := log.NewPrefixLogger("test")
	root := t.TempDir()
	rw := fileio.NewReadWriter(fileio.NewReader(fileio.WithReaderRootDir(root)), fileio.NewWriter(fileio.WithWriterRootDir(root)))
	store := deltastatus.New(rw, "/state", logger)
	keys, err := applicationDeltaSpecKeys(current, nil)
	require.NoError(err)
	ref := imageRef{image: image, owner: v1beta1.RootUsername, ociType: OCITypePodmanImage}
	targetKey := applicationImageTargetKeyFor(ref, "", ref.ociType)
	require.NoError(store.ReconcileApplicationSpecs(keys))
	require.NoError(store.ReconcileApplicationTargets(map[string]map[string]string{appName: {ref.targetID(): targetKey}}, true))
	observed := deltastatus.ApplicationImage{Image: image, Owner: "root", OCIType: string(ref.ociType), Digest: digest}
	require.NoError(store.RecordApplicationImageResult(appName, keys[appName], ref.targetID(), targetKey, observed,
		v1beta1.DeviceDeltaApplyStatus{Outcome: v1beta1.DeviceDeltaApplyOutcomeNotUsed}))
	require.Equal(v1beta1.DeviceDeltaApplyOutcomeNotUsed, store.ObservedApplicationResult(appName, []deltastatus.ApplicationImage{observed}, true).Outcome)

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	exec := executer.NewMockExecuter(ctrl)
	// Only local cache inspection is allowed; registry inspection or a pull fails the mock.
	exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "exists", image).Return("", "", 0).Times(3)
	exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "inspect", "--format", "{{json .}}", image).
		Return(fmt.Sprintf(`{"Id":"immutable-launcher","Digest":%q,"RepoDigests":[%q,%q]}`, platformDigest, "quay.io/kubevirt/virt-launcher@"+digest, "quay.io/other/image@"+unrelatedDigest), "", 0).Times(2)
	podman := client.NewPodman(logger, exec, rw, poll.Config{})
	skopeo := client.NewSkopeo(logger, exec, rw)
	resources := resource.NewMockManager(ctrl)
	resources.EXPECT().IsCriticalAlert(gomock.Any()).Return(false).Times(2)
	manager := NewPrefetchManager(logger,
		func(v1beta1.Username) (*client.Podman, error) { return podman, nil },
		func(v1beta1.Username) (*client.Skopeo, error) { return skopeo, nil },
		client.NewCLIClients(), rw, util.Duration(time.Second), resources, poll.Config{}, WithDeltaStatusStore(store))
	defer manager.Cleanup()
	manager.RegisterOCICollector(newTestOCICollector(func(context.Context, *v1beta1.DeviceSpec, *v1beta1.DeviceSpec, ...OCICollectOpt) (*OCICollection, error) {
		return &OCICollection{Targets: OCIPullTargetsByUser{v1beta1.RootUsername: {{
			Reference: image, Type: OCITypePodmanImage, PullPolicy: v1beta1.PullIfNotPresent,
			Delta: &OCIDeltaTarget{Application: appName},
		}}}}, nil
	}))
	require.NoError(manager.BeforeUpdate(t.Context(), current, desired))
	require.NoError(manager.BeforeUpdate(t.Context(), desired, desired))
	// Application publication reads observed history, rather than prefetch's result map.
	for _, selected := range []*deltastatus.Store{store, deltastatus.New(rw, "/state", logger)} {
		result := selected.ObservedApplicationResult(appName, []deltastatus.ApplicationImage{observed}, true)
		require.NotNil(result)
		require.Equal(v1beta1.DeviceDeltaApplyOutcomeNotRequired, result.Outcome)
		platform := observed
		platform.Digest = platformDigest
		require.Equal(v1beta1.DeviceDeltaApplyOutcomeNotRequired, selected.ObservedApplicationResult(appName, []deltastatus.ApplicationImage{platform}, true).Outcome)
		unrelated := observed
		unrelated.Digest = unrelatedDigest
		require.Nil(selected.ObservedApplicationResult(appName, []deltastatus.ApplicationImage{unrelated}, true))
	}
}
