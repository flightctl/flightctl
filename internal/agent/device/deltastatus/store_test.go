package deltastatus

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func testDigest(n int) string { return fmt.Sprintf("sha256:%064x", n) }
func testContext(t *testing.T, s *Store, app string) ApplicationContext {
	t.Helper()
	require.NoError(t, s.ReconcileApplicationSpecs(map[string]string{app: "spec"}))
	c, ok := s.Context(app, "quay.io/acme/app:latest", "podman", "root", "containers-storage")
	require.True(t, ok)
	return c
}
func lookup(t *testing.T, s *Store, c ApplicationContext, d string) *v1beta1.DeviceDeltaApplyStatus {
	t.Helper()
	r, err := s.LookupApplicationResults([]ApplicationLookup{{Context: c, Digest: d}})
	require.NoError(t, err)
	return r[0].Status
}
func TestStoreApplicationHistoryLRU(t *testing.T) {
	require := require.New(t)
	s := NewInMemory(log.NewPrefixLogger("test"))
	c := testContext(t, s, "app")
	for i := 1; i <= 8; i++ {
		require.NoError(s.RecordApplicationResult(c, testDigest(i), v1beta1.DeviceDeltaApplyStatus{Outcome: v1beta1.DeviceDeltaApplyOutcomeApplied}))
	}
	require.NotNil(lookup(t, s, c, testDigest(1)))
	require.Nil(lookup(t, s, c, testDigest(999)))
	require.Nil(lookup(t, s, c, ""))
	require.NoError(s.RecordApplicationResult(c, testDigest(9), v1beta1.DeviceDeltaApplyStatus{Outcome: v1beta1.DeviceDeltaApplyOutcomeNotUsed}))
	require.Nil(lookup(t, s, c, testDigest(2)))
	require.NotNil(lookup(t, s, c, testDigest(1)))
	reason := "fallback"
	require.NoError(s.RecordApplicationResult(c, testDigest(1), v1beta1.DeviceDeltaApplyStatus{Outcome: v1beta1.DeviceDeltaApplyOutcomeFallback, FallbackReason: &reason}))
	reason = "mutated"
	result := lookup(t, s, c, testDigest(1))
	require.Equal("fallback", *result.FallbackReason)
	*result.FallbackReason = "changed"
	require.Equal("fallback", *lookup(t, s, c, testDigest(1)).FallbackReason)
	snapshot := s.ApplicationSnapshot("app")
	snapshot.Histories[c][0].Digest = ""
	require.Len(s.ApplicationSnapshot("app").Histories[c], 8)
}
func TestStorePersistsApplicationDigestHistory(t *testing.T) {
	require := require.New(t)
	root := t.TempDir()
	rw := fileio.NewReadWriter(fileio.NewReader(fileio.WithReaderRootDir(root)), fileio.NewWriter(fileio.WithWriterRootDir(root)))
	logger := log.NewPrefixLogger("test")
	s := New(rw, "/state", logger)
	c := testContext(t, s, "app")
	require.NoError(s.SetOSTarget("os"))
	require.NoError(s.RecordOSResult("os", v1beta1.DeviceDeltaApplyStatus{Outcome: v1beta1.DeviceDeltaApplyOutcomeApplied}))
	for i := 1; i <= 8; i++ {
		require.NoError(s.RecordApplicationResult(c, testDigest(i), v1beta1.DeviceDeltaApplyStatus{Outcome: v1beta1.DeviceDeltaApplyOutcomeApplied}))
	}
	require.NotNil(lookup(t, s, c, testDigest(1)))
	require.NoError(s.ReconcileApplicationSpecs(map[string]string{"app": "changed"}))
	require.NoError(s.ReconcileApplicationSpecs(nil))
	restarted := New(rw, "/state", logger)
	require.NotNil(restarted.OSResult("os"))
	require.NoError(restarted.RecordApplicationResult(c, testDigest(9), v1beta1.DeviceDeltaApplyStatus{Outcome: v1beta1.DeviceDeltaApplyOutcomeNotUsed}))
	require.Nil(lookup(t, restarted, c, testDigest(2)))
	require.NotNil(lookup(t, restarted, c, testDigest(1)))
	require.NoError(restarted.ReconcileApplicationSpecs(nil))
	require.NoError(restarted.RetireApplicationContexts("app", c.Lifecycle))
	next := testContext(t, restarted, "app")
	require.NotEqual(c.Lifecycle, next.Lifecycle)
	require.Nil(lookup(t, restarted, c, testDigest(1)))
	require.Nil(lookup(t, restarted, next, testDigest(1)))
}
func TestStoreApplicationContextIsolation(t *testing.T) {
	s := NewInMemory(log.NewPrefixLogger("test"))
	c := testContext(t, s, "app")
	require.NoError(t, s.RecordApplicationResult(c, testDigest(1), v1beta1.DeviceDeltaApplyStatus{Outcome: v1beta1.DeviceDeltaApplyOutcomeApplied}))
	for _, change := range []func(*ApplicationContext){func(c *ApplicationContext) { c.Application = "other" }, func(c *ApplicationContext) { c.Reference = "quay.io/acme/other:latest" }, func(c *ApplicationContext) { c.Owner = "user" }, func(c *ApplicationContext) { c.Runtime = "cri" }, func(c *ApplicationContext) { c.Destination = "other-store" }} {
		other := c
		change(&other)
		require.Nil(t, lookup(t, s, other, testDigest(1)))
	}
}
func TestStoreApplicationHistoryMigration(t *testing.T) {
	require := require.New(t)
	root := t.TempDir()
	rw := fileio.NewReadWriter(fileio.NewReader(fileio.WithReaderRootDir(root)), fileio.NewWriter(fileio.WithWriterRootDir(root)))
	for _, version := range []int{1, 99} {
		require.NoError(rw.MkdirAll("/state", 0700))
		data := fmt.Sprintf(`{"version":%d,"os":{"targetKey":"os","result":{"outcome":"Applied"}},"applications":{"app":{"specKey":"old","results":{"target":{"reportedDigest":"%s","status":{"outcome":"Applied"}}}}}}`, version, testDigest(1))
		require.NoError(rw.WriteFile("/state/"+FileName, []byte(data), 0600))
		s := New(rw, "/state", log.NewPrefixLogger("test"))
		require.Nil(s.ApplicationSnapshot("app"))
		if version == 1 {
			require.NotNil(s.OSResult("os"))
			saved, err := rw.ReadFile("/state/" + FileName)
			require.NoError(err)
			var state persistedState
			require.NoError(json.Unmarshal(saved, &state))
			require.Equal(2, state.Version)
		} else {
			require.Nil(s.OSResult("os"))
		}
	}
}
func TestStoreApplicationHistoryLoadValidation(t *testing.T) {
	require := require.New(t)
	root := t.TempDir()
	rw := fileio.NewReadWriter(fileio.NewReader(fileio.WithReaderRootDir(root)), fileio.NewWriter(fileio.WithWriterRootDir(root)))
	s := New(rw, "/state", log.NewPrefixLogger("test"))
	c := testContext(t, s, "app")
	h := applicationHistory{Context: c}
	for i := 1; i <= 10; i++ {
		h.Entries = append(h.Entries, ApplicationResult{Digest: testDigest(i), Status: v1beta1.DeviceDeltaApplyStatus{Outcome: v1beta1.DeviceDeltaApplyOutcomeApplied}})
	}
	h.Entries = append(h.Entries, ApplicationResult{Digest: testDigest(10), Status: v1beta1.DeviceDeltaApplyStatus{Outcome: v1beta1.DeviceDeltaApplyOutcomeFallback}}, ApplicationResult{Digest: "opaque", Status: v1beta1.DeviceDeltaApplyStatus{Outcome: v1beta1.DeviceDeltaApplyOutcomeApplied}})
	s.state.Histories = []applicationHistory{h, h}
	require.NoError(s.saveLocked())
	loaded := New(rw, "/state", log.NewPrefixLogger("test"))
	require.Len(loaded.ApplicationSnapshot("app").Histories[c], 8)
	require.Equal(v1beta1.DeviceDeltaApplyOutcomeFallback, lookup(t, loaded, c, testDigest(10)).Outcome)
	require.Nil(lookup(t, loaded, c, testDigest(1)))
}

func TestStoreApplicationHistorySaveFailure(t *testing.T) {
	require := require.New(t)
	s := NewInMemory(log.NewPrefixLogger("test"))
	c := testContext(t, s, "app")
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	rw := fileio.NewMockReadWriter(ctrl)
	s.readWriter = rw
	s.path = "/state/delta-status.json"
	rw.EXPECT().MkdirAll("/state", gomock.Any()).Return(fmt.Errorf("disk unavailable"))
	err := s.RecordApplicationResult(c, testDigest(1), v1beta1.DeviceDeltaApplyStatus{Outcome: v1beta1.DeviceDeltaApplyOutcomeApplied})
	require.Error(err)
	require.NotNil(lookup(t, s, c, testDigest(1)))
}
func TestStoreApplicationContextCapacity(t *testing.T) {
	s := NewInMemory(log.NewPrefixLogger("test"))
	first := testContext(t, s, "app")
	require.NoError(t, s.RecordApplicationResult(first, testDigest(1), v1beta1.DeviceDeltaApplyStatus{Outcome: v1beta1.DeviceDeltaApplyOutcomeApplied}))
	for i := 0; i < applicationContextCapacity; i++ {
		c, ok := s.Context("app", fmt.Sprintf("quay.io/acme/app:%d", i), "podman", "root", "containers-storage")
		require.True(t, ok)
		require.NoError(t, s.RecordApplicationResult(c, testDigest(1), v1beta1.DeviceDeltaApplyStatus{Outcome: v1beta1.DeviceDeltaApplyOutcomeApplied}))
	}
	require.Len(t, s.ApplicationSnapshot("app").Histories, applicationContextCapacity)
	require.Nil(t, lookup(t, s, first, testDigest(1)))
}

func TestStoreRetiresOnlyConfirmedApplicationContexts(t *testing.T) {
	s := NewInMemory(log.NewPrefixLogger("test"))
	c := testContext(t, s, "app")
	require.NoError(t, s.RecordApplicationResult(c, testDigest(1), v1beta1.DeviceDeltaApplyStatus{Outcome: v1beta1.DeviceDeltaApplyOutcomeApplied}))
	require.NoError(t, s.RetireApplicationContexts("app", 0))
	require.NotNil(t, lookup(t, s, c, testDigest(1)))
	require.NoError(t, s.ReconcileApplicationSpecs(nil))
	require.NoError(t, s.RetireApplicationContexts("app", c.Lifecycle))
	next := testContext(t, s, "app")
	require.NoError(t, s.RetireApplicationContexts("app", c.Lifecycle))
	require.Equal(t, next.Lifecycle, s.ApplicationLifecycle("app"))
}

func TestStoreApplicationHistoryBatchesRecencyWrites(t *testing.T) {
	s := NewInMemory(log.NewPrefixLogger("test"))
	c := testContext(t, s, "app")
	for i := 1; i <= 2; i++ {
		require.NoError(t, s.RecordApplicationResult(c, testDigest(i), v1beta1.DeviceDeltaApplyStatus{Outcome: v1beta1.DeviceDeltaApplyOutcomeApplied}))
	}
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	s.readWriter = fileio.NewMockReadWriter(ctrl)
	s.path = "/state/delta-status.json"
	results, err := s.LookupApplicationResults([]ApplicationLookup{{Context: c, Digest: testDigest(1)}, {Context: c, Digest: testDigest(2)}})
	require.NoError(t, err)
	require.NotNil(t, results[0].Status)
	require.NotNil(t, results[1].Status)
}
