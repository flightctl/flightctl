package registryconfig_test

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	deviceerrors "github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/agent/device/registryconfig"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/stretchr/testify/require"
)

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
	uid, gid := uint32(os.Geteuid()), uint32(os.Getegid()) //nolint:gosec // effective IDs are nonnegative
	mgr := registryconfig.NewManager(fileio.NewReadWriterFactory(root), dataDir, registryconfig.WithAccountLookup(func(name v1beta1.Username) (registryconfig.Account, error) {
		homes := map[v1beta1.Username]string{"": processHome, "workload": namedHome, "protected": dataDir, "relative": "relative/home", "missinghome": "/nonexistent", "linked": "/home-alias/workload"}
		home, ok := homes[name]
		if !ok {
			return registryconfig.Account{}, fs.ErrNotExist
		}
		return registryconfig.Account{UID: uid, GID: gid, HomeDir: home}, nil
	}))
	return mgr, root
}

func TestPrepareRejectsInvalidResourcesAndTargets(t *testing.T) {
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
			err := mgr.Prepare(t.Context(), desired)
			require.ErrorIs(t, err, deviceerrors.ErrInvalidConfigResource)
			require.NotContains(t, err.Error(), secret)
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
