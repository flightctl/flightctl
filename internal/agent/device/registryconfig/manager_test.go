package registryconfig_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/device/config"
	deviceerrors "github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/agent/device/registryconfig"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/pkg/userutil"
	"github.com/stretchr/testify/require"
)

// Registry credentials in these unit fixtures are synthetic test data.
const (
	processHome = "/var/local/process"
	namedHome   = "/srv/custom/workload"
	dataDir     = "/var/lib/flightctl"
	secret      = "test-credential-sentinel"
	valid       = `{"name":"registry:quay.io:5000","type":"registry","registry":"quay.io:5000","scheme":"https","skipServerVerification":false,"access":[{"scope":"","authentication":{"authType":"docker","username":"test","password":"` + secret + `"},"consumers":[{"type":"container"},{"type":"container","user":""}]},{"scope":"team/nested","authentication":{"authType":"docker","username":"test","password":"named"},"consumers":[{"type":"container","user":"workload"}]},{"scope":"anonymous","consumers":[{"type":"container","user":"workload"}]}]}`
)

func live(home string) string { return filepath.Join(home, consts.ContainersAuthRelativePath) }

func spec(t *testing.T, texts ...string) *v1beta1.DeviceSpec {
	t.Helper()
	resources := make([]v1beta1.ConfigResource, 0, len(texts))
	for _, text := range texts {
		var resource v1beta1.ConfigResource
		require.NoError(t, json.Unmarshal([]byte(text), &resource))
		resources = append(resources, resource)
	}
	return &v1beta1.DeviceSpec{ConfigResources: &resources}
}

func fixture(t *testing.T) (registryconfig.Manager, string) {
	t.Helper()
	t.Setenv("TMPDIR", "/tmp")
	root := t.TempDir()
	for _, dir := range []string{processHome, namedHome, dataDir, "/tmp"} {
		require.NoError(t, os.MkdirAll(filepath.Join(root, dir), 0o700))
	}
	mgr := registryconfig.NewManager(fileio.NewReadWriterFactory(root), dataDir, registryconfig.WithAccountLookup(fixtureAccount))
	t.Cleanup(func() { require.NoError(t, mgr.Cleanup()) })
	return mgr, root
}

func fixtureAccount(name v1beta1.Username) (userutil.Account, error) {
	uid, gid := uint32(os.Geteuid()), uint32(os.Getegid()) //nolint:gosec // effective IDs are nonnegative
	homes := map[v1beta1.Username]string{"": processHome, "workload": namedHome, "protected": dataDir, "relative": "relative/home", "missinghome": "/nonexistent", "linked": "/home-alias/workload"}
	home, ok := homes[name]
	if !ok {
		return userutil.Account{}, fs.ErrNotExist
	}
	return userutil.Account{UID: uid, GID: gid, HomeDir: home}, nil
}

func TestRegistryAuthRejectsInvalidResourcesAndTargets(t *testing.T) {
	for _, tc := range []struct {
		name, from, to string
		setup          func(*testing.T, string)
	}{
		{"unknown resource", `"type":"registry"`, `"type":"other"`, nil},
		{"invalid scope", `"scope":""`, `"scope":"../invalid"`, nil},
		{"missing transport field", `"skipServerVerification":false,`, "", nil},
		{"invalid Docker auth", `"username":"test"`, `"username":""`, nil},
		{"unhandled OS consumer", `"type":"container"`, `"type":"os"`, nil},
		{"unhandled Helm chart consumer", `"type":"container"`, `"type":"helm-chart"`, nil},
		{"unhandled Helm image consumer", `"type":"container"`, `"type":"helm-image"`, nil},
		{"missing account", `"user":"workload"`, `"user":"unknown"`, nil},
		{"protected home", `"user":"workload"`, `"user":"protected"`, nil},
		{"relative home", `"user":"workload"`, `"user":"relative"`, nil},
		{"missing home", `"user":"workload"`, `"user":"missinghome"`, nil},
		{"linked home ancestor", `"user":"workload"`, `"user":"linked"`, func(t *testing.T, root string) {
			require.NoError(t, os.Symlink(filepath.Join(root, "/srv/custom"), filepath.Join(root, "home-alias")))
		}},
		{"linked config directory", "", "", func(t *testing.T, root string) {
			require.NoError(t, os.Symlink(filepath.Join(root, dataDir), filepath.Join(root, namedHome, ".config")))
		}},
		{"linked containers directory", "", "", func(t *testing.T, root string) {
			path := filepath.Dir(filepath.Join(root, live(namedHome)))
			require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
			require.NoError(t, os.Symlink(filepath.Join(root, dataDir), path))
		}},
		{"linked auth file", "", "", func(t *testing.T, root string) {
			path := filepath.Join(root, live(namedHome))
			require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
			require.NoError(t, os.Symlink(filepath.Join(root, live(processHome)), path))
		}},
		{"conflicting scope", `{"scope":"anonymous"`, `{"scope":"team/nested","authentication":{"authType":"docker","username":"test","password":"conflict"}`, nil},
	} {
		t.Run("When input contains "+tc.name+" it should reject before staging or changing files", func(t *testing.T) {
			mgr, root := fixture(t)
			current := filepath.Join(root, live(processHome))
			require.NoError(t, os.MkdirAll(filepath.Dir(current), 0o700))
			require.NoError(t, os.WriteFile(current, []byte("current bytes"), 0o600))
			if tc.setup != nil {
				tc.setup(t, root)
			}
			desired := spec(t, strings.ReplaceAll(valid, tc.from, tc.to))
			for _, err := range []error{mgr.BeforeUpdate(t.Context(), desired), mgr.Sync(t.Context(), desired)} {
				require.ErrorIs(t, err, deviceerrors.ErrInvalidConfigResource)
				require.NotContains(t, err.Error(), secret)
			}
			contents, err := os.ReadFile(current)
			require.NoError(t, err)
			require.Equal(t, "current bytes", string(contents))
			staged, err := os.ReadDir(filepath.Join(root, "tmp"))
			require.NoError(t, err)
			require.Empty(t, staged)
			require.NoFileExists(t, filepath.Join(root, "/home/unknown", consts.ContainersAuthRelativePath))
		})
	}
}

func assertAuth(t *testing.T, path string, entries map[string]string) {
	t.Helper()
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, fs.FileMode(0o600), info.Mode().Perm())
	stat := info.Sys().(*syscall.Stat_t)
	require.Equal(t, os.Geteuid(), int(stat.Uid))
	require.Equal(t, os.Getegid(), int(stat.Gid))
	want := map[string]map[string]string{}
	for key, credential := range entries {
		want[key] = map[string]string{"auth": base64.StdEncoding.EncodeToString([]byte(credential))}
	}
	expected, err := json.Marshal(map[string]any{"auths": want})
	require.NoError(t, err)
	actual, err := os.ReadFile(path)
	require.NoError(t, err)
	require.JSONEq(t, string(expected), string(actual))
}

func TestRegistryAuthLifecycle(t *testing.T) {
	mgr, root := fixture(t)
	desired := spec(t, valid)
	processFile, namedFile := live(processHome), live(namedHome)
	processAuth := map[string]string{"quay.io:5000": "test:" + secret}
	namedAuth := map[string]string{"quay.io:5000/team/nested": "test:named"}

	require.NoError(t, mgr.BeforeUpdate(t.Context(), desired))
	processStage, ok := mgr.StagedPath(processFile)
	require.True(t, ok)
	namedStage, ok := mgr.StagedPath(namedFile)
	require.True(t, ok)
	require.NotEqual(t, processStage, namedStage)
	require.NotContains(t, processStage, secret)
	assertAuth(t, processStage, processAuth)
	assertAuth(t, namedStage, namedAuth)
	for _, path := range []string{processStage, namedStage} {
		info, err := os.Stat(filepath.Dir(path))
		require.NoError(t, err)
		require.Equal(t, fs.FileMode(0o711), info.Mode().Perm())
		require.Equal(t, os.Geteuid(), int(info.Sys().(*syscall.Stat_t).Uid))
	}
	require.NoFileExists(t, filepath.Join(root, processFile))
	require.NoFileExists(t, filepath.Join(root, namedFile))
	require.NoError(t, mgr.BeforeUpdate(t.Context(), desired))
	again, ok := mgr.StagedPath(processFile)
	require.True(t, ok)
	require.Equal(t, processStage, again)

	require.NoError(t, mgr.Sync(t.Context(), desired))
	require.FileExists(t, processStage)
	require.FileExists(t, namedStage)
	assertAuth(t, filepath.Join(root, processFile), processAuth)
	assertAuth(t, filepath.Join(root, namedFile), namedAuth)
	require.NoError(t, mgr.AfterUpdate(t.Context(), desired))
	require.NoFileExists(t, processStage)
	require.NoFileExists(t, namedStage)
	_, ok = mgr.StagedPath(processFile)
	require.False(t, ok)
	info, err := os.Stat(filepath.Dir(filepath.Join(root, namedFile)))
	require.NoError(t, err)
	require.Equal(t, fs.FileMode(0o700), info.Mode().Perm())
	record, err := os.ReadFile(filepath.Join(root, dataDir, "registry-config/outputs.json"))
	require.NoError(t, err)
	require.Contains(t, string(record), namedFile)
	require.NotContains(t, string(record), secret)

	fewer := strings.ReplaceAll(valid, `,{"type":"container","user":""}`, "")
	before, err := os.Stat(filepath.Join(root, processFile))
	require.NoError(t, err)
	require.NoError(t, mgr.Sync(t.Context(), spec(t, fewer)))
	after, err := os.Stat(filepath.Join(root, processFile))
	require.NoError(t, err)
	require.True(t, os.SameFile(before, after))

	removed := strings.ReplaceAll(fewer, `"authentication":{"authType":"docker","username":"test","password":"`+secret+`"},`, "")
	require.NoError(t, mgr.Sync(t.Context(), spec(t, removed)))
	require.NoFileExists(t, filepath.Join(root, processFile))
	assertAuth(t, filepath.Join(root, namedFile), namedAuth)
	restarted := registryconfig.NewManager(fileio.NewReadWriterFactory(root), dataDir, registryconfig.WithAccountLookup(func(name v1beta1.Username) (userutil.Account, error) {
		t.Errorf("withdrawal looked up account %q", name)
		return userutil.Account{}, fs.ErrNotExist
	}))
	require.NoError(t, restarted.Sync(t.Context(), &v1beta1.DeviceSpec{}))
	require.NoFileExists(t, filepath.Join(root, namedFile))
}

func TestRegistryAuthPreservesExactAuthoredDestination(t *testing.T) {
	mgr, root := fixture(t)
	desired := spec(t, valid)
	require.NoError(t, mgr.Sync(t.Context(), desired))
	path := live(namedHome)
	var err error
	desired.Config, err = config.FilesToProviderSpec([]v1beta1.FileSpec{{Path: path}})
	require.NoError(t, err)
	require.NoError(t, mgr.BeforeUpdate(t.Context(), desired))
	_, ok := mgr.StagedPath(path)
	require.False(t, ok)
	require.NoError(t, mgr.Sync(t.Context(), desired))
	require.NoError(t, mgr.Sync(t.Context(), spec(t)))
	assertAuth(t, filepath.Join(root, path), map[string]string{"quay.io:5000/team/nested": "test:named"})
}

func TestRegistryAuthRequiresOutputRecord(t *testing.T) {
	mgr, root := fixture(t)
	path := filepath.Join(root, dataDir, "registry-config")
	require.NoError(t, os.WriteFile(path, []byte("not a directory"), 0o600))
	require.Error(t, mgr.Sync(t.Context(), spec(t, valid)))
	require.NoFileExists(t, filepath.Join(root, live(processHome)))
	require.NoFileExists(t, filepath.Join(root, live(namedHome)))
}

func TestRegistryAuthWithdrawalPreservesDirectory(t *testing.T) {
	mgr, root := fixture(t)
	require.NoError(t, mgr.Sync(t.Context(), spec(t, valid)))
	path := filepath.Join(root, live(namedHome))
	require.NoError(t, os.Remove(path))
	require.NoError(t, os.Mkdir(path, 0o700))
	keep := filepath.Join(path, "other-data")
	require.NoError(t, os.WriteFile(keep, []byte("unrelated"), 0o600))
	require.Error(t, mgr.Sync(t.Context(), spec(t)))
	require.FileExists(t, keep)
}

func TestRegistryAuthKeepsOtherEntriesForTheSameUser(t *testing.T) {
	mgr, root := fixture(t)
	// Use a different synthetic credential to distinguish the two fixture registries.
	const otherPort = `{"name":"registry:quay.io:5001","type":"registry","registry":"quay.io:5001","scheme":"https","skipServerVerification":false,"access":[{"scope":"team/nested","authentication":{"authType":"docker","username":"test","password":"alternate"},"consumers":[{"type":"container","user":"workload"}]}]}`
	path := live(namedHome)
	desired := spec(t, valid, otherPort)
	require.NoError(t, mgr.BeforeUpdate(t.Context(), desired))
	stage, ok := mgr.StagedPath(path)
	require.True(t, ok)
	assertAuth(t, stage, map[string]string{
		"quay.io:5000/team/nested": "test:named",
		"quay.io:5001/team/nested": "test:alternate",
	})
	require.NoError(t, mgr.Sync(t.Context(), desired))
	require.NoError(t, mgr.Sync(t.Context(), spec(t, otherPort)))
	assertAuth(t, filepath.Join(root, path), map[string]string{"quay.io:5001/team/nested": "test:alternate"})
	require.NoFileExists(t, filepath.Join(root, live(processHome)))
}

func TestRegistryAuthRejectsUnusableStaging(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, dir string)
	}{
		{name: "When the staging parent is missing it should fail before exposing auth", setup: func(t *testing.T, dir string) {
			require.NoError(t, os.Remove(dir))
		}},
		{name: "When the staging parent is writable without the sticky bit it should refuse it", setup: func(t *testing.T, dir string) {
			require.NoError(t, os.Chmod(dir, 0o777))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mgr, root := fixture(t)
			tc.setup(t, filepath.Join(root, "tmp"))
			err := mgr.BeforeUpdate(t.Context(), spec(t, valid))
			require.Error(t, err)
			require.NotContains(t, err.Error(), secret)
			_, ok := mgr.StagedPath(live(processHome))
			require.False(t, ok)
			require.NoFileExists(t, filepath.Join(root, live(processHome)))
			require.Empty(t, stagedDirectories(t, root))
		})
	}
}

func TestRegistryAuthCancelledBeforeUpdateLeavesExistingState(t *testing.T) {
	mgr, root := fixture(t)
	desired := spec(t, valid)
	require.NoError(t, mgr.Sync(t.Context(), desired))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	require.ErrorIs(t, mgr.BeforeUpdate(ctx, spec(t, strings.ReplaceAll(valid, secret, "rotated"))), context.Canceled)
	require.Empty(t, stagedDirectories(t, root))
	assertAuth(t, filepath.Join(root, live(processHome)), map[string]string{"quay.io:5000": "test:" + secret})
}

func stagedDirectories(t *testing.T, root string) []fs.DirEntry {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(root, "tmp"))
	if os.IsNotExist(err) {
		return nil
	}
	require.NoError(t, err)
	return entries
}

func TestRegistryAuthRejectsInvalidOutputRecord(t *testing.T) {
	for _, tc := range []struct{ name, record string }{
		{"When the record version is unknown it should preserve existing auth", `{"version":99,"outputs":[]}`},
		{"When a record points outside a home it should preserve existing auth", `{"version":2,"outputs":[{"path":"/.config/containers/auth.json","uid":0,"gid":0,"installed":"synthetic-marker"}]}`},
		{"When a record lacks publication markers it should preserve existing auth", `{"version":2,"outputs":[{"path":"/var/local/process/.config/containers/auth.json","uid":0,"gid":0}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mgr, root := fixture(t)
			require.NoError(t, mgr.Sync(t.Context(), spec(t, valid)))
			require.NoError(t, os.WriteFile(filepath.Join(root, dataDir, "registry-config/outputs.json"), []byte(tc.record), 0o600))
			require.Error(t, mgr.Sync(t.Context(), spec(t, strings.ReplaceAll(valid, secret, "rotated"))))
			assertAuth(t, filepath.Join(root, live(processHome)), map[string]string{"quay.io:5000": "test:" + secret})
		})
	}
}

func TestRegistryAuthRetainsStagingUntilCleanup(t *testing.T) {
	mgr, _ := fixture(t)
	desired := spec(t, valid)
	path := live(processHome)
	require.NoError(t, mgr.BeforeUpdate(t.Context(), desired))
	old, ok := mgr.StagedPath(path)
	require.True(t, ok)
	updated := strings.ReplaceAll(valid, secret, "rotated")
	updated = strings.ReplaceAll(updated, `"scheme":"https","skipServerVerification":false`, `"scheme":"http","skipServerVerification":true`)
	require.NoError(t, mgr.BeforeUpdate(t.Context(), spec(t, updated)))
	newPath, ok := mgr.StagedPath(path)
	require.True(t, ok)
	require.NotEqual(t, old, newPath)
	assertAuth(t, old, map[string]string{"quay.io:5000": "test:" + secret})
	assertAuth(t, newPath, map[string]string{"quay.io:5000": "test:rotated"})
	require.NoError(t, mgr.Cleanup())
	require.NoFileExists(t, old)
	require.NoFileExists(t, newPath)
	_, ok = mgr.StagedPath(path)
	require.False(t, ok)
}
