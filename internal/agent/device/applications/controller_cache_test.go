package applications

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/applications/provider"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/quadlet"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/poll"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestWhenParentReferenceMovesAfterPreparationItShouldInstallCollectedContents(t *testing.T) {
	const (
		parentRef = "quay.io/acme/bundle:latest"
		oldID     = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
		newID     = "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
		oldDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		newDigest = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	)
	for _, tc := range []struct {
		name     string
		appType  v1beta1.AppType
		filename string
		original string
		moved    string
	}{
		{
			name:    "When a Compose parent reference moves it should install its collected contents",
			appType: v1beta1.AppTypeCompose, filename: "podman-compose.yaml",
			original: "services:\n  web:\n    image: quay.io/acme/web:v1\n",
			moved:    "services:\n  web:\n    image: quay.io/acme/web:v2\n",
		},
		{
			name:    "When a Quadlet parent reference moves it should install its collected contents",
			appType: v1beta1.AppTypeQuadlet, filename: "web.container",
			original: "[Container]\nImage=quay.io/acme/web:v1\n",
			moved:    "[Container]\nImage=quay.io/acme/web:v2\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			binDir := t.TempDir()
			for _, name := range []string{"podman", "podman-compose", "skopeo"} {
				path := filepath.Join(binDir, name)
				require.NoError(os.WriteFile(path, []byte("#!/bin/sh\n"), 0o600))
				require.NoError(os.Chmod(path, 0o700))
			}
			t.Setenv("PATH", binDir)
			root := t.TempDir()
			rw := fileio.NewReadWriter(fileio.NewReader(fileio.WithReaderRootDir(root)), fileio.NewWriter(fileio.WithWriterRootDir(root)))
			for path, content := range map[string]string{"/parent-v1": tc.original, "/parent-v2": tc.moved} {
				require.NoError(rw.MkdirAll(path, fileio.DefaultDirectoryPermissions))
				require.NoError(rw.WriteFile(filepath.Join(path, tc.filename), []byte(content), fileio.DefaultFilePermissions))
			}
			var apiSpec v1beta1.ApplicationProviderSpec
			imageSpec := v1beta1.ImageApplicationProviderSpec{Image: parentRef}
			if tc.appType == v1beta1.AppTypeCompose {
				app := v1beta1.ComposeApplication{Name: lo.ToPtr("app"), AppType: tc.appType}
				require.NoError(app.FromImageApplicationProviderSpec(imageSpec))
				require.NoError(apiSpec.FromComposeApplication(app))
			} else {
				app := v1beta1.QuadletApplication{Name: lo.ToPtr("app"), AppType: tc.appType}
				require.NoError(app.FromImageApplicationProviderSpec(imageSpec))
				require.NoError(apiSpec.FromQuadletApplication(app))
			}
			desired := &v1beta1.DeviceSpec{Applications: &[]v1beta1.ApplicationProviderSpec{apiSpec}}
			logger := log.NewPrefixLogger("test")
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			mockExec := executer.NewMockExecuter(ctrl)
			selectedID := oldID
			mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "--version").Return("podman version 5.8.0", "", 0).AnyTimes()
			mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "exists", parentRef).Return("", "", 0).AnyTimes()
			mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "inspect", "--format", "{{.Id}}", parentRef).
				DoAndReturn(func(context.Context, string, ...string) (string, string, int) { return selectedID, "", 0 }).AnyTimes()
			for _, image := range []struct{ id, digest, mount string }{{oldID, oldDigest, "/parent-v1"}, {newID, newDigest, "/parent-v2"}} {
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "inspect", "--format", "{{.Digest}}", image.id).Return(image.digest, "", 0).AnyTimes()
				if client.IsPodmanRootless() {
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "unshare", "podman", "image", "mount", image.id).Return(image.mount, "", 0).AnyTimes()
				} else {
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "mount", image.id).Return(image.mount, "", 0).AnyTimes()
				}
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "unmount", image.id).Return("", "", 0).AnyTimes()
			}
			labels, err := createTestLabels(map[string]string{AppTypeLabel: string(tc.appType)})
			require.NoError(err)
			mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "inspect", parentRef).Return(labels, "", 0).AnyTimes()
			podman := client.NewPodman(logger, mockExec, rw, poll.Config{})
			podmanFactory := func(v1beta1.Username) (*client.Podman, error) { return podman, nil }
			rwFactory := func(v1beta1.Username) (fileio.ReadWriter, error) { return rw, nil }
			m := &manager{
				log: logger, rwFactory: rwFactory, podmanFactory: podmanFactory,
				ociTargetCache: provider.NewOCITargetCache(), appDataCache: provider.NewAppDataCache(),
				podmanMonitor:     &PodmanMonitor{apps: make(map[string]Application)},
				kubernetesMonitor: &KubernetesMonitor{monitor: newMonitor(logger, "kubernetes")},
			}
			collection, err := m.CollectOCITargets(t.Context(), &v1beta1.DeviceSpec{}, desired)
			require.NoError(err)
			require.False(collection.Requeue)
			var preparedReferences []string
			for _, target := range collection.Targets.Iter() {
				preparedReferences = append(preparedReferences, target.Reference)
			}
			require.Contains(preparedReferences, "quay.io/acme/web:v1")
			require.NoError(m.BeforeUpdate(t.Context(), desired))

			selectedID = newID
			controller := NewController(podmanFactory, nil, m, rwFactory, logger, "2026-10-16T12:00:00Z",
				WithControllerAppDataCache(m.appDataCache))
			require.NoError(controller.Sync(t.Context(), &v1beta1.DeviceSpec{}, desired))
			providers, err := provider.FromDeviceSpec(t.Context(), logger, podmanFactory, nil, rwFactory, desired)
			require.NoError(err)
			installedFilename := tc.filename
			if tc.appType == v1beta1.AppTypeQuadlet {
				installedFilename = quadlet.NamespaceResource(providers[0].ID(), tc.filename)
			}
			installed, err := rw.ReadFile(filepath.Join(providers[0].Spec().Path, installedFilename))
			require.NoError(err)
			require.Contains(string(installed), "quay.io/acme/web:v1")
			require.NotContains(string(installed), "quay.io/acme/web:v2")
			reported := &v1beta1.DeviceStatus{}
			require.NoError(m.Status(t.Context(), reported))
			require.Len(reported.Applications, 1)
			require.Contains(*reported.Applications[0].ImageDigests, v1beta1.ApplicationImageDigest{Image: parentRef, Digest: oldDigest})
		})
	}
}
