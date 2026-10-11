package dependency

import (
	"context"
	"fmt"
	"testing"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/deltastatus"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/poll"
	"github.com/stretchr/testify/require"
)

func historyDigest(n int) string { return fmt.Sprintf("sha256:%064x", n) }
func seedHistory(t *testing.T, s *deltastatus.Store, app, ref string, n int, outcome v1beta1.DeviceDeltaApplyOutcomeType) {
	t.Helper()
	require.NoError(t, s.ReconcileApplicationSpecs(map[string]string{app: "spec"}))
	c, ok := s.Context(app, ref, "podman", "root", "containers-storage")
	require.True(t, ok)
	require.NoError(t, s.RecordApplicationResult(c, historyDigest(n), v1beta1.DeviceDeltaApplyStatus{Outcome: outcome}))
}
func setObservation(s *deltastatus.Store, app string, complete bool) {
	observations := make(map[string]deltastatus.ApplicationObservation)
	snapshot := s.ApplicationSnapshot(app)
	if snapshot != nil {
		for c := range snapshot.Histories {
			for n := 0; n <= 30; n++ {
				digest := historyDigest(n)
				if n == 0 {
					digest = ""
				}
				observations[deltastatus.ObservationKey(app, c.Reference, digest)] = deltastatus.ApplicationObservation{Application: app, Runtime: "podman", Owner: "root", Destination: "containers-storage", Complete: complete}
			}
		}
	}
	s.SetApplicationObservations(observations)
}
func TestApplicationDeltaStatusLooksUpReportedDigest(t *testing.T) {
	require := require.New(t)
	root := t.TempDir()
	rw := fileio.NewReadWriter(fileio.NewReader(fileio.WithReaderRootDir(root)), fileio.NewWriter(fileio.WithWriterRootDir(root)))
	logger := log.NewPrefixLogger("test")
	s := deltastatus.New(rw, "/state", logger)
	ref := "quay.io/acme/app:latest"
	seedHistory(t, s, "app", ref, 1, v1beta1.DeviceDeltaApplyOutcomeFallback)
	seedHistory(t, s, "app", ref, 2, v1beta1.DeviceDeltaApplyOutcomeApplied)
	task := &prefetchTask{done: false}
	m := &prefetchManager{log: logger, deltaStatusStore: s, tasks: map[imageRef]*prefetchTask{{image: ref}: task}}
	for _, n := range []int{1, 0, 2, 1} {
		setObservation(s, "app", true)
		digests := []v1beta1.ApplicationImageDigest{{Image: ref}}
		if n > 0 {
			digests[0].Digest = historyDigest(n)
		}
		status := &v1beta1.DeviceStatus{Applications: []v1beta1.DeviceApplicationStatus{{Name: "app", ImageDigests: &digests}}}
		require.NoError(m.Status(context.Background(), status))
		if n == 0 {
			require.Nil(status.Applications[0].LastDelta)
		} else {
			require.NotNil(status.Applications[0].LastDelta)
			want := v1beta1.DeviceDeltaApplyOutcomeApplied
			if n == 1 {
				want = v1beta1.DeviceDeltaApplyOutcomeFallback
			}
			require.Equal(want, status.Applications[0].LastDelta.Outcome)
		}
		require.Same(task, m.tasks[imageRef{image: ref}])
		require.False(task.done)
	}
	restarted := deltastatus.New(rw, "/state", logger)
	m.deltaStatusStore = restarted
	setObservation(restarted, "app", true)
	digests := []v1beta1.ApplicationImageDigest{{Image: ref, Digest: historyDigest(2)}}
	status := &v1beta1.DeviceStatus{Applications: []v1beta1.DeviceApplicationStatus{{Name: "app", ImageDigests: &digests}}}
	require.NoError(m.Status(context.Background(), status))
	require.Equal(v1beta1.DeviceDeltaApplyOutcomeApplied, status.Applications[0].LastDelta.Outcome)
}
func TestApplicationDeltaStatusAggregatesCurrentDigests(t *testing.T) {
	ref := "quay.io/acme/app:latest"
	s := deltastatus.NewInMemory(log.NewPrefixLogger("test"))
	seedHistory(t, s, "app", ref, 1, v1beta1.DeviceDeltaApplyOutcomeFallback)
	seedHistory(t, s, "app", ref, 2, v1beta1.DeviceDeltaApplyOutcomeApplied)
	for _, tt := range []struct {
		name     string
		images   []v1beta1.ApplicationImageDigest
		complete bool
		want     v1beta1.DeviceDeltaApplyOutcomeType
	}{
		{"When rollout A and B run it should aggregate both", []v1beta1.ApplicationImageDigest{{Image: ref, Digest: historyDigest(2)}, {Image: ref, Digest: historyDigest(1)}, {Image: ref, Digest: historyDigest(2)}}, true, v1beta1.DeviceDeltaApplyOutcomePartial},
		{"When a known image and unknown observation coexist it should omit", []v1beta1.ApplicationImageDigest{{Image: ref, Digest: historyDigest(2)}, {Image: ref}}, true, ""},
		{"When a current digest misses it should omit", []v1beta1.ApplicationImageDigest{{Image: ref, Digest: historyDigest(2)}, {Image: ref, Digest: historyDigest(3)}}, true, ""},
		{"When runtime snapshot is incomplete it should omit", []v1beta1.ApplicationImageDigest{{Image: ref, Digest: historyDigest(2)}}, false, ""},
		{"When there are no current images it should omit", nil, true, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			setObservation(s, "app", tt.complete)
			m := &prefetchManager{deltaStatusStore: s, log: log.NewPrefixLogger("test")}
			status := &v1beta1.DeviceStatus{Applications: []v1beta1.DeviceApplicationStatus{{Name: "app", ImageDigests: &tt.images}}}
			require.NoError(t, m.Status(context.Background(), status))
			if tt.want == "" {
				require.Nil(t, status.Applications[0].LastDelta)
			} else {
				require.Equal(t, tt.want, status.Applications[0].LastDelta.Outcome)
			}
		})
	}
}
func TestApplicationDeltaStatusRetainsOldActiveReference(t *testing.T) {
	s := deltastatus.NewInMemory(log.NewPrefixLogger("test"))
	old := "quay.io/acme/app:old"
	seedHistory(t, s, "app", old, 1, v1beta1.DeviceDeltaApplyOutcomeApplied)
	require.NoError(t, s.ReconcileApplicationSpecs(map[string]string{"app": "new-spec"}))
	seedHistory(t, s, "app", "quay.io/acme/app:new", 2, v1beta1.DeviceDeltaApplyOutcomeNotUsed)
	setObservation(s, "app", true)
	images := []v1beta1.ApplicationImageDigest{{Image: old, Digest: historyDigest(1)}}
	status := &v1beta1.DeviceStatus{Applications: []v1beta1.DeviceApplicationStatus{{Name: "app", ImageDigests: &images}}}
	m := &prefetchManager{deltaStatusStore: s, log: log.NewPrefixLogger("test")}
	require.NoError(t, m.Status(context.Background(), status))
	require.Equal(t, v1beta1.DeviceDeltaApplyOutcomeApplied, status.Applications[0].LastDelta.Outcome)
}

func TestApplicationDeltaPrefetchStorageContextsDoNotCoalesce(t *testing.T) {
	root := t.TempDir()
	rw := fileio.NewReadWriter(fileio.NewReader(fileio.WithReaderRootDir(root)), fileio.NewWriter(fileio.WithWriterRootDir(root)))
	require.NoError(t, rw.MkdirAll("/configs", 0700))
	require.NoError(t, rw.WriteFile("/configs/one", []byte("image-endpoint: unix:///run/one.sock\n"), 0600))
	require.NoError(t, rw.WriteFile("/configs/two", []byte("image-endpoint: unix:///run/two.sock\n"), 0600))
	logger := log.NewPrefixLogger("test")
	cri := client.NewCRI(logger, nil, rw, poll.NewConfig(0, 1))
	m := &prefetchManager{cliClients: client.NewCLIClients(client.WithCRIClient(cri))}
	target := OCIPullTarget{Reference: "quay.io/acme/app:latest", Type: OCITypeCRIImage, ClientOptsFn: func() []client.ClientOption { return []client.ClientOption{client.WithCRIConfig("/configs/one")} }}
	first := m.targetImageRef("", target)
	target.ClientOptsFn = func() []client.ClientOption { return []client.ClientOption{client.WithCRIConfig("/configs/two")} }
	second := m.targetImageRef("", target)
	require.NotEqual(t, first, second)
	target.Type = OCITypePodmanImage
	third := m.targetImageRef("", target)
	require.NotEqual(t, first, third)
}
