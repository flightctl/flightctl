package fileio

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCopyFile(t *testing.T) {
	require := require.New(t)
	tmpDir := t.TempDir()

	currentBytes := []byte("current")
	desiredBytes := []byte("desired")
	rw := NewReadWriter()
	rw.SetRootdir(tmpDir)
	err := rw.WriteFile("current", currentBytes, 0644)
	require.NoError(err)
	err = rw.WriteFile("desired", desiredBytes, 0644)
	require.NoError(err)

	err = rw.CopyFile("current", "desired")
	require.NoError(err)

	current, err := rw.ReadFile("current")
	require.NoError(err)
	require.Equal(currentBytes, current)

	desired, err := rw.ReadFile("desired")
	require.NoError(err)
	require.Equal(currentBytes, desired)
}

func TestMkdirTemp(t *testing.T) {
	require := require.New(t)

	testFileName := "testFile"
	testFileBytes := []byte("test")

	t.Run("create temp dir and write/read file", func(t *testing.T) {
		tmpDir := t.TempDir()
		rw := NewReadWriter()
		rw.SetRootdir(tmpDir)
		dir, err := rw.MkdirTemp("test")
		require.NoError(err)
		require.NotEmpty(dir)

		err = rw.WriteFile(filepath.Join(dir, testFileName), testFileBytes, DefaultFilePermissions)
		require.NoError(err)

		fileBytes, err := rw.ReadFile(filepath.Join(dir, testFileName))
		require.NoError(err)
		require.Equal(testFileBytes, fileBytes)

		err = rw.RemoveAll(dir)
		require.NoError(err)

		exists, err := rw.PathExists(dir)
		require.NoError(err)
		require.False(exists)
	})

	t.Run("no rootdir create temp dir and write/read file", func(t *testing.T) {
		rw := NewReadWriter()

		dir, err := rw.MkdirTemp("test")
		require.NoError(err)
		require.NotEmpty(dir)
		defer func() {
			_ = rw.RemoveAll(dir)
		}()

		uid, gid, err := getUserIdentity()
		require.NoError(err)

		err = rw.WriteFile(filepath.Join(dir, testFileName), testFileBytes, DefaultFilePermissions, WithUid(uid), WithGid(gid))
		require.NoError(err)

		fileBytes, err := rw.ReadFile(filepath.Join(dir, testFileName))
		require.NoError(err)
		require.Equal(testFileBytes, fileBytes)
	})
}

func TestCopyDir(t *testing.T) {
	tests := []struct {
		name          string
		setup         func(t *testing.T, tmpDir string) (srcDir, dstDir string)
		opts          []CopyDirOption
		expectError   bool
		errorContains string
		verify        func(t *testing.T, rw ReadWriter, dstDir string)
	}{
		{
			name: "skip symlinks (default)",
			setup: func(t *testing.T, tmpDir string) (string, string) {
				srcDir := filepath.Join(tmpDir, "src")
				dstDir := filepath.Join(tmpDir, "dst")

				require.NoError(t, os.MkdirAll(filepath.Join(srcDir, "subdir"), 0755))
				require.NoError(t, os.WriteFile(filepath.Join(srcDir, "file.txt"), []byte("content"), 0644))            //nolint:gosec
				require.NoError(t, os.WriteFile(filepath.Join(srcDir, "subdir", "nested.txt"), []byte("nested"), 0644)) //nolint:gosec

				targetFile := filepath.Join(tmpDir, "target.txt")
				require.NoError(t, os.WriteFile(targetFile, []byte("target"), 0644)) // #nosec G306
				require.NoError(t, os.Symlink(targetFile, filepath.Join(srcDir, "link.txt")))
				require.NoError(t, os.Symlink(filepath.Join(tmpDir, "target_dir"), filepath.Join(srcDir, "link_dir")))

				return srcDir, dstDir
			},
			opts: nil,
			verify: func(t *testing.T, rw ReadWriter, dstDir string) {
				exists, err := rw.PathExists(filepath.Join(dstDir, "file.txt"))
				require.NoError(t, err)
				require.True(t, exists)

				exists, err = rw.PathExists(filepath.Join(dstDir, "subdir", "nested.txt"))
				require.NoError(t, err)
				require.True(t, exists)

				exists, err = rw.PathExists(filepath.Join(dstDir, "link.txt"))
				require.NoError(t, err)
				require.False(t, exists)

				exists, err = rw.PathExists(filepath.Join(dstDir, "link_dir"))
				require.NoError(t, err)
				require.False(t, exists)
			},
		},
		{
			name: "error on symlink",
			setup: func(t *testing.T, tmpDir string) (string, string) {
				srcDir := filepath.Join(tmpDir, "src")
				dstDir := filepath.Join(tmpDir, "dst")

				require.NoError(t, os.MkdirAll(srcDir, 0755))
				require.NoError(t, os.WriteFile(filepath.Join(srcDir, "file.txt"), []byte("content"), 0644)) //nolint:gosec

				targetFile := filepath.Join(tmpDir, "target.txt")
				require.NoError(t, os.WriteFile(targetFile, []byte("target"), 0644)) //nolint:gosec
				require.NoError(t, os.Symlink(targetFile, filepath.Join(srcDir, "link.txt")))

				return srcDir, dstDir
			},
			opts:          []CopyDirOption{WithErrorOnSymlink()},
			expectError:   true,
			errorContains: "symlink encountered",
		},
		{
			name: "preserve symlink",
			setup: func(t *testing.T, tmpDir string) (string, string) {
				srcDir := filepath.Join(tmpDir, "src")
				dstDir := filepath.Join(tmpDir, "dst")

				require.NoError(t, os.MkdirAll(srcDir, 0755))
				require.NoError(t, os.WriteFile(filepath.Join(srcDir, "file.txt"), []byte("content"), 0644)) //nolint:gosec

				targetFile := filepath.Join(tmpDir, "target.txt")
				require.NoError(t, os.WriteFile(targetFile, []byte("target"), 0644)) //nolint:gosec
				require.NoError(t, os.Symlink(targetFile, filepath.Join(srcDir, "link.txt")))

				return srcDir, dstDir
			},
			opts: []CopyDirOption{WithPreserveSymlink()},
			verify: func(t *testing.T, rw ReadWriter, dstDir string) {
				dstLink := filepath.Join(dstDir, "link.txt")
				info, err := os.Lstat(dstLink)
				require.NoError(t, err)
				require.NotZero(t, info.Mode()&os.ModeSymlink)

				linkTarget, err := os.Readlink(dstLink)
				require.NoError(t, err)
				require.NotEmpty(t, linkTarget)
			},
		},
		{
			name: "follow symlink to file",
			setup: func(t *testing.T, tmpDir string) (string, string) {
				srcDir := filepath.Join(tmpDir, "src")
				dstDir := filepath.Join(tmpDir, "dst")

				require.NoError(t, os.MkdirAll(srcDir, 0755))

				targetFile := filepath.Join(tmpDir, "target.txt")
				require.NoError(t, os.WriteFile(targetFile, []byte("target content"), 0644)) //nolint:gosec
				require.NoError(t, os.Symlink(targetFile, filepath.Join(srcDir, "link.txt")))

				return srcDir, dstDir
			},
			opts: []CopyDirOption{WithFollowSymlink()},
			verify: func(t *testing.T, rw ReadWriter, dstDir string) {
				dstFile := filepath.Join(dstDir, "link.txt")
				info, err := os.Lstat(dstFile)
				require.NoError(t, err)
				require.Zero(t, info.Mode()&os.ModeSymlink)

				content, err := os.ReadFile(dstFile)
				require.NoError(t, err)
				require.Equal(t, []byte("target content"), content)
			},
		},
		{
			name: "follow symlink to directory",
			setup: func(t *testing.T, tmpDir string) (string, string) {
				srcDir := filepath.Join(tmpDir, "src")
				dstDir := filepath.Join(tmpDir, "dst")
				targetDir := filepath.Join(tmpDir, "target_dir")

				require.NoError(t, os.MkdirAll(srcDir, 0755))
				require.NoError(t, os.MkdirAll(targetDir, 0755))
				require.NoError(t, os.WriteFile(filepath.Join(targetDir, "file.txt"), []byte("content"), 0644)) //nolint:gosec

				require.NoError(t, os.Symlink(targetDir, filepath.Join(srcDir, "link_dir")))

				return srcDir, dstDir
			},
			opts: []CopyDirOption{WithFollowSymlink()},
			verify: func(t *testing.T, rw ReadWriter, dstDir string) {
				dstLinkDir := filepath.Join(dstDir, "link_dir")
				info, err := os.Lstat(dstLinkDir)
				require.NoError(t, err)
				require.True(t, info.IsDir())

				content, err := os.ReadFile(filepath.Join(dstLinkDir, "file.txt"))
				require.NoError(t, err)
				require.Equal(t, []byte("content"), content)
			},
		},
		{
			name: "circular symlink",
			setup: func(t *testing.T, tmpDir string) (string, string) {
				srcDir := filepath.Join(tmpDir, "src")
				dstDir := filepath.Join(tmpDir, "dst")
				dirA := filepath.Join(srcDir, "dirA")
				dirB := filepath.Join(srcDir, "dirB")

				require.NoError(t, os.MkdirAll(dirA, 0755))
				require.NoError(t, os.MkdirAll(dirB, 0755))
				require.NoError(t, os.WriteFile(filepath.Join(dirA, "fileA.txt"), []byte("contentA"), 0644)) //nolint:gosec
				require.NoError(t, os.WriteFile(filepath.Join(dirB, "fileB.txt"), []byte("contentB"), 0644)) //nolint:gosec

				require.NoError(t, os.Symlink(dirB, filepath.Join(dirA, "link_to_b")))
				require.NoError(t, os.Symlink(dirA, filepath.Join(dirB, "link_to_a")))

				return srcDir, dstDir
			},
			opts:          []CopyDirOption{WithFollowSymlink()},
			expectError:   true,
			errorContains: "circular symlink detected",
		},
		{
			name: "source is symlink",
			setup: func(t *testing.T, tmpDir string) (string, string) {
				targetDir := filepath.Join(tmpDir, "target")
				srcLink := filepath.Join(tmpDir, "src_link")
				dstDir := filepath.Join(tmpDir, "dst")

				require.NoError(t, os.MkdirAll(targetDir, 0755))
				require.NoError(t, os.WriteFile(filepath.Join(targetDir, "file.txt"), []byte("content"), 0644)) //nolint:gosec
				require.NoError(t, os.Symlink(targetDir, srcLink))

				return srcLink, dstDir
			},
			opts:          nil,
			expectError:   true,
			errorContains: "source is a symlink",
		},
		{
			name: "nested directories",
			setup: func(t *testing.T, tmpDir string) (string, string) {
				srcDir := filepath.Join(tmpDir, "src")
				dstDir := filepath.Join(tmpDir, "dst")

				require.NoError(t, os.MkdirAll(filepath.Join(srcDir, "level1", "level2", "level3"), 0755))
				require.NoError(t, os.WriteFile(filepath.Join(srcDir, "level1", "file1.txt"), []byte("file1"), 0644))                     //nolint:gosec
				require.NoError(t, os.WriteFile(filepath.Join(srcDir, "level1", "level2", "file2.txt"), []byte("file2"), 0644))           //nolint:gosec
				require.NoError(t, os.WriteFile(filepath.Join(srcDir, "level1", "level2", "level3", "file3.txt"), []byte("file3"), 0644)) //nolint:gosec

				targetFile := filepath.Join(tmpDir, "target.txt")
				require.NoError(t, os.WriteFile(targetFile, []byte("target"), 0644)) // #nosec G306
				require.NoError(t, os.Symlink(targetFile, filepath.Join(srcDir, "level1", "level2", "link.txt")))

				return srcDir, dstDir
			},
			opts: nil,
			verify: func(t *testing.T, rw ReadWriter, dstDir string) {
				content, err := os.ReadFile(filepath.Join(dstDir, "level1", "file1.txt"))
				require.NoError(t, err)
				require.Equal(t, []byte("file1"), content)

				content, err = os.ReadFile(filepath.Join(dstDir, "level1", "level2", "file2.txt"))
				require.NoError(t, err)
				require.Equal(t, []byte("file2"), content)

				content, err = os.ReadFile(filepath.Join(dstDir, "level1", "level2", "level3", "file3.txt"))
				require.NoError(t, err)
				require.Equal(t, []byte("file3"), content)

				exists, err := rw.PathExists(filepath.Join(dstDir, "level1", "level2", "link.txt"))
				require.NoError(t, err)
				require.False(t, exists)
			},
		},
		{
			name: "follow within root - inside root",
			setup: func(t *testing.T, tmpDir string) (string, string) {
				srcDir := filepath.Join(tmpDir, "src")
				dstDir := filepath.Join(tmpDir, "dst")
				targetDir := filepath.Join(srcDir, "target")

				require.NoError(t, os.MkdirAll(targetDir, 0755))
				require.NoError(t, os.WriteFile(filepath.Join(targetDir, "file.txt"), []byte("content"), 0644)) //nolint:gosec

				require.NoError(t, os.MkdirAll(filepath.Join(srcDir, "links"), 0755))
				require.NoError(t, os.Symlink(filepath.Join(srcDir, "target"), filepath.Join(srcDir, "links", "link_to_target")))

				targetFile := filepath.Join(srcDir, "target.txt")
				require.NoError(t, os.WriteFile(targetFile, []byte("file content"), 0644)) //nolint:gosec
				require.NoError(t, os.Symlink(targetFile, filepath.Join(srcDir, "links", "link_to_file.txt")))

				return srcDir, dstDir
			},
			opts: []CopyDirOption{WithFollowSymlinkWithinRoot()},
			verify: func(t *testing.T, rw ReadWriter, dstDir string) {
				content, err := os.ReadFile(filepath.Join(dstDir, "links", "link_to_target", "file.txt"))
				require.NoError(t, err)
				require.Equal(t, []byte("content"), content)

				content, err = os.ReadFile(filepath.Join(dstDir, "links", "link_to_file.txt"))
				require.NoError(t, err)
				require.Equal(t, []byte("file content"), content)
			},
		},
		{
			name: "follow within root - outside root",
			setup: func(t *testing.T, tmpDir string) (string, string) {
				srcDir := filepath.Join(tmpDir, "src")
				dstDir := filepath.Join(tmpDir, "dst")
				outsideFile := filepath.Join(tmpDir, "outside.txt")
				outsideDir := filepath.Join(tmpDir, "outside_dir")

				require.NoError(t, os.MkdirAll(srcDir, 0755))
				require.NoError(t, os.MkdirAll(outsideDir, 0755))
				require.NoError(t, os.WriteFile(outsideFile, []byte("outside"), 0644))                               //nolint:gosec
				require.NoError(t, os.WriteFile(filepath.Join(outsideDir, "file.txt"), []byte("outside dir"), 0644)) //nolint:gosec

				require.NoError(t, os.Symlink(outsideFile, filepath.Join(srcDir, "link_to_outside.txt")))
				require.NoError(t, os.Symlink(outsideDir, filepath.Join(srcDir, "link_to_outside_dir")))

				return srcDir, dstDir
			},
			opts: []CopyDirOption{WithFollowSymlinkWithinRoot()},
			verify: func(t *testing.T, rw ReadWriter, dstDir string) {
				exists, err := rw.PathExists(filepath.Join(dstDir, "link_to_outside.txt"))
				require.NoError(t, err)
				require.False(t, exists)

				exists, err = rw.PathExists(filepath.Join(dstDir, "link_to_outside_dir"))
				require.NoError(t, err)
				require.False(t, exists)
			},
		},
		{
			name: "preserve within root - inside root",
			setup: func(t *testing.T, tmpDir string) (string, string) {
				srcDir := filepath.Join(tmpDir, "src")
				dstDir := filepath.Join(tmpDir, "dst")
				targetDir := filepath.Join(srcDir, "target")

				require.NoError(t, os.MkdirAll(targetDir, 0755))
				require.NoError(t, os.WriteFile(filepath.Join(targetDir, "file.txt"), []byte("content"), 0644)) //nolint:gosec

				require.NoError(t, os.MkdirAll(filepath.Join(srcDir, "links"), 0755))
				require.NoError(t, os.Symlink(filepath.Join(srcDir, "target"), filepath.Join(srcDir, "links", "link_to_target")))

				targetFile := filepath.Join(srcDir, "target.txt")
				require.NoError(t, os.WriteFile(targetFile, []byte("file content"), 0644)) //nolint:gosec
				require.NoError(t, os.Symlink(targetFile, filepath.Join(srcDir, "links", "link_to_file.txt")))

				return srcDir, dstDir
			},
			opts: []CopyDirOption{WithPreserveSymlinkWithinRoot()},
			verify: func(t *testing.T, rw ReadWriter, dstDir string) {
				dstLinkDir := filepath.Join(dstDir, "links", "link_to_target")
				info, err := os.Lstat(dstLinkDir)
				require.NoError(t, err)
				require.NotZero(t, info.Mode()&os.ModeSymlink)

				dstLinkFile := filepath.Join(dstDir, "links", "link_to_file.txt")
				info, err = os.Lstat(dstLinkFile)
				require.NoError(t, err)
				require.NotZero(t, info.Mode()&os.ModeSymlink)
			},
		},
		{
			name: "preserve within root - outside root",
			setup: func(t *testing.T, tmpDir string) (string, string) {
				srcDir := filepath.Join(tmpDir, "src")
				dstDir := filepath.Join(tmpDir, "dst")
				outsideFile := filepath.Join(tmpDir, "outside.txt")
				outsideDir := filepath.Join(tmpDir, "outside_dir")

				require.NoError(t, os.MkdirAll(srcDir, 0755))
				require.NoError(t, os.MkdirAll(outsideDir, 0755))
				require.NoError(t, os.WriteFile(outsideFile, []byte("outside"), 0644))                               //nolint:gosec
				require.NoError(t, os.WriteFile(filepath.Join(outsideDir, "file.txt"), []byte("outside dir"), 0644)) //nolint:gosec

				require.NoError(t, os.Symlink(outsideFile, filepath.Join(srcDir, "link_to_outside.txt")))
				require.NoError(t, os.Symlink(outsideDir, filepath.Join(srcDir, "link_to_outside_dir")))

				return srcDir, dstDir
			},
			opts: []CopyDirOption{WithPreserveSymlinkWithinRoot()},
			verify: func(t *testing.T, rw ReadWriter, dstDir string) {
				exists, err := rw.PathExists(filepath.Join(dstDir, "link_to_outside.txt"))
				require.NoError(t, err)
				require.False(t, exists)

				exists, err = rw.PathExists(filepath.Join(dstDir, "link_to_outside_dir"))
				require.NoError(t, err)
				require.False(t, exists)
			},
		},
		{
			name: "follow within root - circular nested symlink",
			setup: func(t *testing.T, tmpDir string) (string, string) {
				srcDir := filepath.Join(tmpDir, "src")
				dstDir := filepath.Join(tmpDir, "dst")
				dirA := filepath.Join(srcDir, "dirA")
				dirB := filepath.Join(dirA, "dirB")

				require.NoError(t, os.MkdirAll(dirB, 0755))
				require.NoError(t, os.WriteFile(filepath.Join(dirA, "fileA.txt"), []byte("contentA"), 0644)) //nolint:gosec
				require.NoError(t, os.WriteFile(filepath.Join(dirB, "fileB.txt"), []byte("contentB"), 0644)) //nolint:gosec

				require.NoError(t, os.Symlink(dirA, filepath.Join(dirB, "dirC")))

				return srcDir, dstDir
			},
			opts:          []CopyDirOption{WithFollowSymlinkWithinRoot()},
			expectError:   true,
			errorContains: "circular symlink detected",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			srcDir, dstDir := tt.setup(t, tmpDir)

			rw := NewReadWriter()
			err := rw.CopyDir(srcDir, dstDir, tt.opts...)

			if tt.expectError {
				require.Error(t, err)
				if tt.errorContains != "" {
					require.Contains(t, err.Error(), tt.errorContains)
				}
			} else {
				require.NoError(t, err)
				if tt.verify != nil {
					tt.verify(t, rw, dstDir)
				}
			}
		})
	}
}

func TestUnpackTar(t *testing.T) {
	require := require.New(t)

	tests := []struct {
		name          string
		setupTar      func(t *testing.T, tarPath string)
		expectError   bool
		errorContains string
		verify        func(t *testing.T, rw ReadWriter, destDir string)
	}{
		{
			name: "tar with files and directories",
			setupTar: func(t *testing.T, tarPath string) {
				f, err := os.Create(tarPath)
				require.NoError(err)
				defer f.Close()

				tw := tar.NewWriter(f)
				defer tw.Close()

				require.NoError(tw.WriteHeader(&tar.Header{
					Name:     "testdir/",
					Mode:     0755,
					Typeflag: tar.TypeDir,
				}))

				content := []byte("test content")
				require.NoError(tw.WriteHeader(&tar.Header{
					Name:     "testdir/file.txt",
					Mode:     0600,
					Size:     int64(len(content)),
					Typeflag: tar.TypeReg,
				}))
				_, err = tw.Write(content)
				require.NoError(err)
			},
			verify: func(t *testing.T, rw ReadWriter, destDir string) {
				content, err := rw.ReadFile(filepath.Join(destDir, "testdir", "file.txt"))
				require.NoError(err)
				require.Equal([]byte("test content"), content)

				info, err := os.Stat(rw.PathFor(filepath.Join(destDir, "testdir", "file.txt")))
				require.NoError(err)
				require.Equal(os.FileMode(0600), info.Mode().Perm())
			},
		},
		{
			name: "gzipped tar",
			setupTar: func(t *testing.T, tarPath string) {
				f, err := os.Create(tarPath)
				require.NoError(err)
				defer f.Close()

				gzw := gzip.NewWriter(f)
				defer gzw.Close()

				tw := tar.NewWriter(gzw)
				defer tw.Close()

				content := []byte("compressed")
				require.NoError(tw.WriteHeader(&tar.Header{
					Name:     "file.txt",
					Mode:     0644,
					Size:     int64(len(content)),
					Typeflag: tar.TypeReg,
				}))
				_, err = tw.Write(content)
				require.NoError(err)
			},
			verify: func(t *testing.T, rw ReadWriter, destDir string) {
				content, err := rw.ReadFile(filepath.Join(destDir, "file.txt"))
				require.NoError(err)
				require.Equal([]byte("compressed"), content)
			},
		},
		{
			name: "path traversal blocked",
			setupTar: func(t *testing.T, tarPath string) {
				f, err := os.Create(tarPath)
				require.NoError(err)
				defer f.Close()

				tw := tar.NewWriter(f)
				defer tw.Close()

				content := []byte("malicious")
				require.NoError(tw.WriteHeader(&tar.Header{
					Name:     "../../../etc/passwd",
					Mode:     0644,
					Size:     int64(len(content)),
					Typeflag: tar.TypeReg,
				}))
				_, err = tw.Write(content)
				require.NoError(err)
			},
			expectError:   true,
			errorContains: "invalid file path in tar",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			rw := NewReadWriter()
			rw.SetRootdir(tmpDir)

			tarFileName := "test.tar"
			if tt.name == "gzipped tar" {
				tarFileName = "test.tar.gz"
			}
			tarPath := filepath.Join(tmpDir, tarFileName)
			tt.setupTar(t, tarPath)

			destDir := "extracted"
			err := rw.MkdirAll(destDir, DefaultDirectoryPermissions)
			require.NoError(err)

			err = UnpackTar(rw, tarFileName, destDir)

			if tt.expectError {
				require.Error(err)
				if tt.errorContains != "" {
					require.Contains(err.Error(), tt.errorContains)
				}
			} else {
				require.NoError(err)
				if tt.verify != nil {
					tt.verify(t, rw, destDir)
				}
			}
		})
	}
}
