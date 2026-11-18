package firmware_test

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	deviceerrors "github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/agent/device/firmware"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

// onPublishRename runs act before each rename of a staging directory, passing
// the 1-based count of such renames; a non-nil result fails that rename.
func onPublishRename(x *extractHarness, act func(n int) error) {
	n := 0
	record := x.hooks.rename
	x.hooks.rename = func(old, new string) error {
		if strings.HasPrefix(old, ".staging-") {
			n++
			if err := act(n); err != nil {
				return err
			}
		}
		return record(old, new)
	}
}

// failCacheSyncAfter fails every sync of the updater cache once armed
// reports true.
func failCacheSyncAfter(x *extractHarness, armed func() bool) {
	x.hooks.sync = func(dir string) error {
		if armed() && strings.HasSuffix(dir, "/updaters") {
			return fmt.Errorf("injected: %w", unix.EIO)
		}
		return nil
	}
}

func TestExtractRefusesBeforeMounting(t *testing.T) {
	t.Run("When no verified package is given it should refuse to extract", func(t *testing.T) {
		x := newExtractHarness(t, defaultPayload(t))
		_, err := x.extractor().Extract(context.Background(), nil)
		requireFetchError(t, err, firmware.ReasonUpdaterUnavailable, false)
		x.requireCounts(t, 0, 0, 0, 0)
		require.Empty(t, x.entries())
	})
	t.Run("When the verified manifest exceeds the release budget it should refuse before mounting", func(t *testing.T) {
		x := newExtractHarness(t, defaultPayload(t))
		pkg := *x.pkg
		pkg.Manifest.Files = append([]firmware.ManifestFile(nil), x.pkg.Manifest.Files...)
		pkg.Manifest.Files[1].Size = testExtraction().MaxExtractedFileBytes + 1
		_, err := x.extractor().Extract(context.Background(), &pkg)
		requireBudget(t, err, "maxExtractedFileBytes")
		x.requireCounts(t, 0, 0, 0, 0)
		require.Empty(t, x.entries())
	})
	t.Run("When an existing copy cannot be inspected it should refuse before mounting", func(t *testing.T) {
		skipAsRoot(t)
		require := require.New(t)
		x := newExtractHarness(t, defaultPayload(t))
		require.NoError(os.MkdirAll(x.cacheDir(), 0o700))
		require.NoError(os.Chmod(x.cacheDir(), 0o400))
		t.Cleanup(func() { _ = os.Chmod(x.cacheDir(), 0o700) })
		_, err := x.extractor().Extract(context.Background(), x.pkg)
		require.ErrorIs(err, fs.ErrPermission)
		requireFetchError(t, err, firmware.ReasonUpdaterUnavailable, false)
		x.requireCounts(t, 0, 0, 0, 0)
	})
	t.Run("When quarantining an invalid existing copy fails it should refuse before mounting and keep the copy", func(t *testing.T) {
		require := require.New(t)
		x := newExtractHarness(t, defaultPayload(t))
		require.NoError(os.MkdirAll(x.cacheDir(x.finalName(), "junk"), 0o700))
		record := x.hooks.rename
		x.hooks.rename = func(old, new string) error {
			if strings.HasPrefix(new, ".quarantine-") {
				return fmt.Errorf("injected: %w", unix.EIO)
			}
			return record(old, new)
		}
		_, err := x.extractor().Extract(context.Background(), x.pkg)
		requirePublishError(t, err, firmware.PublishNotVisible, "quarantine")
		require.ErrorIs(err, unix.EIO)
		x.requireCounts(t, 0, 0, 0, 0)
		require.Equal([]string{x.finalName()}, x.entries())
		_, statErr := os.Stat(x.cacheDir(x.finalName(), "junk"))
		require.NoError(statErr)
	})
	t.Run("When the context is canceled while an existing copy is verified it should stop without quarantining it", func(t *testing.T) {
		require := require.New(t)
		x := newExtractHarness(t, defaultPayload(t))
		require.NoError(os.MkdirAll(x.cacheDir(x.finalName(), "junk"), 0o700))
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := x.extractor().Extract(ctx, x.pkg)
		require.ErrorIs(err, context.Canceled)
		x.requireCounts(t, 0, 0, 0, 0)
		require.Zero(x.renames)
		require.Equal([]string{x.finalName()}, x.entries())
	})
}

func TestExtractReuseConfirmation(t *testing.T) {
	testCases := []struct {
		name string
		op   string
		fail func(x *extractHarness)
	}{
		{
			name: "When a reused copy cannot be synced it should report it visible but not usable",
			op:   "sync-parent",
			fail: func(x *extractHarness) { failCacheSyncAfter(x, func() bool { return true }) },
		},
		{
			name: "When a reused copy no longer matches its path it should report it visible but not usable",
			op:   "revalidate",
			fail: func(x *extractHarness) {
				x.hooks.revalidate = func(dir string) error {
					if strings.HasSuffix(dir, "/updaters") {
						return deviceerrors.ErrAnchorMoved
					}
					return nil
				}
			},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			x := newExtractHarness(t, defaultPayload(t))
			_, err := x.extractor().Extract(context.Background(), x.pkg)
			require.NoError(err)
			x.resetCounts()
			tc.fail(x)
			_, err = x.extractor().Extract(context.Background(), x.pkg)
			requirePublishError(t, err, firmware.PublishVisibleNotUsable, tc.op)
			requireFetchError(t, err, firmware.ReasonUpdaterUnavailable, true)
			x.requireCounts(t, 0, 0, 0, 0)
			require.Equal([]string{x.finalName()}, x.entries())
		})
	}
}

func TestExtractStagingFailures(t *testing.T) {
	testCases := []struct {
		name    string
		prepare func(t *testing.T, x *extractHarness) *firmware.VerifiedPackage
	}{
		{
			name: "When the mount point cannot be opened it should fail without publishing and still unmount",
			prepare: func(_ *testing.T, x *extractHarness) *firmware.VerifiedPackage {
				x.mounter.point = "/missing-mount"
				return x.pkg
			},
		},
		{
			name: "When the mounted image has no manifest it should fail without publishing",
			prepare: func(t *testing.T, x *extractHarness) *firmware.VerifiedPackage {
				require.NoError(t, os.Remove(x.imageFile("manifest.json")))
				return x.pkg
			},
		},
		{
			name: "When the mounted manifest differs from the verified manifest it should fail without publishing",
			prepare: func(t *testing.T, x *extractHarness) *firmware.VerifiedPackage {
				require.NoError(t, os.WriteFile(x.imageFile("manifest.json"), append(append([]byte(nil), x.manifest...), ' '), 0o600))
				return x.pkg
			},
		},
		{
			name: "When the verified manifest differs from what the mounted manifest describes it should fail without publishing",
			prepare: func(_ *testing.T, x *extractHarness) *firmware.VerifiedPackage {
				pkg := *x.pkg
				pkg.Manifest.Version = "9.9.9"
				return &pkg
			},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			x := newExtractHarness(t, defaultPayload(t))
			pkg := tc.prepare(t, x)
			_, err := x.extractor().Extract(context.Background(), pkg)
			requirePublishError(t, err, firmware.PublishNotVisible, "stage")
			requireFetchError(t, err, firmware.ReasonUpdaterUnavailable, false)
			x.requireCounts(t, 1, 1, 0, 0)
			require.Empty(x.entries())
		})
	}
	t.Run("When the mount fails after the extraction is canceled it should report the cancellation", func(t *testing.T) {
		require := require.New(t)
		x := newExtractHarness(t, defaultPayload(t))
		x.mounter.mountErr = errors.New("mount interrupted")
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := x.extractor().Extract(ctx, x.pkg)
		require.ErrorIs(err, context.Canceled)
		requirePublishError(t, err, firmware.PublishNotVisible, "stage")
		x.requireCounts(t, 1, 0, 0, 0)
		require.Empty(x.entries())
	})
	t.Run("When the caller cancels during staging and the unmount fails too it should report both", func(t *testing.T) {
		require := require.New(t)
		x := newExtractHarness(t, defaultPayload(t))
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		x.hooks.copy = func(string, string) error {
			x.copies++
			cancel()
			return nil
		}
		x.mounter.unmountErr = errors.New("target is busy")
		_, err := x.extractor().Extract(ctx, x.pkg)
		require.ErrorIs(err, context.Canceled)
		require.ErrorIs(err, firmware.ErrUnmountFailed)
		require.Equal(1, x.mounter.unmounts)
		require.Empty(x.entries())
	})
}

func TestExtractPublicationFailures(t *testing.T) {
	t.Run("When the publication rename fails it should discard the staging", func(t *testing.T) {
		require := require.New(t)
		x := newExtractHarness(t, defaultPayload(t))
		onPublishRename(x, func(int) error { return fmt.Errorf("injected: %w", unix.EIO) })
		_, err := x.extractor().Extract(context.Background(), x.pkg)
		requirePublishError(t, err, firmware.PublishNotVisible, "rename")
		requireFetchError(t, err, firmware.ReasonUpdaterUnavailable, true)
		require.ErrorIs(err, unix.EIO)
		require.Contains(x.events.events, "RemoveTree staging")
		require.Empty(x.entries())
	})
	t.Run("When the published copy cannot be synced it should report it visible but not usable and keep it", func(t *testing.T) {
		require := require.New(t)
		x := newExtractHarness(t, defaultPayload(t))
		published := false
		onPublishRename(x, func(int) error { published = true; return nil })
		failCacheSyncAfter(x, func() bool { return published })
		_, err := x.extractor().Extract(context.Background(), x.pkg)
		requirePublishError(t, err, firmware.PublishVisibleNotUsable, "sync-parent")
		require.NotContains(x.events.events, "RemoveTree staging")
		require.Equal([]string{x.finalName()}, x.entries())
	})
	t.Run("When a valid winner cannot be confirmed durable it should discard its own staging and keep the winner", func(t *testing.T) {
		require := require.New(t)
		x := newExtractHarness(t, defaultPayload(t))
		var winnerIno uint64
		planted := false
		onPublishRename(x, func(n int) error {
			if n == 1 {
				other := newExtractHarness(t, x.files)
				_, err := other.extractor().Extract(context.Background(), other.pkg)
				require.NoError(err)
				require.NoError(os.Rename(other.cacheDir(other.finalName()), x.cacheDir(x.finalName())))
				winnerIno = inode(t, x.cacheDir(x.finalName()))
				planted = true
			}
			return nil
		})
		failCacheSyncAfter(x, func() bool { return planted })
		_, err := x.extractor().Extract(context.Background(), x.pkg)
		requirePublishError(t, err, firmware.PublishVisibleNotUsable, "sync-parent")
		require.Contains(x.events.events, "RemoveTree staging")
		require.Equal([]string{x.finalName()}, x.entries())
		require.Equal(winnerIno, inode(t, x.cacheDir(x.finalName())))
	})
	retry := []struct {
		name  string
		state firmware.PublishState
		op    string
		setup func(x *extractHarness, retried func() bool)
		check func(t *testing.T, x *extractHarness)
	}{
		{
			name:  "When the retried publication rename fails it should discard the staging and keep the quarantine",
			state: firmware.PublishNotVisible,
			op:    "rename-retry",
			check: func(t *testing.T, x *extractHarness) {
				entries := x.entries()
				require.Len(t, entries, 1)
				require.True(t, strings.HasPrefix(entries[0], ".quarantine-"))
				require.Contains(t, x.events.events, "RemoveTree staging")
			},
		},
		{
			name:  "When the retried publication cannot be synced it should report it visible but not usable and keep it",
			state: firmware.PublishVisibleNotUsable,
			op:    "sync-parent",
			setup: func(x *extractHarness, retried func() bool) { failCacheSyncAfter(x, retried) },
			check: func(t *testing.T, x *extractHarness) {
				require.Contains(t, x.entries(), x.finalName())
				require.NotContains(t, x.events.events, "RemoveTree staging")
			},
		},
	}
	for _, tc := range retry {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			x := newExtractHarness(t, defaultPayload(t))
			attempts := 0
			onPublishRename(x, func(n int) error {
				attempts = n
				switch {
				case n == 1:
					require.NoError(os.MkdirAll(x.cacheDir(x.finalName(), "junk"), 0o700))
				case n == 2 && tc.setup == nil:
					return fmt.Errorf("injected: %w", unix.EIO)
				}
				return nil
			})
			if tc.setup != nil {
				tc.setup(x, func() bool { return attempts == 2 })
			}
			_, err := x.extractor().Extract(context.Background(), x.pkg)
			requirePublishError(t, err, tc.state, tc.op)
			require.Equal(2, attempts)
			tc.check(t, x)
		})
	}
}

func TestExtractRetainedCapabilityMetadata(t *testing.T) {
	ioErr := fmt.Errorf("injected: %w", unix.EIO)
	publishOnce := func(t *testing.T) *extractHarness {
		t.Helper()
		x := newExtractHarness(t, defaultPayload(t))
		_, err := x.extractor().Extract(context.Background(), x.pkg)
		require.NoError(t, err)
		x.resetCounts()
		return x
	}
	t.Run("When an unpinned copy has unreadable capability metadata it should fail without reusing, quarantining or extracting it", func(t *testing.T) {
		require := require.New(t)
		x := publishOnce(t)
		x.hooks.xattr = capabilityHooks("manifest.json", fileio.XattrUnknown, ioErr).xattr
		_, err := x.extractor().Extract(context.Background(), x.pkg)
		require.ErrorIs(err, firmware.ErrRetainedVerificationIncomplete)
		require.ErrorIs(err, unix.EIO)
		require.NotErrorIs(err, firmware.ErrRetainedPayloadChanged)
		requireFetchError(t, err, firmware.ReasonUpdaterUnavailable, false)
		x.requireCounts(t, 0, 0, 0, 0)
		require.Zero(x.renames)
		require.Equal([]string{x.finalName()}, x.entries())
	})
	t.Run("When an unpinned copy carries file capabilities it should quarantine it and publish a fresh copy", func(t *testing.T) {
		require := require.New(t)
		x := publishOnce(t)
		x.hooks.xattr = capabilityHooks("payload/bin/updater", fileio.XattrPresent, nil).xattr
		payload, err := x.extractor().Extract(context.Background(), x.pkg)
		require.NoError(err)
		require.False(payload.Reused)
		require.Len(payload.Cleanup.Pending, 1)
		require.True(strings.HasPrefix(payload.Cleanup.Pending[0], ".quarantine-"))
		x.requireCounts(t, 1, 1, len(x.files), 1)
	})
	t.Run("When a pinned copy carries file capabilities on its manifest it should refuse it before mounting and keep it", func(t *testing.T) {
		require := require.New(t)
		x := newExtractHarness(t, defaultPayload(t))
		payload, err := x.extractor().Extract(context.Background(), x.pkg)
		require.NoError(err)
		x.pin(t, payload)
		x.resetCounts()
		x.hooks.xattr = capabilityHooks("manifest.json", fileio.XattrPresent, nil).xattr
		_, err = x.extractor().Extract(context.Background(), x.pkg)
		require.ErrorIs(err, firmware.ErrRetainedPayloadChanged)
		x.requireCounts(t, 0, 0, 0, 0)
		require.Zero(x.renames)
		require.Equal([]string{x.finalName()}, x.entries())
	})
	t.Run("When a winner's capability metadata cannot be read it should leave the winner and discard its own staging", func(t *testing.T) {
		require := require.New(t)
		x := newExtractHarness(t, defaultPayload(t))
		onPublishRename(x, func(n int) error {
			if n == 1 {
				other := newExtractHarness(t, x.files)
				_, err := other.extractor().Extract(context.Background(), other.pkg)
				require.NoError(err)
				require.NoError(os.Rename(other.cacheDir(other.finalName()), x.cacheDir(x.finalName())))
				x.hooks.xattr = capabilityHooks("manifest.json", fileio.XattrUnknown, ioErr).xattr
			}
			return nil
		})
		_, err := x.extractor().Extract(context.Background(), x.pkg)
		require.ErrorIs(err, firmware.ErrRetainedVerificationIncomplete)
		requirePublishError(t, err, firmware.PublishNotVisible, "classify")
		require.NotContains(x.events.events, "Rename final->quarantine")
		require.Contains(x.events.events, "RemoveTree staging")
		require.Equal([]string{x.finalName()}, x.entries())
	})
	t.Run("When a late capability read of an unpinned copy fails it should fail without quarantining, mounting or extracting it", func(t *testing.T) {
		require := require.New(t)
		x := publishOnce(t)
		before := cacheTree(t, filepath.Dir(x.cacheDir()))
		late := &lateCapabilityFailure{rel: "payload/bin/updater"}
		x.hooks.hash = late.hook
		_, err := x.extractor().Extract(context.Background(), x.pkg)
		require.ErrorIs(err, firmware.ErrRetainedVerificationIncomplete)
		require.ErrorIs(err, unix.EIO)
		require.NotErrorIs(err, firmware.ErrRetainedPayloadChanged)
		requireFetchError(t, err, firmware.ReasonUpdaterUnavailable, false)
		require.Equal(1, late.hashes, "the failing read was exercised once")
		x.requireCounts(t, 0, 0, 0, 0)
		require.Zero(x.renames)
		require.Equal(before, cacheTree(t, filepath.Dir(x.cacheDir())), "the retained copy, registry and evidence are unchanged")
	})
	t.Run("When a late capability read of a pinned copy fails it should refuse it as unverified before mounting and keep it", func(t *testing.T) {
		require := require.New(t)
		x := newExtractHarness(t, defaultPayload(t))
		payload, err := x.extractor().Extract(context.Background(), x.pkg)
		require.NoError(err)
		x.pin(t, payload)
		x.resetCounts()
		before := cacheTree(t, filepath.Dir(x.cacheDir()))
		late := &lateCapabilityFailure{rel: "payload/bin/updater"}
		x.hooks.hash = late.hook
		_, err = x.extractor().Extract(context.Background(), x.pkg)
		require.ErrorIs(err, firmware.ErrRetainedVerificationIncomplete)
		require.ErrorIs(err, unix.EIO)
		require.NotErrorIs(err, firmware.ErrRetainedPayloadChanged)
		requireFetchError(t, err, firmware.ReasonUpdaterUnavailable, false)
		require.Equal(1, late.hashes, "the failing read was exercised once")
		x.requireCounts(t, 0, 0, 0, 0)
		require.Zero(x.renames)
		require.Equal(before, cacheTree(t, filepath.Dir(x.cacheDir())), "the pinned copy, registry and evidence are unchanged")
	})
	t.Run("When a late capability read of a contended winner fails it should leave the winner and add no mount, copy, publication or quarantine", func(t *testing.T) {
		require := require.New(t)
		x := newExtractHarness(t, defaultPayload(t))
		late := &lateCapabilityFailure{rel: "payload/bin/updater"}
		var winner map[string]string
		mounts, copies, seen := -1, -1, -1
		onPublishRename(x, func(n int) error {
			if n == 1 {
				other := newExtractHarness(t, x.files)
				_, err := other.extractor().Extract(context.Background(), other.pkg)
				require.NoError(err)
				require.NoError(os.Rename(other.cacheDir(other.finalName()), x.cacheDir(x.finalName())))
				winner = cacheTree(t, x.cacheDir(x.finalName()))
				mounts, copies, seen = x.mounter.mounts, x.copies, len(x.events.events)
				x.hooks.hash = late.hook
			}
			return nil
		})
		_, err := x.extractor().Extract(context.Background(), x.pkg)
		require.ErrorIs(err, firmware.ErrRetainedVerificationIncomplete)
		require.ErrorIs(err, unix.EIO)
		require.NotErrorIs(err, firmware.ErrRetainedPayloadChanged)
		requirePublishError(t, err, firmware.PublishNotVisible, "classify")
		require.Equal(1, late.hashes, "the failing read was exercised once")
		require.Equal(mounts, x.mounter.mounts, "no additional mount")
		require.Equal(copies, x.copies, "no additional copy")
		after := x.events.events[seen:]
		publications := 0
		for _, e := range after {
			if e == "Rename staging->final" {
				publications++
			}
		}
		require.Equal(1, publications, "only the contended publication attempt")
		require.NotContains(after, "Rename final->quarantine")
		require.Contains(after, "RemoveTree staging")
		require.Equal(winner, cacheTree(t, x.cacheDir(x.finalName())), "the winner is unchanged")
		require.Equal([]string{x.finalName()}, x.entries())
	})
	t.Run("When the capabilities of an image file cannot be read it should fail the copy without publishing", func(t *testing.T) {
		require := require.New(t)
		x := newExtractHarness(t, defaultPayload(t))
		x.hooks.xattr = capabilityHooks("updater/payload/bin/updater", fileio.XattrUnknown, ioErr).xattr
		_, err := x.extractor().Extract(context.Background(), x.pkg)
		require.ErrorIs(err, unix.EIO)
		require.ErrorIs(err, deviceerrors.ErrFileCapabilityUnknown)
		require.NotErrorIs(err, deviceerrors.ErrFileCapability)
		requirePublishError(t, err, firmware.PublishNotVisible, "copy")
		requireFetchError(t, err, firmware.ReasonUpdaterUnavailable, false)
		require.NotContains(x.events.events, "Rename staging->final")
		require.Empty(x.entries())
	})
}

// cacheTree describes every entry under dir by mode, size, inode and, for
// regular files, content digest.
func cacheTree(t *testing.T, dir string) map[string]string {
	t.Helper()
	tree := map[string]string{}
	require.NoError(t, filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		desc := fmt.Sprintf("%s %d ino=%d", info.Mode(), info.Size(), info.Sys().(*syscall.Stat_t).Ino)
		if info.Mode().IsRegular() {
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			desc += " " + sha256Hex(data)
		}
		tree[strings.TrimPrefix(p, dir)] = desc
		return nil
	}))
	return tree
}
