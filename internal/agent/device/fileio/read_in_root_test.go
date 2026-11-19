package fileio

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
)

const manifestRel = "updater/manifest.json"

func newRootReader(t *testing.T) (ReadWriter, string) {
	t.Helper()
	tmpDir := t.TempDir()
	return NewReadWriter(NewReader(WithReaderRootDir(tmpDir)), NewWriter(WithWriterRootDir(tmpDir))), tmpDir
}

func TestReadFileInRoot(t *testing.T) {
	content := []byte(`{"apiVersion":"updater-manifest/v1"}`)
	size := int64(len(content))

	testCases := []struct {
		name    string
		setup   func(t *testing.T, imageRoot string)
		root    string
		relPath string
		limit   int64
		want    []byte
		wantErr func(t *testing.T, err error)
	}{
		{
			name:    "When the file is regular and within the limit it should return its bytes",
			setup:   writeManifest(content),
			relPath: manifestRel,
			limit:   size + 10,
			want:    content,
		},
		{
			name:    "When the file size equals the limit it should return its bytes",
			setup:   writeManifest(content),
			relPath: manifestRel,
			limit:   size,
			want:    content,
		},
		{
			name:    "When the file exceeds the limit it should fail",
			setup:   writeManifest(content),
			relPath: manifestRel,
			limit:   size - 1,
		},
		{
			name:    "When the limit is zero it should fail",
			setup:   writeManifest(content),
			relPath: manifestRel,
			limit:   0,
		},
		{
			name:    "When the limit is negative it should fail",
			setup:   writeManifest(content),
			relPath: manifestRel,
			limit:   -1,
		},
		{
			name:    "When the relative path is absolute it should fail",
			setup:   writeManifest(content),
			relPath: "/" + manifestRel,
			limit:   size,
		},
		{
			name:    "When the relative path is the parent directory it should fail",
			setup:   writeManifest(content),
			relPath: "..",
			limit:   size,
		},
		{
			name: "When the relative path escapes the root it should fail",
			setup: func(t *testing.T, imageRoot string) {
				writeManifest(content)(t, imageRoot)
				require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(imageRoot), "outside.json"), content, 0o600))
			},
			relPath: "../outside.json",
			limit:   size,
		},
		{
			name:    "When the relative path traverses through a parent component it should fail",
			setup:   writeManifest(content),
			relPath: "updater/../updater/manifest.json",
			limit:   size,
		},
		{
			name:    "When the relative path has an empty component it should fail",
			setup:   writeManifest(content),
			relPath: "updater//manifest.json",
			limit:   size,
		},
		{
			name:    "When the relative path has a dot component it should fail",
			setup:   writeManifest(content),
			relPath: "./" + manifestRel,
			limit:   size,
		},
		{
			name:    "When the relative path has a trailing slash it should fail",
			setup:   writeManifest(content),
			relPath: manifestRel + "/",
			limit:   size,
		},
		{
			name:    "When the relative path is empty it should fail",
			setup:   writeManifest(content),
			relPath: "",
			limit:   size,
		},
		{
			name:    "When the relative path is the root itself it should fail",
			setup:   writeManifest(content),
			relPath: ".",
			limit:   size,
		},
		{
			name:    "When the relative path contains a NUL byte it should fail",
			setup:   writeManifest(content),
			relPath: "updater/manifest\x00.json",
			limit:   size,
		},
		{
			name: "When the final component is a symlink with a relative target it should fail",
			setup: func(t *testing.T, imageRoot string) {
				require.NoError(t, os.MkdirAll(filepath.Join(imageRoot, "updater"), 0o755))
				require.NoError(t, os.WriteFile(filepath.Join(imageRoot, "updater", "real.json"), content, 0o600))
				require.NoError(t, os.Symlink("real.json", filepath.Join(imageRoot, manifestRel)))
			},
			relPath: manifestRel,
			limit:   size,
		},
		{
			name: "When the final component is a symlink with an absolute target it should fail",
			setup: func(t *testing.T, imageRoot string) {
				require.NoError(t, os.MkdirAll(filepath.Join(imageRoot, "updater"), 0o755))
				target := filepath.Join(imageRoot, "real.json")
				require.NoError(t, os.WriteFile(target, content, 0o600))
				require.NoError(t, os.Symlink(target, filepath.Join(imageRoot, manifestRel)))
			},
			relPath: manifestRel,
			limit:   size,
		},
		{
			name: "When an intermediate directory is a symlink with a relative target it should fail",
			setup: func(t *testing.T, imageRoot string) {
				require.NoError(t, os.MkdirAll(filepath.Join(imageRoot, "real"), 0o755))
				require.NoError(t, os.WriteFile(filepath.Join(imageRoot, "real", "manifest.json"), content, 0o600))
				require.NoError(t, os.Symlink("real", filepath.Join(imageRoot, "updater")))
			},
			relPath: manifestRel,
			limit:   size,
		},
		{
			name: "When an intermediate directory is a symlink with an absolute target it should fail",
			setup: func(t *testing.T, imageRoot string) {
				target := filepath.Join(filepath.Dir(imageRoot), "elsewhere")
				require.NoError(t, os.MkdirAll(target, 0o755))
				require.NoError(t, os.WriteFile(filepath.Join(target, "manifest.json"), content, 0o600))
				require.NoError(t, os.Symlink(target, filepath.Join(imageRoot, "updater")))
			},
			relPath: manifestRel,
			limit:   size,
		},
		{
			name: "When an intermediate component is a regular file it should fail",
			setup: func(t *testing.T, imageRoot string) {
				require.NoError(t, os.WriteFile(filepath.Join(imageRoot, "updater"), content, 0o600))
			},
			relPath: manifestRel,
			limit:   size,
		},
		{
			name: "When the final component is a directory it should fail",
			setup: func(t *testing.T, imageRoot string) {
				require.NoError(t, os.MkdirAll(filepath.Join(imageRoot, manifestRel), 0o755))
			},
			relPath: manifestRel,
			limit:   size,
		},
		{
			name: "When the final component is a FIFO it should fail without blocking",
			setup: func(t *testing.T, imageRoot string) {
				require.NoError(t, os.MkdirAll(filepath.Join(imageRoot, "updater"), 0o755))
				require.NoError(t, syscall.Mkfifo(filepath.Join(imageRoot, manifestRel), 0o600))
			},
			relPath: manifestRel,
			limit:   size,
		},
		{
			name: "When the file is missing it should fail as not existing",
			setup: func(t *testing.T, imageRoot string) {
				require.NoError(t, os.MkdirAll(filepath.Join(imageRoot, "updater"), 0o755))
			},
			relPath: manifestRel,
			limit:   size,
			wantErr: func(t *testing.T, err error) { require.ErrorIs(t, err, fs.ErrNotExist) },
		},
		{
			name: "When the root does not exist it should fail",
			setup: func(t *testing.T, imageRoot string) {
				require.NoError(t, os.RemoveAll(imageRoot))
			},
			relPath: manifestRel,
			limit:   size,
			wantErr: func(t *testing.T, err error) { require.ErrorIs(t, err, fs.ErrNotExist) },
		},
		{
			name: "When the root is a regular file it should fail",
			setup: func(t *testing.T, imageRoot string) {
				require.NoError(t, os.RemoveAll(imageRoot))
				require.NoError(t, os.WriteFile(imageRoot, content, 0o600))
			},
			relPath: manifestRel,
			limit:   size,
		},
		{
			name: "When the root path itself is a symlink it should read beneath its target",
			setup: func(t *testing.T, imageRoot string) {
				target := filepath.Join(filepath.Dir(imageRoot), "storage")
				writeManifest(content)(t, target)
				require.NoError(t, os.Remove(imageRoot))
				require.NoError(t, os.Symlink(target, imageRoot))
			},
			relPath: manifestRel,
			limit:   size,
			want:    content,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			rw, tmpDir := newRootReader(t)
			root := "/mnt/image"
			imageRoot := filepath.Join(tmpDir, root)
			require.NoError(os.MkdirAll(imageRoot, 0o755))
			tc.setup(t, imageRoot)

			data, err := rw.ReadFileInRoot(root, tc.relPath, tc.limit)
			if tc.want != nil {
				require.NoError(err)
				require.Equal(tc.want, data)
				return
			}
			require.Error(err)
			require.Nil(data)
			if tc.wantErr != nil {
				tc.wantErr(t, err)
			}
		})
	}
}

func TestReadFileInRootRefusesDeviceFiles(t *testing.T) {
	require := require.New(t)
	info, err := os.Lstat("/dev/null")
	require.NoError(err, "the test needs the /dev/null character device")
	require.Equal(fs.ModeDevice|fs.ModeCharDevice, info.Mode().Type())

	reader := NewReader()
	data, err := reader.ReadFileInRoot("/dev", "null", 1024)
	require.Error(err)
	require.Nil(data)
	require.True(strings.Contains(err.Error(), "regular"), "unexpected error: %v", err)
}

func writeManifest(content []byte) func(t *testing.T, imageRoot string) {
	return func(t *testing.T, imageRoot string) {
		t.Helper()
		require.NoError(t, os.MkdirAll(filepath.Join(imageRoot, "updater"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(imageRoot, manifestRel), content, 0o600))
	}
}
