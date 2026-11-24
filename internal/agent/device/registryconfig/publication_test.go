package registryconfig_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/agent/device/registryconfig"
	"github.com/stretchr/testify/require"
)

func TestRegistryAuthInterruptedPublication(t *testing.T) {
	for _, tc := range []struct {
		name          string
		managed       bool
		replaced      bool
		retry         bool
		wantPreserved bool
	}{
		{name: "When publication fails before replacement it should preserve unmanaged auth", wantPreserved: true},
		{name: "When publication stops after replacement it should withdraw the installed file", replaced: true},
		{name: "When publication stops after replacement it should recover without rewriting", replaced: true, retry: true},
		{name: "When a rotation fails before replacement it should still withdraw the old managed file", managed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			restarted, root := fixture(t)
			path := filepath.Join(root, live(namedHome))
			const unmanaged = "unmanaged auth"
			if tc.managed {
				require.NoError(t, restarted.Sync(ctx, spec(t, valid)))
			} else {
				require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
				require.NoError(t, os.WriteFile(path, []byte(unmanaged), 0o600))
			}
			desired := spec(t, strings.ReplaceAll(valid, `"password":"named"`, `"password":"rotated"`))
			factory := fileio.NewReadWriterFactory(root)
			interrupted := errors.New("interrupted publication")
			mgr := registryconfig.NewManager(func(user v1beta1.Username) (fileio.ReadWriter, error) {
				rw, err := factory(user)
				return publicationReadWriter{ReadWriter: rw, replace: func(dir, name string, write func() error) error {
					if filepath.Join(dir, name) != live(namedHome) {
						return write()
					}
					if tc.replaced {
						if err := write(); err != nil {
							return err
						}
					}
					return interrupted
				}}, err
			}, dataDir, registryconfig.WithAccountLookup(fixtureAccount))
			require.ErrorIs(t, mgr.Sync(ctx, desired), interrupted)
			if tc.retry {
				before, err := os.Stat(path)
				require.NoError(t, err)
				require.NoError(t, restarted.Sync(ctx, desired))
				after, err := os.Stat(path)
				require.NoError(t, err)
				require.True(t, os.SameFile(before, after))
			}

			require.NoError(t, restarted.Sync(ctx, &v1beta1.DeviceSpec{}))
			if tc.wantPreserved {
				auth, err := os.ReadFile(path)
				require.NoError(t, err)
				require.Equal(t, unmanaged, string(auth))
			} else {
				require.NoFileExists(t, path)
			}
		})
	}
}

func TestRegistryAuthWithdrawalRetriesDirectorySync(t *testing.T) {
	ctx := t.Context()
	mgr, root := fixture(t)
	require.NoError(t, mgr.Sync(ctx, spec(t, valid)))
	path := live(namedHome)
	// Simulate an unlink that completed before withdrawal's directory sync failed.
	require.NoError(t, os.Remove(filepath.Join(root, path)))
	recordPath := filepath.Join(root, dataDir, "registry-config/outputs.json")
	before, err := os.ReadFile(recordPath)
	require.NoError(t, err)
	require.Contains(t, string(before), path)

	factory := fileio.NewReadWriterFactory(root)
	syncErr := errors.New("directory sync failed")
	retrying := registryconfig.NewManager(func(user v1beta1.Username) (fileio.ReadWriter, error) {
		rw, err := factory(user)
		return publicationReadWriter{ReadWriter: rw, syncDirectory: func(dir string) error {
			if dir == filepath.Dir(path) {
				return syncErr
			}
			return nil
		}}, err
	}, dataDir, registryconfig.WithAccountLookup(fixtureAccount))
	withdrawn := &v1beta1.DeviceSpec{}
	require.ErrorIs(t, retrying.Sync(ctx, withdrawn), syncErr)
	after, err := os.ReadFile(recordPath)
	require.NoError(t, err)
	require.Equal(t, before, after)

	require.NoError(t, mgr.Sync(ctx, withdrawn))
	after, err = os.ReadFile(recordPath)
	require.NoError(t, err)
	require.NotContains(t, string(after), path)
}

func TestRegistryAuthWithdrawalPreservesExternalReplacement(t *testing.T) {
	mgr, root := fixture(t)
	require.NoError(t, mgr.Sync(t.Context(), spec(t, valid)))
	path := filepath.Join(root, live(namedHome))
	auth, err := os.ReadFile(path)
	require.NoError(t, err)
	// Even identical contents and permissions do not establish agent ownership.
	other := filepath.Join(filepath.Dir(path), "other-auth.json")
	require.NoError(t, os.WriteFile(other, auth, 0o600))
	require.NoError(t, os.Rename(other, path))
	require.NoError(t, mgr.Sync(t.Context(), &v1beta1.DeviceSpec{}))
	require.FileExists(t, path)
}

func TestRegistryAuthReconcilesRollbackTarget(t *testing.T) {
	for _, tc := range []struct {
		name    string
		applied bool
	}{
		{name: "When rolling back a deferred update it should repair drift without removing newer staging"},
		{name: "When rolling back an applied update it should restore the previous auth", applied: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mgr, root := fixture(t)
			current := spec(t, valid)
			next := spec(t, strings.ReplaceAll(valid, secret, "rotated"))
			ctx := t.Context()
			livePath := live(processHome)
			path := filepath.Join(root, livePath)
			require.NoError(t, mgr.Sync(ctx, current))
			require.NoError(t, mgr.BeforeUpdate(ctx, next))
			stage, ok := mgr.StagedPath(livePath)
			require.True(t, ok)
			if tc.applied {
				require.NoError(t, mgr.Sync(ctx, next))
				assertAuth(t, path, map[string]string{"quay.io:5000": "test:rotated"})
			} else {
				require.NoError(t, os.Remove(path))
			}

			require.NoError(t, mgr.Sync(ctx, current))
			require.NoError(t, mgr.AfterUpdate(ctx, current))
			assertAuth(t, path, map[string]string{"quay.io:5000": "test:" + secret})
			stillStaged, ok := mgr.StagedPath(livePath)
			require.True(t, ok)
			require.Equal(t, stage, stillStaged)
			require.FileExists(t, stage)
		})
	}
}

func TestRegistryAuthRepairsDriftOnSameSpec(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(t *testing.T, path string)
	}{
		{name: "When generated auth is edited it should restore approved contents", edit: func(t *testing.T, path string) {
			require.NoError(t, os.WriteFile(path, []byte(`{"auths":{}}`), 0o600))
		}},
		{name: "When generated auth is replaced it should restore approved contents", edit: func(t *testing.T, path string) {
			require.NoError(t, os.Remove(path))
			require.NoError(t, os.WriteFile(path, []byte(`{"auths":{}}`), 0o600))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mgr, root := fixture(t)
			ctx := t.Context()
			desired := spec(t, valid)
			path := filepath.Join(root, live(processHome))
			require.NoError(t, mgr.Sync(ctx, desired))
			tc.edit(t, path)

			require.NoError(t, mgr.BeforeUpdate(ctx, desired))
			require.NoError(t, mgr.Sync(ctx, desired))
			require.NoError(t, mgr.AfterUpdate(ctx, desired))
			assertAuth(t, path, map[string]string{"quay.io:5000": "test:" + secret})
		})
	}
}

type publicationReadWriter struct {
	fileio.ReadWriter
	replace       func(dir, name string, write func() error) error
	syncDirectory func(dir string) error
}

func (rw publicationReadWriter) OpenAnchor(path string, opts fileio.DirOptions) (fileio.Anchor, error) {
	a, err := rw.ReadWriter.OpenAnchor(path, opts)
	return publicationAnchor{Anchor: a, rw: rw}, err
}

type publicationAnchor struct {
	fileio.Anchor
	rw publicationReadWriter
}

func (a publicationAnchor) OpenDir(name string, opts fileio.DirOptions) (fileio.Anchor, error) {
	dir, err := a.Anchor.OpenDir(name, opts)
	return publicationAnchor{Anchor: dir, rw: a.rw}, err
}

func (a publicationAnchor) ReplaceFileDurable(name string, data []byte, perm os.FileMode, opts ...fileio.ReplaceOption) error {
	if a.rw.replace == nil {
		return a.Anchor.ReplaceFileDurable(name, data, perm, opts...)
	}
	return a.rw.replace(a.Path(), name, func() error { return a.Anchor.ReplaceFileDurable(name, data, perm, opts...) })
}

func (a publicationAnchor) Sync() error {
	if a.rw.syncDirectory != nil {
		if err := a.rw.syncDirectory(a.Path()); err != nil {
			return err
		}
	}
	return a.Anchor.Sync()
}
