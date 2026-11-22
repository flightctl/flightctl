package firmware_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	deviceerrors "github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/agent/device/firmware"
	policy "github.com/flightctl/flightctl/internal/firmwarepolicy"
	catalog "github.com/flightctl/flightctl/internal/service/updatercatalog"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func (a hooked) Close() error {
	if a.h.close != nil {
		a.h.close(a.Path())
	}
	return a.Anchor.Close()
}

type eventLog struct{ events []string }

func (l *eventLog) add(e string) { l.events = append(l.events, e) }

type fakeMounter struct {
	point           string
	log             *eventLog
	mounts          int
	unmounts        int
	mountErr        error
	unmountErr      error
	unmountBlock    bool
	unmountDeadline time.Time
	unmountCtxErr   error
}

func (m *fakeMounter) Mount(ctx context.Context, image string) (string, error) {
	m.mounts++
	m.log.add("Mount")
	if m.mountErr != nil {
		return "", m.mountErr
	}
	return m.point, nil
}

func (m *fakeMounter) Unmount(ctx context.Context, image string) error {
	m.unmounts++
	m.log.add("Unmount")
	m.unmountDeadline, _ = ctx.Deadline()
	m.unmountCtxErr = ctx.Err()
	if m.unmountBlock {
		<-ctx.Done()
		return fmt.Errorf("podman image unmount: %w", ctx.Err())
	}
	return m.unmountErr
}

type fakeApproval struct{ approved bool }

func (a *fakeApproval) IsApproved(string, string, string) bool { return a.approved }

type extractHarness struct {
	*fetchHarness
	files     []payloadFile
	pkg       *firmware.VerifiedPackage
	mounter   *fakeMounter
	approval  *fakeApproval
	events    *eventLog
	hooks     *hooks
	retention *firmware.Retention
	copies    int
	renames   int
}

func newExtractHarness(t *testing.T, files []payloadFile) *extractHarness {
	t.Helper()
	fh := newFetchHarness(t)
	release := withExtractionBudget(fh.release, testExtraction())
	fh.approve(release)
	doc := manifestFor(release)
	list := make([]any, 0, len(files))
	for _, f := range files {
		list = append(list, map[string]any{"path": f.path, "sha256": sha256Hex(f.data), "size": len(f.data), "executable": f.exec})
	}
	doc["files"] = list
	doc["entrypoint"] = files[0].path
	fh.setManifest(doc)
	for _, f := range files {
		p := filepath.Join(fh.root, testMountPoint, "updater", f.path)
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		mode := os.FileMode(0o644)
		if f.exec {
			mode = 0o755
		}
		require.NoError(t, os.WriteFile(p, f.data, mode))
	}
	pkg, err := fh.fetcher().Fetch(context.Background(), release.Name, release.Version)
	require.NoError(t, err)
	x := &extractHarness{fetchHarness: fh, files: files, pkg: pkg, events: &eventLog{}, approval: &fakeApproval{approved: true}, hooks: &hooks{}}
	x.mounter = &fakeMounter{point: testMountPoint, log: x.events}
	x.retention = firmware.NewRetention(testDataDir, x.rw(), fh.logger)
	x.record()
	return x
}

func (x *extractHarness) rw() fileio.ReadWriter {
	return hookedRW{ReadWriter: x.fetchHarness.rw, h: x.hooks}
}

func (x *extractHarness) kind(name string) string {
	switch {
	case strings.HasPrefix(name, ".staging-"):
		return "staging"
	case strings.HasPrefix(name, ".quarantine-"):
		return "quarantine"
	case name == x.finalName():
		return "final"
	default:
		return name
	}
}

// record installs hooks that log cache events and count copies and renames.
func (x *extractHarness) record() {
	x.hooks.copy = func(string, string) error { x.copies++; x.events.add("CopyFrom"); return nil }
	x.hooks.rename = func(old, new string) error {
		x.renames++
		x.events.add("Rename " + x.kind(old) + "->" + x.kind(new))
		return nil
	}
	x.hooks.sync = func(dir string) error {
		if strings.HasSuffix(dir, "/updaters") {
			x.events.add("Sync")
		}
		return nil
	}
	x.hooks.revalidate = func(dir string) error {
		if strings.HasSuffix(dir, "/updaters") {
			x.events.add("Revalidate")
		}
		return nil
	}
	x.hooks.removeTree = func(name string) (fileio.RemoveResult, error, bool) {
		x.events.add("RemoveTree " + x.kind(name))
		return fileio.RemoveResult{}, nil, false
	}
	x.hooks.replace = func(dir, _ string) error {
		if strings.HasSuffix(dir, "/evidence") {
			x.events.add("Evidence")
		}
		return nil
	}
	x.hooks.close = func(dir string) {
		if dir == testMountPoint {
			x.events.add("CloseSource")
		}
	}
}

func (x *extractHarness) extractor(opts ...firmware.ExtractorOption) *firmware.Extractor {
	return firmware.NewExtractor(testDataDir, x.mounter, x.approval, x.rw(), x.retention, x.logger, append([]firmware.ExtractorOption{firmware.WithGOARCH(testArch)}, opts...)...)
}

func (x *extractHarness) finalName() string {
	return strings.TrimPrefix(x.pkg.PackageDigest, "sha256:")
}

func (x *extractHarness) cacheDir(rel ...string) string {
	return filepath.Join(append([]string{x.root, testDataDir, "firmware", "updaters"}, rel...)...)
}

func (x *extractHarness) entries() []string {
	entries, err := os.ReadDir(x.cacheDir())
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

func (x *extractHarness) imageFile(rel string) string {
	return filepath.Join(x.root, testMountPoint, "updater", rel)
}

func (x *extractHarness) pin(t *testing.T, payload *firmware.PublishedPayload) firmware.Pin {
	t.Helper()
	pin := payload.Pin("attempt-1", firmware.PinRoleActive)
	require.NoError(t, x.retention.Pin(pin))
	return pin
}

func (x *extractHarness) requireCounts(t *testing.T, mounts, unmounts, copies, publications int) {
	t.Helper()
	require.Equal(t, mounts, x.mounter.mounts, "mounts")
	require.Equal(t, unmounts, x.mounter.unmounts, "unmounts")
	require.Equal(t, copies, x.copies, "copies")
	publishes := 0
	for _, e := range x.events.events {
		if e == "Rename staging->final" {
			publishes++
		}
	}
	require.Equal(t, publications, publishes, "publications")
}

func (x *extractHarness) resetCounts() {
	x.mounter.mounts, x.mounter.unmounts, x.copies, x.renames = 0, 0, 0, 0
	x.events.events = nil
}

func TestExtractPublishesAFreshPayload(t *testing.T) {
	require := require.New(t)
	x := newExtractHarness(t, defaultPayload(t))
	payload, err := x.extractor().Extract(context.Background(), x.pkg)
	require.NoError(err)
	require.False(payload.Reused)
	require.Equal(x.pkg.Reference, payload.Reference)
	require.Equal(x.pkg.PackageDigest, payload.PackageDigest)
	require.Equal(x.pkg.ManifestDigest, payload.ManifestDigest)
	require.Equal(x.pkg.DescriptorDigest, payload.DescriptorDigest)
	require.Equal(x.finalName(), payload.Dir)
	require.Equal(asPolicyLimits(testExtraction()), payload.Limits)
	require.Equal("publish", payload.Evidence.Event)
	require.False(payload.Evidence.RecordedAfterPublication)
	require.Empty(payload.Cleanup.Pending)
	x.requireCounts(t, 1, 1, len(x.files), 1)
	require.Equal([]string{"Mount", "CopyFrom", "CopyFrom", "CopyFrom", "CloseSource", "Unmount", "Rename staging->final", "Sync", "Revalidate", "Evidence"}, x.events.events)
	require.Equal([]string{x.finalName()}, x.entries())
	ev, err := x.retention.Evidence(x.pkg.PackageDigest)
	require.NoError(err)
	require.Equal(ev.Published, ev.Latest)
	require.Equal("publish", ev.Published.Event)

	x.resetCounts()
	again, err := x.extractor().Extract(context.Background(), x.pkg)
	require.NoError(err)
	require.True(again.Reused)
	require.Equal("reuse", again.Evidence.Event)
	require.Equal(payload.Files, again.Files)
	x.requireCounts(t, 0, 0, 0, 0)
	ev, err = x.retention.Evidence(x.pkg.PackageDigest)
	require.NoError(err)
	require.EqualValues(1, ev.ObservationsSincePublished)
}

func TestExtractRetainedPayloadMatrix(t *testing.T) {
	t.Run("When the registry is malformed it should fail closed before mounting", func(t *testing.T) {
		require := require.New(t)
		x := newExtractHarness(t, defaultPayload(t))
		require.NoError(os.MkdirAll(filepath.Join(x.root, testDataDir, "firmware"), 0o700))
		require.NoError(os.WriteFile(filepath.Join(x.root, testDataDir, "firmware", "retained.json"), []byte("{"), 0o600))
		_, err := x.extractor().Extract(context.Background(), x.pkg)
		requireFetchError(t, err, firmware.ReasonUpdaterUnavailable, false)
		x.requireCounts(t, 0, 0, 0, 0)
	})
	t.Run("When the pin identity conflicts with the package it should report the conflict before mounting", func(t *testing.T) {
		require := require.New(t)
		x := newExtractHarness(t, defaultPayload(t))
		payload, err := x.extractor().Extract(context.Background(), x.pkg)
		require.NoError(err)
		conflicting := payload.Pin("attempt-1", firmware.PinRoleActive)
		conflicting.ManifestDigest = "sha256:" + strings.Repeat("0", 64)
		require.NoError(x.retention.Pin(conflicting))
		x.resetCounts()
		_, err = x.extractor().Extract(context.Background(), x.pkg)
		require.ErrorIs(err, firmware.ErrPinConflict)
		x.requireCounts(t, 0, 0, 0, 0)
	})
	t.Run("When a pinned payload is missing it should refuse it before any mount, also after restart and cleanup", func(t *testing.T) {
		require := require.New(t)
		x := newExtractHarness(t, defaultPayload(t))
		payload, err := x.extractor().Extract(context.Background(), x.pkg)
		require.NoError(err)
		x.pin(t, payload)
		require.NoError(os.RemoveAll(x.cacheDir(x.finalName())))
		for i := 0; i < 3; i++ {
			x.resetCounts()
			if i == 1 {
				x.retention = firmware.NewRetention(testDataDir, x.rw(), x.logger)
			}
			if i == 2 {
				_, err := x.retention.CleanupUnreferenced(context.Background())
				require.NoError(err)
			}
			_, err = x.extractor().Extract(context.Background(), x.pkg)
			require.ErrorIs(err, firmware.ErrRetainedPayloadMissing)
			requireFetchError(t, err, firmware.ReasonUpdaterUnavailable, false)
			x.resetCountsKeepEvents(t, 0, 0, 0, 0)
			require.NotContains(x.entries(), x.finalName(), "missing pinned code is never re-extracted")
		}
	})
	t.Run("When a pinned payload verifies it should reuse it without mounting and update its evidence", func(t *testing.T) {
		require := require.New(t)
		x := newExtractHarness(t, defaultPayload(t))
		payload, err := x.extractor().Extract(context.Background(), x.pkg)
		require.NoError(err)
		x.pin(t, payload)
		x.resetCounts()
		reused, err := x.extractor().Extract(context.Background(), x.pkg)
		require.NoError(err)
		require.True(reused.Reused)
		require.Equal("reuse", reused.Evidence.Event)
		x.requireCounts(t, 0, 0, 0, 0)
		require.Equal([]string{"Sync", "Revalidate", "Evidence"}, x.events.events)
	})
	t.Run("When a pinned payload changed it should refuse it without mounting or touching its evidence", func(t *testing.T) {
		require := require.New(t)
		x := newExtractHarness(t, defaultPayload(t))
		payload, err := x.extractor().Extract(context.Background(), x.pkg)
		require.NoError(err)
		x.pin(t, payload)
		evidenceFile := filepath.Join(x.root, testDataDir, "firmware", "evidence", x.finalName()+".json")
		before, err := os.ReadFile(evidenceFile)
		require.NoError(err)
		doc := x.cacheDir(x.finalName(), "payload", "share", "doc.txt")
		require.NoError(os.Chmod(doc, 0o600))
		require.NoError(os.WriteFile(doc, []byte("DOCUMENTATION"), 0o600))
		require.NoError(os.Chmod(doc, 0o400))
		x.resetCounts()
		_, err = x.extractor().Extract(context.Background(), x.pkg)
		require.ErrorIs(err, firmware.ErrRetainedPayloadChanged)
		x.requireCounts(t, 0, 0, 0, 0)
		after, err := os.ReadFile(evidenceFile)
		require.NoError(err)
		require.Equal(before, after)
	})
	t.Run("When verifying a pinned payload exceeds its deadline it should report the verification incomplete", func(t *testing.T) {
		require := require.New(t)
		x := newExtractHarness(t, defaultPayload(t))
		payload, err := x.extractor().Extract(context.Background(), x.pkg)
		require.NoError(err)
		x.pin(t, payload)
		x.resetCounts()
		ctx := &expiringContext{Context: context.Background(), done: make(chan struct{})}
		x.hooks.xattr = func(_, attr string) ([]byte, fileio.XattrState, error, bool) {
			if attr == "security.capability" {
				close(ctx.done)
			}
			return nil, fileio.XattrAbsent, nil, true
		}
		_, err = x.extractor().Extract(ctx, x.pkg)
		require.ErrorIs(err, firmware.ErrRetainedVerificationIncomplete)
		requireFetchError(t, err, firmware.ReasonUpdaterUnavailable, true)
		x.requireCounts(t, 0, 0, 0, 0)
	})
	t.Run("When the cache anchor is untrusted it should refuse before mounting", func(t *testing.T) {
		require := require.New(t)
		x := newExtractHarness(t, defaultPayload(t))
		sentinel := filepath.Join(x.root, "sentinel")
		require.NoError(os.MkdirAll(sentinel, 0o700))
		require.NoError(os.Symlink(sentinel, x.cacheDir()))
		_, err := x.extractor().Extract(context.Background(), x.pkg)
		require.ErrorIs(err, deviceerrors.ErrUntrustedDirectory)
		requireFetchError(t, err, firmware.ReasonUpdaterUnavailable, false)
		x.requireCounts(t, 0, 0, 0, 0)
		entries, err := os.ReadDir(sentinel)
		require.NoError(err)
		require.Empty(entries)
	})
	t.Run("When an unpinned final is corrupt it should quarantine it and publish a fresh copy", func(t *testing.T) {
		require := require.New(t)
		x := newExtractHarness(t, defaultPayload(t))
		require.NoError(os.MkdirAll(x.cacheDir(x.finalName(), "junk"), 0o700))
		payload, err := x.extractor().Extract(context.Background(), x.pkg)
		require.NoError(err)
		require.False(payload.Reused)
		require.Len(payload.Cleanup.Pending, 1)
		require.True(strings.HasPrefix(payload.Cleanup.Pending[0], ".quarantine-"))
		x.requireCounts(t, 1, 1, len(x.files), 1)
		require.Equal("Rename final->quarantine", x.events.events[0])
		_, err = os.Stat(x.cacheDir(payload.Cleanup.Pending[0], "junk"))
		require.NoError(err)
	})
	t.Run("When an earlier publication left a final without evidence it should reuse it and record evidence", func(t *testing.T) {
		require := require.New(t)
		x := newExtractHarness(t, defaultPayload(t))
		_, err := x.extractor().Extract(context.Background(), x.pkg)
		require.NoError(err)
		require.NoError(os.RemoveAll(filepath.Join(x.root, testDataDir, "firmware", "evidence")))
		x.resetCounts()
		reused, err := x.extractor().Extract(context.Background(), x.pkg)
		require.NoError(err)
		require.True(reused.Reused)
		require.True(reused.Evidence.RecordedAfterPublication)
		x.requireCounts(t, 0, 0, 0, 0)
	})
}

func (x *extractHarness) resetCountsKeepEvents(t *testing.T, mounts, unmounts, copies, publications int) {
	t.Helper()
	x.requireCounts(t, mounts, unmounts, copies, publications)
}

func TestExtractChecksBeforeMounting(t *testing.T) {
	t.Run("When the release is no longer approved it should refuse before mounting", func(t *testing.T) {
		x := newExtractHarness(t, defaultPayload(t))
		x.approval.approved = false
		_, err := x.extractor().Extract(context.Background(), x.pkg)
		requireFetchError(t, err, firmware.ReasonUpdaterUnavailable, false)
		x.requireCounts(t, 0, 0, 0, 0)
	})
	t.Run("When the release budget exceeds a ceiling it should refuse before mounting", func(t *testing.T) {
		x := newExtractHarness(t, defaultPayload(t))
		ceilings := firmware.DefaultExtractionCeilings()
		ceilings.MaxExtractedFiles = 2
		_, err := x.extractor(firmware.WithExtractionCeilings(ceilings)).Extract(context.Background(), x.pkg)
		requireFetchError(t, err, firmware.ReasonFirmwareNotSupported, false)
		x.requireCounts(t, 0, 0, 0, 0)
	})
	t.Run("When the reference is not pinned to the package digest it should refuse before mounting", func(t *testing.T) {
		x := newExtractHarness(t, defaultPayload(t))
		pkg := *x.pkg
		pkg.Reference = "quay.io/example/updater:latest"
		_, err := x.extractor().Extract(context.Background(), &pkg)
		requireFetchError(t, err, firmware.ReasonUpdaterUnavailable, false)
		x.requireCounts(t, 0, 0, 0, 0)
	})
	t.Run("When the cache lacks the scratch space it should report insufficient space before mounting", func(t *testing.T) {
		x := newExtractHarness(t, defaultPayload(t))
		x.hooks.available = func() (uint64, bool) { return 1, true }
		_, err := x.extractor().Extract(context.Background(), x.pkg)
		requireFetchError(t, err, firmware.ReasonInsufficientSpace, false)
		x.requireCounts(t, 0, 0, 0, 0)
	})
	t.Run("When the mount fails it should not unmount", func(t *testing.T) {
		require := require.New(t)
		x := newExtractHarness(t, defaultPayload(t))
		x.mounter.mountErr = errors.New("mount refused")
		_, err := x.extractor().Extract(context.Background(), x.pkg)
		require.Error(err)
		x.requireCounts(t, 1, 0, 0, 0)
		require.Empty(x.entries())
	})
}

func TestExtractUnmountCleanup(t *testing.T) {
	t.Run("When the caller cancels during staging it should still unmount with an independent deadline", func(t *testing.T) {
		require := require.New(t)
		x := newExtractHarness(t, defaultPayload(t))
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		x.hooks.copy = func(string, string) error {
			x.copies++
			cancel()
			return nil
		}
		start := time.Now()
		_, err := x.extractor(firmware.WithUnmountTimeout(time.Minute)).Extract(ctx, x.pkg)
		require.ErrorIs(err, context.Canceled)
		require.Equal(1, x.mounter.unmounts)
		require.NoError(x.mounter.unmountCtxErr, "the unmount context is not canceled")
		require.WithinDuration(start.Add(time.Minute), x.mounter.unmountDeadline, 10*time.Second)
		require.Empty(x.entries(), "no final, staging or lease remains")
	})
	t.Run("When the extraction deadline expires during staging it should report the time budget and still unmount", func(t *testing.T) {
		require := require.New(t)
		x := newExtractHarness(t, defaultPayload(t))
		x.hooks.copy = func(string, string) error {
			x.copies++
			time.Sleep(1200 * time.Millisecond)
			return nil
		}
		release := withExtractionBudget(baseRelease(t), func() catalog.ExtractionLimits {
			l := testExtraction()
			l.MaxExtractionSeconds = 1
			return l
		}())
		x.refetch(t, release)
		_, err := x.extractor().Extract(context.Background(), x.pkg)
		requireBudget(t, err, "maxExtractionSeconds")
		require.Equal(1, x.mounter.unmounts)
		require.Empty(x.entries())
	})
	t.Run("When the unmount times out it should report the unmount failure and discard the staging", func(t *testing.T) {
		require := require.New(t)
		x := newExtractHarness(t, defaultPayload(t))
		x.mounter.unmountBlock = true
		start := time.Now()
		_, err := x.extractor(firmware.WithUnmountTimeout(50*time.Millisecond)).Extract(context.Background(), x.pkg)
		require.Less(time.Since(start), 5*time.Second)
		require.ErrorIs(err, firmware.ErrUnmountFailed)
		require.ErrorIs(err, context.DeadlineExceeded)
		requireFetchError(t, err, firmware.ReasonUpdaterUnavailable, true)
		requirePublishError(t, err, firmware.PublishNotVisible, "unmount")
		require.Empty(x.entries())
		require.Equal("RemoveTree staging", x.events.events[len(x.events.events)-1])
	})
	t.Run("When the unmount fails after staging it should not publish and report the leaked mount", func(t *testing.T) {
		require := require.New(t)
		x := newExtractHarness(t, defaultPayload(t))
		x.mounter.unmountErr = errors.New("target is busy")
		_, err := x.extractor().Extract(context.Background(), x.pkg)
		require.ErrorIs(err, firmware.ErrUnmountFailed)
		requireFetchError(t, err, firmware.ReasonUpdaterUnavailable, true)
		requirePublishError(t, err, firmware.PublishNotVisible, "unmount")
		require.Equal(x.pkg.Reference, deviceerrors.GetElement(err))
		require.Contains(err.Error(), testMountPoint)
		require.Contains(x.logs.String(), testMountPoint)
		require.Empty(x.entries())
		_, ok, err := x.retention.PinFor(x.pkg.PackageDigest)
		require.NoError(err)
		require.False(ok)
		require.NotContains(x.events.events, "Rename staging->final")
	})
	t.Run("When staging fails and the unmount fails too it should keep the staging reason", func(t *testing.T) {
		require := require.New(t)
		x := newExtractHarness(t, defaultPayload(t))
		x.mounter.unmountErr = errors.New("target is busy")
		require.NoError(os.WriteFile(x.imageFile("payload/extra"), []byte("x"), 0o600))
		_, err := x.extractor().Extract(context.Background(), x.pkg)
		requireFetchError(t, err, firmware.ReasonUpdaterUnavailable, false)
		require.ErrorIs(err, firmware.ErrUnmountFailed)
		require.Empty(x.entries())
	})
	t.Run("When a budget failure and an unmount failure combine it should keep the exceeded budget", func(t *testing.T) {
		require := require.New(t)
		x := newExtractHarness(t, defaultPayload(t))
		x.mounter.unmountErr = errors.New("target is busy")
		require.NoError(os.WriteFile(x.imageFile("manifest.json"), append(x.manifest, make([]byte, 70000)...), 0o600))
		_, err := x.extractor().Extract(context.Background(), x.pkg)
		requireBudget(t, err, "maxExtractedManifestBytes")
		require.ErrorIs(err, firmware.ErrUnmountFailed)
	})
	t.Run("When staging fails it should unmount before discarding and never publish", func(t *testing.T) {
		require := require.New(t)
		x := newExtractHarness(t, defaultPayload(t))
		require.NoError(os.Chmod(x.imageFile("payload/bin/updater"), 0o755|os.ModeSetuid))
		_, err := x.extractor().Extract(context.Background(), x.pkg)
		requireFetchError(t, err, firmware.ReasonUpdaterUnavailable, false)
		require.Equal([]string{"Mount", "CloseSource", "Unmount"}, x.events.events)
		require.Empty(x.entries())
	})
}

func (x *extractHarness) refetch(t *testing.T, release catalog.ReleaseBinding) {
	t.Helper()
	x.approve(release)
	doc := manifestFor(release)
	list := make([]any, 0, len(x.files))
	for _, f := range x.files {
		list = append(list, map[string]any{"path": f.path, "sha256": sha256Hex(f.data), "size": len(f.data), "executable": f.exec})
	}
	doc["files"] = list
	doc["entrypoint"] = x.files[0].path
	x.setManifest(doc)
	pkg, err := x.fetcher().Fetch(context.Background(), release.Name, release.Version)
	require.NoError(t, err)
	x.pkg = pkg
}

func TestExtractEEXISTSequence(t *testing.T) {
	plantOnFirstPublish := func(x *extractHarness, plant func()) {
		done := false
		record := x.hooks.rename
		x.hooks.rename = func(old, new string) error {
			if !done && strings.HasPrefix(old, ".staging-") {
				done = true
				plant()
			}
			return record(old, new)
		}
	}
	validWinner := func(t *testing.T, x *extractHarness) {
		t.Helper()
		other := newExtractHarness(t, x.files)
		_, err := other.extractor().Extract(context.Background(), other.pkg)
		require.NoError(t, err)
		require.NoError(t, os.Rename(other.cacheDir(other.finalName()), x.cacheDir(x.finalName())))
	}

	t.Run("When a valid winner appears it should adopt it, keep it untouched and discard its own staging", func(t *testing.T) {
		require := require.New(t)
		x := newExtractHarness(t, defaultPayload(t))
		var winnerIno uint64
		plantOnFirstPublish(x, func() {
			validWinner(t, x)
			winnerIno = inode(t, x.cacheDir(x.finalName()))
		})
		payload, err := x.extractor().Extract(context.Background(), x.pkg)
		require.NoError(err)
		require.True(payload.Reused)
		require.Equal(winnerIno, inode(t, x.cacheDir(x.finalName())))
		require.Equal([]string{x.finalName()}, x.entries())
		require.NotContains(x.events.events, "Rename final->quarantine")
		tail := x.events.events[len(x.events.events)-5:]
		require.Equal([]string{"Rename staging->final", "Sync", "Revalidate", "Evidence", "RemoveTree staging"}, tail)
	})
	t.Run("When an invalid unpinned winner appears it should quarantine it once and publish its own staging", func(t *testing.T) {
		require := require.New(t)
		x := newExtractHarness(t, defaultPayload(t))
		var stagingIno, winnerIno uint64
		plantOnFirstPublish(x, func() {
			for _, e := range x.entries() {
				if strings.HasPrefix(e, ".staging-") {
					stagingIno = inode(t, x.cacheDir(e))
				}
			}
			require.NoError(os.MkdirAll(x.cacheDir(x.finalName(), "junk"), 0o700))
			winnerIno = inode(t, x.cacheDir(x.finalName()))
		})
		payload, err := x.extractor().Extract(context.Background(), x.pkg)
		require.NoError(err)
		require.False(payload.Reused)
		require.Equal(stagingIno, inode(t, x.cacheDir(x.finalName())), "the retry published the staged directory")
		require.Len(payload.Cleanup.Pending, 1)
		require.Equal(winnerIno, inode(t, x.cacheDir(payload.Cleanup.Pending[0])))
		require.Equal(1, x.mounter.mounts)
		require.Equal(len(x.files), x.copies, "nothing is copied again")
		after := x.events.events[indexOf(x.events.events, "Unmount"):]
		require.Equal([]string{"Unmount", "Rename staging->final", "Rename final->quarantine", "Rename staging->final", "Sync", "Revalidate", "Evidence"}, after)
	})
	t.Run("When a symlink is planted at the final it should quarantine the link and keep its target", func(t *testing.T) {
		require := require.New(t)
		x := newExtractHarness(t, defaultPayload(t))
		target := filepath.Join(x.root, "target")
		require.NoError(os.MkdirAll(target, 0o700))
		require.NoError(os.WriteFile(filepath.Join(target, "keep"), []byte("x"), 0o600))
		plantOnFirstPublish(x, func() { require.NoError(os.Symlink(target, x.cacheDir(x.finalName()))) })
		payload, err := x.extractor().Extract(context.Background(), x.pkg)
		require.NoError(err)
		info, err := os.Lstat(x.cacheDir(payload.Cleanup.Pending[0]))
		require.NoError(err)
		require.Equal(os.ModeSymlink, info.Mode().Type())
		_, err = os.Stat(filepath.Join(target, "keep"))
		require.NoError(err)
	})
	t.Run("When a second invalid winner appears it should not quarantine again and report the contention", func(t *testing.T) {
		require := require.New(t)
		x := newExtractHarness(t, defaultPayload(t))
		publishes := 0
		record := x.hooks.rename
		x.hooks.rename = func(old, new string) error {
			if strings.HasPrefix(old, ".staging-") {
				publishes++
				require.NoError(os.MkdirAll(x.cacheDir(x.finalName(), fmt.Sprintf("junk-%d", publishes)), 0o700))
			}
			return record(old, new)
		}
		_, err := x.extractor().Extract(context.Background(), x.pkg)
		require.ErrorIs(err, firmware.ErrPublishContended)
		requireFetchError(t, err, firmware.ReasonUpdaterUnavailable, true)
		requirePublishError(t, err, firmware.PublishNotVisible, "rename-retry")
		quarantines := 0
		for _, e := range x.events.events {
			if e == "Rename final->quarantine" {
				quarantines++
			}
		}
		require.Equal(1, quarantines)
		_, err = os.Stat(x.cacheDir(x.finalName(), "junk-2"))
		require.NoError(err, "the new winner is untouched")
		for _, e := range x.entries() {
			require.False(strings.HasPrefix(e, ".staging-"), "the staging is discarded")
		}
	})
	t.Run("When a second winner is valid it should adopt it", func(t *testing.T) {
		require := require.New(t)
		x := newExtractHarness(t, defaultPayload(t))
		publishes := 0
		record := x.hooks.rename
		x.hooks.rename = func(old, new string) error {
			if strings.HasPrefix(old, ".staging-") {
				publishes++
				if publishes == 1 {
					require.NoError(os.MkdirAll(x.cacheDir(x.finalName(), "junk"), 0o700))
				} else {
					validWinner(t, x)
				}
			}
			return record(old, new)
		}
		payload, err := x.extractor().Extract(context.Background(), x.pkg)
		require.NoError(err)
		require.True(payload.Reused)
		require.Len(payload.Cleanup.Pending, 1)
	})
	t.Run("When the quarantine rename fails it should leave the winner and discard its staging", func(t *testing.T) {
		require := require.New(t)
		x := newExtractHarness(t, defaultPayload(t))
		plantOnFirstPublish(x, func() { require.NoError(os.MkdirAll(x.cacheDir(x.finalName(), "junk"), 0o700)) })
		record := x.hooks.rename
		x.hooks.rename = func(old, new string) error {
			if strings.HasPrefix(new, ".quarantine-") {
				return fmt.Errorf("injected: %w", unix.EIO)
			}
			return record(old, new)
		}
		_, err := x.extractor().Extract(context.Background(), x.pkg)
		requirePublishError(t, err, firmware.PublishNotVisible, "quarantine")
		requireFetchError(t, err, firmware.ReasonUpdaterUnavailable, true)
		require.Equal([]string{x.finalName()}, x.entries())
		_, err = os.Stat(x.cacheDir(x.finalName(), "junk"))
		require.NoError(err)
	})
	t.Run("When the winner cannot be classified it should leave it and discard its staging", func(t *testing.T) {
		require := require.New(t)
		x := newExtractHarness(t, defaultPayload(t))
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		plantOnFirstPublish(x, func() {
			require.NoError(os.MkdirAll(x.cacheDir(x.finalName(), "junk"), 0o700))
			cancel()
		})
		_, err := x.extractor().Extract(ctx, x.pkg)
		require.ErrorIs(err, context.Canceled)
		requirePublishError(t, err, firmware.PublishNotVisible, "classify")
		var fetchErr *firmware.FetchError
		require.False(errors.As(err, &fetchErr), "caller cancellation has no firmware reason")
		require.Equal([]string{x.finalName()}, x.entries())
	})
	t.Run("When an invalid winner is pinned it should fail closed and leave it", func(t *testing.T) {
		require := require.New(t)
		x := newExtractHarness(t, defaultPayload(t))
		plantOnFirstPublish(x, func() {
			require.NoError(os.MkdirAll(x.cacheDir(x.finalName(), "junk"), 0o700))
			pin := firmware.Pin{AttemptID: "a", Role: firmware.PinRoleActive, Reference: x.pkg.Reference, PackageDigest: x.pkg.PackageDigest, ManifestDigest: x.pkg.ManifestDigest, DescriptorDigest: x.pkg.DescriptorDigest, PayloadDir: x.finalName(), Limits: asPolicyLimits(testExtraction())}
			data, err := json.Marshal(map[string]any{"apiVersion": "updater-retention/v1", "pins": []firmware.Pin{pin}})
			require.NoError(err)
			require.NoError(os.WriteFile(filepath.Join(x.root, testDataDir, "firmware", "retained.json"), data, 0o600))
		})
		_, err := x.extractor().Extract(context.Background(), x.pkg)
		require.ErrorIs(err, firmware.ErrRetainedPayloadChanged)
		require.Equal([]string{x.finalName()}, x.entries())
		_, err = os.Stat(x.cacheDir(x.finalName(), "junk"))
		require.NoError(err)
	})
	t.Run("When discarding the staging fails after adopting a winner it should report the pending staging", func(t *testing.T) {
		require := require.New(t)
		x := newExtractHarness(t, defaultPayload(t))
		plantOnFirstPublish(x, func() { validWinner(t, x) })
		x.hooks.removeTree = func(name string) (fileio.RemoveResult, error, bool) {
			return fileio.RemoveResult{Stop: fileio.RemoveError}, errors.New("injected removal failure"), true
		}
		payload, err := x.extractor().Extract(context.Background(), x.pkg)
		require.NoError(err)
		require.True(payload.Reused)
		require.Len(payload.Cleanup.Pending, 1)
		require.True(strings.HasPrefix(payload.Cleanup.Pending[0], ".staging-"))
		require.NotEmpty(payload.Cleanup.Err)
		require.LessOrEqual(len(payload.Cleanup.Err), 256)
	})
}

func indexOf(events []string, e string) int {
	for i, v := range events {
		if v == e {
			return i
		}
	}
	return -1
}

func TestExtractEvidencePersistence(t *testing.T) {
	require := require.New(t)
	x := newExtractHarness(t, defaultPayload(t))
	record := x.hooks.replace
	x.hooks.replace = func(dir, name string) error {
		if strings.HasSuffix(dir, "/evidence") {
			return fmt.Errorf("injected: %w", unix.EIO)
		}
		return record(dir, name)
	}
	_, err := x.extractor().Extract(context.Background(), x.pkg)
	require.ErrorIs(err, firmware.ErrEvidenceNotPersisted)
	requireFetchError(t, err, firmware.ReasonUpdaterUnavailable, true)
	requirePublishError(t, err, firmware.PublishVisibleNotUsable, "evidence")
	require.Equal([]string{x.finalName()}, x.entries(), "the complete final is kept")
	removed, err := x.retention.CleanupUnreferenced(context.Background())
	require.NoError(err)
	require.Contains(removed, x.finalName(), "a final that was never usable holds no lease")

	x2 := newExtractHarness(t, defaultPayload(t))
	x2.hooks.replace = func(dir, name string) error {
		if strings.HasSuffix(dir, "/evidence") {
			return fmt.Errorf("injected: %w", unix.EIO)
		}
		return nil
	}
	_, err = x2.extractor().Extract(context.Background(), x2.pkg)
	require.ErrorIs(err, firmware.ErrEvidenceNotPersisted)
	x2.hooks.replace = nil
	payload, err := x2.extractor().Extract(context.Background(), x2.pkg)
	require.NoError(err)
	require.True(payload.Reused)
	require.True(payload.Evidence.RecordedAfterPublication)
}

func TestExtractLeases(t *testing.T) {
	t.Run("When a usable payload is leased cleanup keeps it until it is pinned or released", func(t *testing.T) {
		require := require.New(t)
		x := newExtractHarness(t, defaultPayload(t))
		payload, err := x.extractor().Extract(context.Background(), x.pkg)
		require.NoError(err)
		removed, err := x.retention.CleanupUnreferenced(context.Background())
		require.NoError(err)
		require.NotContains(removed, payload.Dir)
		x.pin(t, payload)
		removed, err = x.retention.CleanupUnreferenced(context.Background())
		require.NoError(err)
		require.NotContains(removed, payload.Dir)
		require.NoError(x.retention.Release("attempt-1"))
		removed, err = x.retention.CleanupUnreferenced(context.Background())
		require.NoError(err)
		require.Contains(removed, payload.Dir)
	})
	t.Run("When a lease is released the payload becomes removable", func(t *testing.T) {
		require := require.New(t)
		x := newExtractHarness(t, defaultPayload(t))
		payload, err := x.extractor().Extract(context.Background(), x.pkg)
		require.NoError(err)
		x.retention.ReleaseLease(payload)
		removed, err := x.retention.CleanupUnreferenced(context.Background())
		require.NoError(err)
		require.Contains(removed, payload.Dir)
	})
	t.Run("When another retention handle cleans the cache it should preserve live leases", func(t *testing.T) {
		require := require.New(t)
		x := newExtractHarness(t, defaultPayload(t))
		payload, err := x.extractor().Extract(context.Background(), x.pkg)
		require.NoError(err)
		other := firmware.NewRetention(testDataDir, x.rw(), x.logger)
		removed, err := other.CleanupUnreferenced(context.Background())
		require.NoError(err)
		require.Empty(removed)
		other.ReleaseLease(payload)
		removed, err = other.CleanupUnreferenced(context.Background())
		require.NoError(err)
		require.Contains(removed, payload.Dir)
	})
}

func TestExtractInvalidatesStaleCachedManifest(t *testing.T) {
	for _, refresh := range []bool{false, true} {
		name := "When the cached digest is stale it should require a verified fetch"
		if refresh {
			name = "When a verified refresh replaced the stale record it should preserve the new record"
		}
		t.Run(name, func(t *testing.T) {
			x := newExtractHarness(t, defaultPayload(t))
			ctx := context.Background()
			fetcher := x.fetcher()
			if refresh {
				published, err := x.extractor().Extract(ctx, x.pkg)
				require.NoError(t, err)
				x.retention.ReleaseLease(published)
			}
			x.editRecord(func(r map[string]any) {
				r["manifestDigest"] = policy.Hash([]byte("stale manifest"))
			})
			x.podman.existsResult = 0
			stale, err := fetcher.Fetch(ctx, x.pkg.Name, x.pkg.Version)
			require.NoError(t, err)
			require.True(t, stale.Cached)
			if refresh {
				x.podman.existsResult = 1
				_, err := x.fetcher().Fetch(ctx, x.pkg.Name, x.pkg.Version)
				require.NoError(t, err)
				x.podman.existsResult = 0
			}
			_, err = x.extractor().Extract(ctx, stale)
			require.ErrorContains(t, err, "fetch the approved package again")
			if refresh {
				require.Equal(t, []string{x.finalName()}, x.entries())
			} else {
				require.Empty(t, x.entries())
				require.NoFileExists(t, x.recordPath())
			}
			current, err := fetcher.Fetch(ctx, x.pkg.Name, x.pkg.Version)
			require.NoError(t, err)
			require.Equal(t, refresh, current.Cached)
			require.Equal(t, x.pkg.ManifestDigest, current.ManifestDigest)
			_, err = x.extractor().Extract(ctx, current)
			require.NoError(t, err)
		})
	}
}

func TestExtractPreservesPayloadAfterTransientReadError(t *testing.T) {
	x := newExtractHarness(t, defaultPayload(t))
	ctx := context.Background()
	published, err := x.extractor().Extract(ctx, x.pkg)
	require.NoError(t, err)
	x.retention.ReleaseLease(published)
	x.resetCounts()
	x.hooks.hash = func(_, _ string) (fileio.FileDigest, error, bool) {
		return fileio.FileDigest{}, unix.EIO, true
	}
	_, err = x.extractor().Extract(ctx, x.pkg)
	require.ErrorIs(t, err, firmware.ErrRetainedVerificationIncomplete)
	require.ErrorIs(t, err, unix.EIO)
	require.Equal(t, []string{published.Dir}, x.entries())
	x.requireCounts(t, 0, 0, 0, 0)
}
