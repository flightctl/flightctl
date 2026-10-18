package applications

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/applications/provider"
	"github.com/flightctl/flightctl/internal/agent/device/deltastatus"
	"github.com/flightctl/flightctl/internal/agent/device/dependency"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/poll"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestWhenStatusRunsDuringApplicationUpdatesItShouldPreserveVolumeObservations(t *testing.T) {
	require := require.New(t)
	binDir := t.TempDir()
	for _, name := range []string{"podman", "podman-compose", "skopeo"} {
		path := filepath.Join(binDir, name)
		require.NoError(os.WriteFile(path, []byte("#!/bin/sh\n"), 0o600))
		require.NoError(os.Chmod(path, 0o700))
	}
	t.Setenv("PATH", binDir)
	logger := log.NewPrefixLogger("test")
	logger.SetLevel(logrus.ErrorLevel)
	root := t.TempDir()
	rw := fileio.NewReadWriter(fileio.NewReader(fileio.WithReaderRootDir(root)), fileio.NewWriter(fileio.WithWriterRootDir(root)))
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockExec := executer.NewMockExecuter(ctrl)
	podman := client.NewPodman(logger, mockExec, rw, poll.Config{})
	podmanFactory := func(v1beta1.Username) (*client.Podman, error) { return podman, nil }
	rwFactory := func(v1beta1.Username) (fileio.ReadWriter, error) { return rw, nil }
	desired := newTestDeviceWithApplications(t, "app", []testInlineDetails{{Content: "services: {}\n", Path: "compose.yaml"}})
	providers, err := provider.FromDeviceSpec(t.Context(), logger, podmanFactory, nil, rwFactory, desired)
	require.NoError(err)
	appProvider := providers[0]
	const (
		volumeID = "app-data"
		imageRef = "quay.io/acme/data:v1"
		imageID  = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
		digest   = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	)
	appProvider.Spec().Volume.Add(&provider.Volume{
		Name: "data", ID: volumeID, Reference: imageRef, OCIType: dependency.OCITypePodmanImage, Available: true,
	})
	store := deltastatus.New(rw, "/var/lib/flightctl", logger)
	m := &manager{
		log: logger, rwFactory: rwFactory, podmanFactory: podmanFactory, deltaStatusStore: store,
		podmanMonitor:     &PodmanMonitor{apps: make(map[string]Application)},
		kubernetesMonitor: &KubernetesMonitor{monitor: newMonitor(logger, "kubernetes")},
	}
	require.NoError(m.Ensure(t.Context(), appProvider))
	inspection, err := json.Marshal(client.PodmanVolumeInspect{
		Name: volumeID, Driver: "image", CreatedAt: "2026-10-16T12:00:00Z",
		Options: map[string]string{"image": imageID},
		Labels: map[string]string{
			client.PodmanDeltaVolumeApplicationLabel: appProvider.ID(),
			client.PodmanDeltaVolumeLifetimeLabel:    store.ApplicationLifetime("app"),
			client.PodmanDeltaVolumeReferenceLabel:   imageRef,
			client.PodmanDeltaVolumeOwnerLabel:       "root",
			client.PodmanDeltaVolumeOCITypeLabel:     string(dependency.OCITypePodmanImage),
			client.PodmanDeltaVolumeDigestLabel:      digest,
		},
	})
	require.NoError(err)
	mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "volume", "inspect", volumeID, "--format", "{{json .}}").Return(string(inspection), "", 0)
	mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "inspect", "--format", "{{.Digest}}", imageID).Return(digest, "", 0).AnyTimes()
	require.NoError(m.Status(t.Context(), &v1beta1.DeviceStatus{}))

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	statusInspecting := make(chan struct{})
	firstUpdateDone := make(chan struct{})
	var firstObservation sync.Once
	mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "volume", "inspect", volumeID, "--format", "{{json .}}").
		DoAndReturn(func(context.Context, string, ...string) (string, string, int) {
			firstObservation.Do(func() {
				close(statusInspecting)
				select {
				case <-firstUpdateDone:
				case <-ctx.Done():
				}
			})
			return string(inspection), "", 0
		}).AnyTimes()

	statusDone := make(chan struct{})
	updatesDone := make(chan error, 1)
	go func() {
		// Start an update while Status is inspecting the volume, then continue
		// exercising cache invalidation alongside the remaining status reads.
		select {
		case <-statusInspecting:
		case <-ctx.Done():
			updatesDone <- ctx.Err()
			return
		}
		err := m.Update(ctx, appProvider)
		close(firstUpdateDone)
		if err != nil {
			updatesDone <- err
			return
		}
		for {
			select {
			case <-statusDone:
				updatesDone <- nil
				return
			case <-ctx.Done():
				updatesDone <- ctx.Err()
				return
			default:
			}
			if err := m.Update(ctx, appProvider); err != nil {
				updatesDone <- err
				return
			}
		}
	}()
	for range 100 {
		reported := &v1beta1.DeviceStatus{}
		require.NoError(m.Status(ctx, reported))
		require.Len(reported.Applications, 1)
		require.NotNil(reported.Applications[0].ImageDigests)
		require.Contains(*reported.Applications[0].ImageDigests, v1beta1.ApplicationImageDigest{Image: imageRef, Digest: digest})
	}
	close(statusDone)
	select {
	case err := <-updatesDone:
		require.NoError(err)
	case <-ctx.Done():
		t.Fatal("application updates did not finish before the deadline")
	}
}
