package config

import (
	"context"
	"os"
	"os/user"
	"path/filepath"
	"testing"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

// inlineProvider builds a single inline config provider owning the given files,
// mirroring how the service renders every config source into one inline provider
// before the agent reconciles it. Ownership defaults to the current process so
// the real writer does not attempt a privileged chown during tests.
func inlineProvider(t *testing.T, paths ...string) *[]v1beta1.ConfigProviderSpec {
	t.Helper()
	cur, err := user.Current()
	require.NoError(t, err)

	files := make([]v1beta1.FileSpec, 0, len(paths))
	for _, path := range paths {
		files = append(files, v1beta1.FileSpec{
			Path:    path,
			Content: "content of " + path,
			Mode:    lo.ToPtr(0o644),
			User:    v1beta1.Username(cur.Uid),
			Group:   cur.Gid,
		})
	}

	var provider v1beta1.ConfigProviderSpec
	require.NoError(t, provider.FromInlineConfigProviderSpec(v1beta1.InlineConfigProviderSpec{Inline: files}))
	return &[]v1beta1.ConfigProviderSpec{provider}
}

// TestSyncRemovesEmptyDirs verifies end-to-end with a real writer that removing
// a config source deletes both its files and the now-empty directory tree that
// held them, while a protected system root survives.
func TestSyncRemovesEmptyDirs(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	tempDir := t.TempDir()
	rw := fileio.NewReadWriter(
		fileio.NewReader(fileio.WithReaderRootDir(tempDir)),
		fileio.NewWriter(fileio.WithWriterRootDir(tempDir)),
	)
	controller := NewController(rw, "/var/lib/flightctl", log.NewPrefixLogger("test"))

	files := []string{
		"/etc/microshift/manifests.d/app1/kustomization.yaml",
		"/etc/microshift/manifests.d/app1/deploy.yaml",
		"/etc/microshift/manifests.d/app2/svc.yaml",
	}

	// Add the source: files are written to disk.
	require.NoError(controller.Sync(ctx,
		&v1beta1.DeviceSpec{},
		&v1beta1.DeviceSpec{Config: inlineProvider(t, files...)},
	))
	for _, f := range files {
		_, err := os.Stat(filepath.Join(tempDir, f))
		require.NoError(err, "file should exist after applying the source: %s", f)
	}

	// Remove the source: files and the now-empty directory tree must be removed.
	require.NoError(controller.Sync(ctx,
		&v1beta1.DeviceSpec{Config: inlineProvider(t, files...)},
		&v1beta1.DeviceSpec{},
	))

	for _, f := range files {
		_, err := os.Stat(filepath.Join(tempDir, f))
		require.True(os.IsNotExist(err), "file should be removed after source removal: %s", f)
	}
	for _, dir := range []string{
		"/etc/microshift/manifests.d/app1",
		"/etc/microshift/manifests.d/app2",
		"/etc/microshift/manifests.d",
		"/etc/microshift",
	} {
		_, err := os.Stat(filepath.Join(tempDir, dir))
		require.True(os.IsNotExist(err), "empty directory should be cleaned up: %s", dir)
	}
	// /etc is a protected system root and must never be removed.
	_, err := os.Stat(filepath.Join(tempDir, "/etc"))
	require.NoError(err, "/etc must not be removed")
}

// TestSyncPreservesSharedDirs verifies that removing some files under a
// directory that still holds desired files leaves that directory (and its
// remaining files) intact.
func TestSyncPreservesSharedDirs(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	tempDir := t.TempDir()
	rw := fileio.NewReadWriter(
		fileio.NewReader(fileio.WithReaderRootDir(tempDir)),
		fileio.NewWriter(fileio.WithWriterRootDir(tempDir)),
	)
	controller := NewController(rw, "/var/lib/flightctl", log.NewPrefixLogger("test"))

	kept := "/etc/microshift/manifests.d/app/keep.yaml"
	removed := "/etc/microshift/manifests.d/app/remove.yaml"

	require.NoError(controller.Sync(ctx,
		&v1beta1.DeviceSpec{},
		&v1beta1.DeviceSpec{Config: inlineProvider(t, kept, removed)},
	))

	// Drop one file; the other remains desired.
	require.NoError(controller.Sync(ctx,
		&v1beta1.DeviceSpec{Config: inlineProvider(t, kept, removed)},
		&v1beta1.DeviceSpec{Config: inlineProvider(t, kept)},
	))

	_, err := os.Stat(filepath.Join(tempDir, removed))
	require.True(os.IsNotExist(err), "removed file should be gone")
	_, err = os.Stat(filepath.Join(tempDir, kept))
	require.NoError(err, "desired file must remain")
	_, err = os.Stat(filepath.Join(tempDir, "/etc/microshift/manifests.d/app"))
	require.NoError(err, "shared directory holding a desired file must remain")
}

// TestSyncPreservesNonEmptyDirs verifies that a directory holding unmanaged
// content (files not part of any config source) is never removed.
func TestSyncPreservesNonEmptyDirs(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	tempDir := t.TempDir()
	rw := fileio.NewReadWriter(
		fileio.NewReader(fileio.WithReaderRootDir(tempDir)),
		fileio.NewWriter(fileio.WithWriterRootDir(tempDir)),
	)
	controller := NewController(rw, "/var/lib/flightctl", log.NewPrefixLogger("test"))

	managed := "/etc/microshift/manifests.d/app/managed.yaml"
	require.NoError(controller.Sync(ctx,
		&v1beta1.DeviceSpec{},
		&v1beta1.DeviceSpec{Config: inlineProvider(t, managed)},
	))

	// Drop an unmanaged file into the same directory before removal.
	unmanaged := filepath.Join(tempDir, "/etc/microshift/manifests.d/app/unmanaged.txt")
	require.NoError(os.WriteFile(unmanaged, []byte("keep me"), 0o600))

	require.NoError(controller.Sync(ctx,
		&v1beta1.DeviceSpec{Config: inlineProvider(t, managed)},
		&v1beta1.DeviceSpec{},
	))

	_, err := os.Stat(filepath.Join(tempDir, managed))
	require.True(os.IsNotExist(err), "managed file should be removed")
	_, err = os.Stat(unmanaged)
	require.NoError(err, "unmanaged file must be preserved")
	_, err = os.Stat(filepath.Join(tempDir, "/etc/microshift/manifests.d/app"))
	require.NoError(err, "directory holding unmanaged content must not be removed")
}

// TestSyncPreservesPreExistingDirs verifies end-to-end that cleanup removes only
// directories the agent created: a pre-existing (e.g. package-created) directory
// whose only file was a managed one is preserved once that file is removed,
// while a sibling directory the agent created is pruned.
func TestSyncPreservesPreExistingDirs(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	tempDir := t.TempDir()
	rw := fileio.NewReadWriter(
		fileio.NewReader(fileio.WithReaderRootDir(tempDir)),
		fileio.NewWriter(fileio.WithWriterRootDir(tempDir)),
	)
	controller := NewController(rw, "/var/lib/flightctl", log.NewPrefixLogger("test"))

	// Simulate a package-created directory the agent must never remove.
	preExisting := "/opt/vendor/data"
	require.NoError(os.MkdirAll(filepath.Join(tempDir, preExisting), 0o755))

	inPreExisting := "/opt/vendor/data/app.conf" // only file, but dir predates the agent
	agentOwned := "/opt/managed/app/config.yaml" // agent creates /opt/managed[/app]

	require.NoError(controller.Sync(ctx,
		&v1beta1.DeviceSpec{},
		&v1beta1.DeviceSpec{Config: inlineProvider(t, inPreExisting, agentOwned)},
	))

	// Remove the source.
	require.NoError(controller.Sync(ctx,
		&v1beta1.DeviceSpec{Config: inlineProvider(t, inPreExisting, agentOwned)},
		&v1beta1.DeviceSpec{},
	))

	// Both files are gone.
	for _, f := range []string{inPreExisting, agentOwned} {
		_, err := os.Stat(filepath.Join(tempDir, f))
		require.True(os.IsNotExist(err), "file should be removed: %s", f)
	}
	// The pre-existing directory (and its parent) must survive.
	_, err := os.Stat(filepath.Join(tempDir, preExisting))
	require.NoError(err, "pre-existing directory the agent did not create must be preserved")
	// The agent-created tree must be pruned.
	for _, dir := range []string{"/opt/managed/app", "/opt/managed"} {
		_, err := os.Stat(filepath.Join(tempDir, dir))
		require.True(os.IsNotExist(err), "agent-created empty directory should be removed: %s", dir)
	}
}

// TestSyncOwnershipPersistsAcrossControllers verifies that directory ownership
// recorded when files are written survives into a fresh controller (simulating
// an agent restart between the write and the later removal), so cleanup still
// prunes the agent-created tree.
func TestSyncOwnershipPersistsAcrossControllers(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	tempDir := t.TempDir()
	rw := fileio.NewReadWriter(
		fileio.NewReader(fileio.WithReaderRootDir(tempDir)),
		fileio.NewWriter(fileio.WithWriterRootDir(tempDir)),
	)

	file := "/etc/foo/bar/x.yaml"

	// First controller writes the files and persists ownership.
	writer := NewController(rw, "/var/lib/flightctl", log.NewPrefixLogger("test"))
	require.NoError(writer.Sync(ctx,
		&v1beta1.DeviceSpec{},
		&v1beta1.DeviceSpec{Config: inlineProvider(t, file)},
	))

	// A fresh controller (as after a reboot) removes the source.
	remover := NewController(rw, "/var/lib/flightctl", log.NewPrefixLogger("test"))
	require.NoError(remover.Sync(ctx,
		&v1beta1.DeviceSpec{Config: inlineProvider(t, file)},
		&v1beta1.DeviceSpec{},
	))

	for _, dir := range []string{"/etc/foo/bar", "/etc/foo"} {
		_, err := os.Stat(filepath.Join(tempDir, dir))
		require.True(os.IsNotExist(err), "agent-created empty directory should be removed after restart: %s", dir)
	}
	_, err := os.Stat(filepath.Join(tempDir, "/etc"))
	require.NoError(err, "/etc must not be removed")
}

func TestRemoveEmptyDirs(t *testing.T) {
	tests := []struct {
		name         string
		removedFiles []string
		desiredFiles []v1beta1.FileSpec
		// owned is the set of agent-created directories loaded from the ownership
		// manifest; only directories in this set are eligible for cleanup.
		owned []string
		// expectedDirs is the exact set of directories RemoveEmptyDir is called
		// with, in deepest-first order.
		expectedDirs []string
	}{
		{
			name: "nested tree cleaned deepest first, stops at /etc",
			removedFiles: []string{
				"/etc/microshift/manifests.d/app1/deploy.yaml",
				"/etc/microshift/manifests.d/app2/svc.yaml",
			},
			owned: []string{
				"/etc/microshift",
				"/etc/microshift/manifests.d",
				"/etc/microshift/manifests.d/app1",
				"/etc/microshift/manifests.d/app2",
			},
			expectedDirs: []string{
				"/etc/microshift/manifests.d/app1",
				"/etc/microshift/manifests.d/app2",
				"/etc/microshift/manifests.d",
				"/etc/microshift",
			},
		},
		{
			name: "directories holding desired files are preserved",
			removedFiles: []string{
				"/etc/microshift/manifests.d/app/remove.yaml",
			},
			desiredFiles: []v1beta1.FileSpec{
				{Path: "/etc/microshift/manifests.d/app/keep.yaml"},
			},
			owned: []string{
				"/etc/microshift",
				"/etc/microshift/manifests.d",
				"/etc/microshift/manifests.d/app",
			},
			expectedDirs: nil,
		},
		{
			name: "protected root is never removed",
			removedFiles: []string{
				"/etc/motd",
			},
			owned:        []string{"/etc"},
			expectedDirs: nil,
		},
		{
			name: "unlisted top-level dir is never removed, nested dirs are",
			removedFiles: []string{
				"/media/config/app.yaml",
			},
			owned: []string{"/media", "/media/config"},
			// /media/config is cleaned, but /media (a direct child of "/") is
			// preserved even though it is not on any allowlist.
			expectedDirs: []string{
				"/media/config",
			},
		},
		{
			name: "directories the agent did not create are never removed",
			removedFiles: []string{
				"/home/flightctl/.local/app.conf",
			},
			// The agent never created ~flightctl/.local (the package did), so it
			// is absent from the owned set and must be preserved even once empty.
			owned:        nil,
			expectedDirs: nil,
		},
		{
			name:         "empty removal list is a no-op",
			removedFiles: nil,
			expectedDirs: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockWriter := fileio.NewMockReadWriter(ctrl)
			var got []string
			mockWriter.EXPECT().RemoveEmptyDir(gomock.Any()).DoAndReturn(func(dir string) error {
				got = append(got, dir)
				return nil
			}).AnyTimes()

			owned := make(map[string]bool, len(tt.owned))
			for _, dir := range tt.owned {
				owned[dir] = true
			}

			controller := NewController(mockWriter, "/var/lib/flightctl", log.NewPrefixLogger("test"))
			controller.removeEmptyDirs(tt.removedFiles, tt.desiredFiles, owned)

			require.Equal(t, tt.expectedDirs, got)
		})
	}
}

func TestIsCleanupCandidate(t *testing.T) {
	tests := []struct {
		dir  string
		want bool
	}{
		{"", false},
		{".", false},
		{"/", false},
		{"/etc", false},
		{"/var", false},
		{"/etc/microshift", true},
		{"/etc/microshift/manifests.d", true},
		{"/opt/app/data", true},
		// top-level directories (direct children of "/") are never candidates,
		// even ones not on any allowlist such as /media or /lib64.
		{"/media", false},
		{"/lib64", false},
		{"/media/app", true},
		// relative paths must never be cleanup candidates: joined with the
		// writer root they could traverse outside it (e.g. "../etc").
		{"../etc", false},
		{"etc/microshift", false},
		{"./opt/app/data", false},
	}
	for _, tt := range tests {
		t.Run(tt.dir, func(t *testing.T) {
			require.Equal(t, tt.want, isCleanupCandidate(tt.dir))
		})
	}
}
