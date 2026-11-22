package firmware_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	deviceerrors "github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/agent/device/firmware"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

// retentionOps runs every retention operation that reads the registry and
// returns its error by operation name.
func retentionOps(r *firmware.Retention, pin firmware.Pin) map[string]error {
	ctx := context.Background()
	errs := map[string]error{}
	_, errs["Pins"] = r.Pins()
	_, _, errs["PinFor"] = r.PinFor(pin.PackageDigest)
	_, errs["RetainedImageReferences"] = r.RetainedImageReferences(ctx)
	errs["Pin"] = r.Pin(pin)
	errs["Release"] = r.Release(pin.AttemptID)
	_, errs["CleanupUnreferenced"] = r.CleanupUnreferenced(ctx)
	return errs
}

// treeListing lists every path beneath dir with its type and permissions
// without following symbolic links.
func treeListing(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	require.NoError(t, filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		out = append(out, fmt.Sprintf("%s %s", strings.TrimPrefix(p, dir), info.Mode()))
		return nil
	}))
	return out
}

func skipAsRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root bypasses permission checks")
	}
}

func evidenceDir(f *payloadFixture) string {
	return filepath.Join(f.root, "data", "firmware", "evidence")
}

func TestRetentionRegistryRejectsInconsistentContent(t *testing.T) {
	registry := func(t *testing.T, pins ...firmware.Pin) []byte {
		t.Helper()
		data, err := json.Marshal(map[string]any{"apiVersion": "updater-retention/v1", "pins": pins})
		require.NoError(t, err)
		return data
	}
	testCases := []struct {
		name    string
		content func(t *testing.T, pin firmware.Pin) []byte
		want    error
	}{
		{name: "When the registry has trailing content every operation should fail closed", content: func(t *testing.T, pin firmware.Pin) []byte {
			return append(registry(t, pin), []byte(" {}")...)
		}},
		{name: "When the registry holds an invalid pin every operation should fail closed", content: func(t *testing.T, pin firmware.Pin) []byte {
			pin.Role = "spare"
			return registry(t, pin)
		}},
		{name: "When the registry pins an attempt in one role twice every operation should fail closed", content: func(t *testing.T, pin firmware.Pin) []byte {
			return registry(t, pin, pin)
		}},
		{name: "When the registry gives one package two identities every operation should fail closed", content: func(t *testing.T, pin firmware.Pin) []byte {
			other := pin
			other.AttemptID = "attempt-other"
			other.ManifestDigest = "sha256:" + strings.Repeat("0", 64)
			return registry(t, pin, other)
		}, want: firmware.ErrPinConflict},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			f := newPayloadFixture(t, defaultPayload(t))
			pin := publishVersion(t, f, 'a')
			content := tc.content(t, pin)
			require.NoError(os.WriteFile(registryPath(f), content, 0o600))
			for op, err := range retentionOps(newRetention(f), pin) {
				require.Error(err, op)
				if tc.want != nil {
					require.ErrorIs(err, tc.want, op)
				}
			}
			got, err := os.ReadFile(registryPath(f))
			require.NoError(err)
			require.Equal(content, got, "a rejected registry is never rewritten")
			_, err = os.Stat(f.cacheDir(pin.PayloadDir))
			require.NoError(err, "a rejected registry never leads to deletion")
		})
	}
}

func TestRetentionPinsSeveralAttempts(t *testing.T) {
	t.Run("When several attempts are pinned it should store them in attempt order and release only the named attempt", func(t *testing.T) {
		require := require.New(t)
		f := newPayloadFixture(t, defaultPayload(t))
		later := publishVersion(t, f, 'a')
		earlier := publishVersion(t, f, 'b')
		earlier.AttemptID = "attempt-0"
		r := newRetention(f)
		require.NoError(r.Pin(later))
		require.NoError(r.Pin(earlier))
		data, err := os.ReadFile(registryPath(f))
		require.NoError(err)
		var doc struct {
			Pins []firmware.Pin `json:"pins"`
		}
		require.NoError(json.Unmarshal(data, &doc))
		require.Equal([]firmware.Pin{earlier, later}, doc.Pins)

		require.NoError(r.Release(earlier.AttemptID))
		pins, err := r.Pins()
		require.NoError(err)
		require.Equal([]firmware.Pin{later}, pins)
		_, err = r.VerifyPublished(context.Background(), later)
		require.NoError(err)
	})
}

func TestRetentionFailsClosedOnUntrustedDirectories(t *testing.T) {
	testCases := []struct {
		name    string
		corrupt func(t *testing.T, f *payloadFixture)
	}{
		{name: "When the data directory is writable by others every operation should fail closed", corrupt: func(t *testing.T, f *payloadFixture) {
			dir := filepath.Join(f.root, "data")
			require.NoError(t, os.Chmod(dir, 0o777))
			t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
		}},
		{name: "When the firmware directory is a symlink every operation should fail closed", corrupt: func(t *testing.T, f *payloadFixture) {
			dir := filepath.Join(f.root, "data", "firmware")
			require.NoError(t, os.Rename(dir, dir+"-real"))
			require.NoError(t, os.Symlink(dir+"-real", dir))
		}},
		{name: "When the updater cache is writable by others every operation should fail closed", corrupt: func(t *testing.T, f *payloadFixture) {
			require.NoError(t, os.Chmod(f.cacheDir(), 0o777))
			t.Cleanup(func() { _ = os.Chmod(f.cacheDir(), 0o700) })
		}},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			f := newPayloadFixture(t, defaultPayload(t))
			pin := publishVersion(t, f, 'a')
			publishVersion(t, f, 'b')
			tc.corrupt(t, f)
			before := treeListing(t, f.root)
			r := newRetention(f)
			for op, err := range retentionOps(r, pin) {
				require.ErrorIs(err, deviceerrors.ErrUntrustedDirectory, op)
			}
			_, err := r.VerifyPublished(context.Background(), pin)
			require.ErrorIs(err, deviceerrors.ErrUntrustedDirectory)
			requireFetchError(t, err, firmware.ReasonUpdaterUnavailable, false)
			_, err = r.Evidence(pin.PackageDigest)
			require.ErrorIs(err, deviceerrors.ErrUntrustedDirectory)
			require.Equal(before, treeListing(t, f.root), "nothing is created, changed or removed")
		})
	}
}

func TestRetentionWithoutUpdaterCache(t *testing.T) {
	t.Run("When the firmware directory has no updater cache it should stay passive and report pins missing", func(t *testing.T) {
		require := require.New(t)
		f := newPayloadFixture(t, defaultPayload(t))
		require.NoError(os.MkdirAll(filepath.Join(f.root, "data", "firmware", "packages"), 0o700))
		digest, hex := digestFor('a')
		pin := firmware.Pin{
			AttemptID:        "attempt-a",
			Role:             firmware.PinRoleActive,
			Reference:        "quay.io/example/updater@" + digest,
			PackageDigest:    digest,
			ManifestDigest:   "sha256:" + strings.Repeat("e", 64),
			DescriptorDigest: "sha256:" + strings.Repeat("f", 64),
			PayloadDir:       hex,
			Limits:           testLimits(),
		}
		before := treeListing(t, filepath.Join(f.root, "data"))
		r := newRetention(f)
		pins, err := r.Pins()
		require.NoError(err)
		require.Empty(pins)
		refs, err := r.RetainedImageReferences(context.Background())
		require.NoError(err)
		require.Empty(refs)
		removed, err := r.CleanupUnreferenced(context.Background())
		require.NoError(err)
		require.Empty(removed)
		require.NoError(r.Release(pin.AttemptID))
		_, err = r.VerifyPublished(context.Background(), pin)
		require.ErrorIs(err, firmware.ErrRetainedPayloadMissing)
		requireFetchError(t, err, firmware.ReasonUpdaterUnavailable, false)
		_, err = r.Evidence(pin.PackageDigest)
		require.ErrorIs(err, fs.ErrNotExist)
		require.Equal(before, treeListing(t, filepath.Join(f.root, "data")), "passive retention creates nothing")
	})
}

func TestRetentionEvidenceAccessFailures(t *testing.T) {
	setup := func(t *testing.T) (*payloadFixture, firmware.Pin, *firmware.Retention) {
		t.Helper()
		f := newPayloadFixture(t, defaultPayload(t))
		pin := publishVersion(t, f, 'a')
		r := newRetention(f)
		res, err := r.VerifyPublished(context.Background(), pin)
		require.NoError(t, err)
		require.True(t, res.EvidencePersisted)
		return f, pin, r
	}
	otherDigest, otherHex := digestFor('b')

	t.Run("When a package has no evidence record it should report it not found", func(t *testing.T) {
		_, _, r := setup(t)
		_, err := r.Evidence(otherDigest)
		require.ErrorIs(t, err, fs.ErrNotExist)
	})
	invalid := []struct {
		name    string
		reason  string
		rewrite func(t *testing.T, f *payloadFixture, pin firmware.Pin, record []byte) (string, []byte)
	}{
		{
			name:   "When the evidence record has trailing content it should refuse it",
			reason: "malformed record",
			rewrite: func(_ *testing.T, f *payloadFixture, pin firmware.Pin, record []byte) (string, []byte) {
				return evidencePath(f, pin), append(record, []byte(" {}")...)
			},
		},
		{
			name:   "When the evidence record has an unsupported version it should refuse it",
			reason: "unsupported record version",
			rewrite: func(t *testing.T, f *payloadFixture, pin firmware.Pin, record []byte) (string, []byte) {
				var doc map[string]any
				require.NoError(t, json.Unmarshal(record, &doc))
				doc["apiVersion"] = "updater-host-evidence/v9"
				data, err := json.Marshal(doc)
				require.NoError(t, err)
				return evidencePath(f, pin), data
			},
		},
		{
			name:   "When the evidence record belongs to another package it should refuse it",
			reason: "record identity differs",
			rewrite: func(_ *testing.T, f *payloadFixture, _ firmware.Pin, record []byte) (string, []byte) {
				return filepath.Join(evidenceDir(f), otherHex+".json"), record
			},
		},
	}
	for _, tc := range invalid {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			f, pin, r := setup(t)
			record, err := os.ReadFile(evidencePath(f, pin))
			require.NoError(err)
			p, data := tc.rewrite(t, f, pin, record)
			require.NoError(os.WriteFile(p, data, 0o600))
			digest := pin.PackageDigest
			if !strings.HasSuffix(p, pin.PayloadDir+".json") {
				digest = otherDigest
			}
			_, err = r.Evidence(digest)
			require.Error(err)
			require.Contains(err.Error(), tc.reason)
		})
	}
	t.Run("When the evidence directory is a symlink it should refuse evidence access and verify without persisting evidence", func(t *testing.T) {
		require := require.New(t)
		f, pin, r := setup(t)
		dir := evidenceDir(f)
		require.NoError(os.Rename(dir, dir+"-real"))
		require.NoError(os.Symlink(dir+"-real", dir))
		before := treeListing(t, dir+"-real")
		_, err := r.Evidence(pin.PackageDigest)
		require.ErrorIs(err, deviceerrors.ErrUntrustedDirectory)
		res, err := r.VerifyPublished(context.Background(), pin)
		require.NoError(err, "the byte verdict does not depend on evidence persistence")
		require.False(res.EvidencePersisted)
		require.ErrorIs(res.EvidenceErr, firmware.ErrEvidenceNotPersisted)
		require.ErrorIs(res.EvidenceErr, deviceerrors.ErrUntrustedDirectory)
		require.Equal(before, treeListing(t, dir+"-real"))
	})
}

func TestRetentionVerifyPublishedRejects(t *testing.T) {
	t.Run("When the pin is invalid it should refuse to verify it", func(t *testing.T) {
		f := newPayloadFixture(t, defaultPayload(t))
		pin := publishVersion(t, f, 'a')
		pin.Role = "spare"
		_, err := newRetention(f).VerifyPublished(context.Background(), pin)
		require.Error(t, err)
		requireFetchError(t, err, firmware.ReasonUpdaterUnavailable, false)
	})
	t.Run("When the manifest matches the pin but is not a valid manifest it should report the payload changed", func(t *testing.T) {
		require := require.New(t)
		f := newPayloadFixture(t, defaultPayload(t))
		pin := publishVersion(t, f, 'a')
		p := f.cacheDir(pin.PayloadDir, "manifest.json")
		require.NoError(os.Chmod(p, 0o600))
		require.NoError(os.WriteFile(p, []byte("{}"), 0o600))
		require.NoError(os.Chmod(p, 0o400))
		pin.ManifestDigest = "sha256:" + sha256Hex([]byte("{}"))
		_, err := newRetention(f).VerifyPublished(context.Background(), pin)
		require.ErrorIs(err, firmware.ErrRetainedPayloadChanged)
		requireFetchError(t, err, firmware.ReasonUpdaterUnavailable, false)
	})
	t.Run("When a listed file is removed it should report the payload changed", func(t *testing.T) {
		require := require.New(t)
		f := newPayloadFixture(t, defaultPayload(t))
		pin := publishVersion(t, f, 'a')
		require.NoError(os.Remove(f.cacheDir(pin.PayloadDir, "payload", "share", "doc.txt")))
		_, err := newRetention(f).VerifyPublished(context.Background(), pin)
		require.ErrorIs(err, firmware.ErrRetainedPayloadChanged)
		require.Contains(err.Error(), "missing")
	})
	t.Run("When the version directory cannot be inspected it should report the payload changed", func(t *testing.T) {
		skipAsRoot(t)
		require := require.New(t)
		f := newPayloadFixture(t, defaultPayload(t))
		pin := publishVersion(t, f, 'a')
		require.NoError(os.Chmod(f.cacheDir(), 0o400))
		t.Cleanup(func() { _ = os.Chmod(f.cacheDir(), 0o700) })
		_, err := newRetention(f).VerifyPublished(context.Background(), pin)
		require.ErrorIs(err, firmware.ErrRetainedPayloadChanged)
		require.Contains(err.Error(), "permission denied")
	})
}

func TestRetentionCleanupBoundaries(t *testing.T) {
	ctx := context.Background()
	fakeRecord := func(t *testing.T, f *payloadFixture, c byte) string {
		t.Helper()
		_, hex := digestFor(c)
		require.NoError(t, os.MkdirAll(evidenceDir(f), 0o700))
		p := filepath.Join(evidenceDir(f), hex+".json")
		require.NoError(t, os.WriteFile(p, []byte("{}"), 0o600))
		return p
	}

	t.Run("When the updater cache holds a foreign file it should keep it", func(t *testing.T) {
		require := require.New(t)
		f := newPayloadFixture(t, defaultPayload(t))
		unpinned := publishVersion(t, f, 'a')
		require.NoError(os.WriteFile(f.cacheDir("notes.txt"), []byte("x"), 0o600))
		removed, err := newRetention(f).CleanupUnreferenced(ctx)
		require.NoError(err)
		require.Equal([]string{unpinned.PayloadDir}, removed)
		require.Equal([]string{"notes.txt"}, f.cacheEntries())
	})
	t.Run("When a stale entry cannot be removed it should report the error and continue with the others", func(t *testing.T) {
		skipAsRoot(t)
		require := require.New(t)
		f := newPayloadFixture(t, defaultPayload(t))
		unpinned := publishVersion(t, f, 'a')
		locked := f.cacheDir(".staging-0123456789abcdef", "locked")
		require.NoError(os.MkdirAll(locked, 0o700))
		require.NoError(os.WriteFile(filepath.Join(locked, "f"), []byte("x"), 0o600))
		require.NoError(os.Chmod(locked, 0o500))
		t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })
		removed, err := newRetention(f).CleanupUnreferenced(ctx)
		require.Error(err)
		require.ErrorIs(err, fs.ErrPermission)
		require.Equal([]string{unpinned.PayloadDir}, removed)
		_, statErr := os.Stat(filepath.Join(locked, "f"))
		require.NoError(statErr)
	})
	t.Run("When the evidence directory holds a subdirectory it should keep it", func(t *testing.T) {
		require := require.New(t)
		f := newPayloadFixture(t, defaultPayload(t))
		pin := publishVersion(t, f, 'a')
		r := newRetention(f)
		require.NoError(r.Pin(pin))
		_, err := r.VerifyPublished(ctx, pin)
		require.NoError(err)
		_, hex := digestFor('c')
		sub := filepath.Join(evidenceDir(f), hex+".json")
		require.NoError(os.Mkdir(sub, 0o700))
		removed, err := r.CleanupUnreferenced(ctx)
		require.NoError(err)
		require.Empty(removed)
		_, statErr := os.Stat(sub)
		require.NoError(statErr)
	})
	t.Run("When an orphan evidence record cannot be removed it should report the error and keep it", func(t *testing.T) {
		skipAsRoot(t)
		require := require.New(t)
		f := newPayloadFixture(t, defaultPayload(t))
		f.cache()
		orphan := fakeRecord(t, f, 'c')
		require.NoError(os.Chmod(evidenceDir(f), 0o500))
		t.Cleanup(func() { _ = os.Chmod(evidenceDir(f), 0o700) })
		removed, err := newRetention(f).CleanupUnreferenced(ctx)
		require.ErrorIs(err, fs.ErrPermission)
		require.Empty(removed)
		_, statErr := os.Stat(orphan)
		require.NoError(statErr)
	})
	t.Run("When the evidence listing exceeds the listing cap it should report the error and keep the pinned record", func(t *testing.T) {
		require := require.New(t)
		f := newPayloadFixture(t, defaultPayload(t))
		pin := publishVersion(t, f, 'a')
		r := newRetention(f, firmware.WithCleanupLimits(firmware.CleanupTreesPerPass, 2, firmware.CleanupPassMaxWork))
		require.NoError(r.Pin(pin))
		_, err := r.VerifyPublished(ctx, pin)
		require.NoError(err)
		fakeRecord(t, f, 'c')
		fakeRecord(t, f, 'd')
		fakeRecord(t, f, 'e')
		_, err = r.CleanupUnreferenced(ctx)
		require.ErrorIs(err, deviceerrors.ErrWalkLimitExceeded)
		require.Contains(err.Error(), "list evidence")
		_, err = os.Stat(evidencePath(f, pin))
		require.NoError(err)
		_, err = r.Evidence(pin.PackageDigest)
		require.NoError(err)
	})
	passWork := []struct {
		name     string
		passWork int
		records  []byte
	}{
		{name: "When the pass work runs out while listing evidence it should stop and keep the records", passWork: 3, records: []byte("c")},
		{name: "When the pass work runs out before removing an orphan record it should stop and keep it", passWork: 4, records: []byte("c")},
	}
	for _, tc := range passWork {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			f := newPayloadFixture(t, defaultPayload(t))
			pin := publishVersion(t, f, 'a')
			r := newRetention(f, firmware.WithCleanupLimits(firmware.CleanupTreesPerPass, firmware.MaxCacheListingEntries, tc.passWork))
			require.NoError(r.Pin(pin))
			var records []string
			for _, c := range tc.records {
				records = append(records, fakeRecord(t, f, c))
			}
			removed, err := r.CleanupUnreferenced(ctx)
			require.Error(err)
			require.Contains(err.Error(), "cleanup pass work limit reached")
			require.Empty(removed)
			entries, readErr := os.ReadDir(evidenceDir(f))
			require.NoError(readErr)
			names := make([]string, 0, len(entries))
			for _, e := range entries {
				names = append(names, filepath.Join(evidenceDir(f), e.Name()))
			}
			sort.Strings(names)
			require.Equal(records, names)
		})
	}
}

func (a hooked) Hash(ctx context.Context, rel string, b fileio.CopyBounds) (fileio.FileDigest, error) {
	if a.h.hash != nil {
		if d, err, ok := a.h.hash(a.Path(), rel); ok {
			return d, err
		}
	}
	return a.Anchor.Hash(ctx, rel, b)
}

// lateCapabilityFailure fails the first hash of the retained file rel the way
// fileio reports file capabilities that cannot be read while hashing, after
// the explicit capability check has passed. It counts every hash of rel.
type lateCapabilityFailure struct {
	rel    string
	hashes int
}

func (l *lateCapabilityFailure) hook(dir, rel string) (fileio.FileDigest, error, bool) {
	if rel != l.rel {
		return fileio.FileDigest{}, nil, false
	}
	l.hashes++
	if l.hashes > 1 {
		return fileio.FileDigest{}, nil, false
	}
	display := dir + "/" + rel
	cause := fmt.Errorf("read attribute security.capability of %w: %w", deviceerrors.WithElement(display), unix.EIO)
	return fileio.FileDigest{}, fmt.Errorf("%w %w: %w", deviceerrors.ErrFileCapabilityUnknown, deviceerrors.WithElement(display), cause), true
}

func (a hooked) Lstat(rel string) (fileio.EntryInfo, error) {
	if a.h.lstat != nil {
		if e, err, ok := a.h.lstat(a.Path(), rel); ok {
			return e, err
		}
	}
	return a.Anchor.Lstat(rel)
}

func TestRetentionPinInspectionErrors(t *testing.T) {
	otherPin := func(pin firmware.Pin) firmware.Pin {
		digest, hex := digestFor('7')
		pin.PackageDigest, pin.PayloadDir, pin.Reference = digest, hex, "quay.io/example/updater@"+digest
		return pin
	}
	testCases := []struct {
		name    string
		prepare func(t *testing.T, f *payloadFixture, pin firmware.Pin) *hooks
		want    error
		notWant []error
	}{
		{
			name: "When the version name is a regular file it should report the payload changed",
			prepare: func(t *testing.T, f *payloadFixture, pin firmware.Pin) *hooks {
				require.NoError(t, os.WriteFile(f.cacheDir(pin.PayloadDir), []byte("x"), 0o600))
				return nil
			},
			want:    firmware.ErrRetainedPayloadChanged,
			notWant: []error{firmware.ErrRetainedPayloadMissing},
		},
		{
			name: "When the version name is a symlink it should report the payload changed without following it",
			prepare: func(t *testing.T, f *payloadFixture, pin firmware.Pin) *hooks {
				require.NoError(t, os.Symlink(f.cacheDir(), f.cacheDir(pin.PayloadDir)))
				return nil
			},
			want:    firmware.ErrRetainedPayloadChanged,
			notWant: []error{firmware.ErrRetainedPayloadMissing},
		},
		{
			name: "When the version directory cannot be inspected it should keep the permission error",
			prepare: func(t *testing.T, f *payloadFixture, pin firmware.Pin) *hooks {
				skipAsRoot(t)
				require.NoError(t, os.Chmod(f.cacheDir(), 0o400))
				t.Cleanup(func() { _ = os.Chmod(f.cacheDir(), 0o700) })
				return nil
			},
			want:    fs.ErrPermission,
			notWant: []error{firmware.ErrRetainedPayloadMissing, firmware.ErrRetainedPayloadChanged},
		},
		{
			name: "When inspecting the version directory fails with an I/O error it should keep that cause",
			prepare: func(_ *testing.T, _ *payloadFixture, pin firmware.Pin) *hooks {
				return &hooks{lstat: func(_, rel string) (fileio.EntryInfo, error, bool) {
					if rel == pin.PayloadDir {
						return fileio.EntryInfo{}, fmt.Errorf("injected: %w", unix.EIO), true
					}
					return fileio.EntryInfo{}, nil, false
				}}
			},
			want:    unix.EIO,
			notWant: []error{firmware.ErrRetainedPayloadMissing, firmware.ErrRetainedPayloadChanged},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			f := newPayloadFixture(t, defaultPayload(t))
			pin := otherPin(publishVersion(t, f, 'a'))
			h := tc.prepare(t, f, pin)
			err := evidenceRetention(f, h).Pin(pin)
			require.ErrorIs(err, tc.want)
			for _, e := range tc.notWant {
				require.NotErrorIs(err, e)
			}
			_, statErr := os.Stat(registryPath(f))
			require.ErrorIs(statErr, fs.ErrNotExist, "a failed pin persists nothing")
		})
	}
}

// capabilityHooks reports the security.capability attribute of the retained
// entry rel with state and err, leaving every other attribute untouched.
func capabilityHooks(rel string, state fileio.XattrState, err error) *hooks {
	return &hooks{xattr: func(r, attr string) ([]byte, fileio.XattrState, error, bool) {
		if attr != "security.capability" || r != rel {
			return nil, "", nil, false
		}
		if state == fileio.XattrPresent {
			return []byte{0, 0, 0, 2}, state, nil, true
		}
		return nil, state, err, true
	}}
}

// expiringContext reports an expired deadline once done is closed.
type expiringContext struct {
	context.Context
	done chan struct{}
}

func (c *expiringContext) Done() <-chan struct{} { return c.done }

func (c *expiringContext) Err() error {
	select {
	case <-c.done:
		return context.DeadlineExceeded
	default:
		return nil
	}
}

func TestRetentionVerifyPublishedCapabilities(t *testing.T) {
	ioErr := fmt.Errorf("injected: %w", unix.EIO)
	testCases := []struct {
		name    string
		hooks   *hooks
		want    error
		notWant []error
		cause   error
	}{
		{name: "When the manifest carries file capabilities it should report the payload changed", hooks: capabilityHooks("manifest.json", fileio.XattrPresent, nil), want: firmware.ErrRetainedPayloadChanged},
		{name: "When the executable carries file capabilities it should report the payload changed", hooks: capabilityHooks("payload/bin/updater", fileio.XattrPresent, nil), want: firmware.ErrRetainedPayloadChanged},
		{name: "When a data file carries file capabilities it should report the payload changed", hooks: capabilityHooks("payload/share/doc.txt", fileio.XattrPresent, nil), want: firmware.ErrRetainedPayloadChanged},
		{
			name:    "When the capabilities of the manifest cannot be read it should report the verification incomplete with the cause",
			hooks:   capabilityHooks("manifest.json", fileio.XattrUnknown, ioErr),
			want:    firmware.ErrRetainedVerificationIncomplete,
			notWant: []error{firmware.ErrRetainedPayloadChanged, firmware.ErrRetainedPayloadMissing},
			cause:   unix.EIO,
		},
		{
			name:    "When the capabilities of the executable cannot be read it should report the verification incomplete with the cause",
			hooks:   capabilityHooks("payload/bin/updater", fileio.XattrUnknown, ioErr),
			want:    firmware.ErrRetainedVerificationIncomplete,
			notWant: []error{firmware.ErrRetainedPayloadChanged, firmware.ErrRetainedPayloadMissing},
			cause:   unix.EIO,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			f := newPayloadFixture(t, defaultPayload(t))
			pin := publishVersion(t, f, 'a')
			_, err := evidenceRetention(f, tc.hooks).VerifyPublished(context.Background(), pin)
			require.ErrorIs(err, tc.want)
			for _, e := range tc.notWant {
				require.NotErrorIs(err, e)
			}
			if tc.cause != nil {
				require.ErrorIs(err, tc.cause)
			}
			requireFetchError(t, err, firmware.ReasonUpdaterUnavailable, false)
			_, statErr := os.Stat(f.cacheDir(pin.PayloadDir, "manifest.json"))
			require.NoError(statErr, "verification never removes the retained payload")
		})
	}
	t.Run("When the filesystem does not support capability attributes it should verify the payload", func(t *testing.T) {
		f := newPayloadFixture(t, defaultPayload(t))
		pin := publishVersion(t, f, 'a')
		unsupported := &hooks{xattr: func(_, attr string) ([]byte, fileio.XattrState, error, bool) {
			if attr != "security.capability" {
				return nil, "", nil, false
			}
			return nil, fileio.XattrUnsupported, nil, true
		}}
		_, err := evidenceRetention(f, unsupported).VerifyPublished(context.Background(), pin)
		require.NoError(t, err)
	})
	t.Run("When the pinned deadline expires during the capability scan it should stop reading and report incomplete verification", func(t *testing.T) {
		require := require.New(t)
		f := newPayloadFixture(t, defaultPayload(t))
		pin := publishVersion(t, f, 'a')
		pin.Limits.MaxExtractionSeconds = 1
		reads := 0
		expiring := &hooks{xattr: func(_, attr string) ([]byte, fileio.XattrState, error, bool) {
			if attr != "security.capability" {
				return nil, "", nil, false
			}
			reads++
			if reads == 1 {
				time.Sleep(1100 * time.Millisecond)
			}
			return nil, fileio.XattrAbsent, nil, true
		}}
		_, err := evidenceRetention(f, expiring).VerifyPublished(context.Background(), pin)
		require.ErrorIs(err, context.DeadlineExceeded)
		require.ErrorIs(err, firmware.ErrRetainedVerificationIncomplete)
		require.NotErrorIs(err, firmware.ErrRetainedPayloadChanged)
		require.Equal(1, reads, "the scan stops at the deadline")
	})
	t.Run("When a capability read while hashing a retained file fails it should report the verification incomplete with the cause", func(t *testing.T) {
		require := require.New(t)
		f := newPayloadFixture(t, defaultPayload(t))
		pin := publishVersion(t, f, 'a')
		late := &lateCapabilityFailure{rel: "payload/bin/updater"}
		_, err := evidenceRetention(f, &hooks{hash: late.hook}).VerifyPublished(context.Background(), pin)
		require.ErrorIs(err, firmware.ErrRetainedVerificationIncomplete)
		require.ErrorIs(err, deviceerrors.ErrFileCapabilityUnknown)
		require.ErrorIs(err, unix.EIO)
		require.NotErrorIs(err, firmware.ErrRetainedPayloadChanged)
		requireFetchError(t, err, firmware.ReasonUpdaterUnavailable, false)
		require.Equal(1, late.hashes, "the failing read was exercised once")
		_, statErr := os.Stat(f.cacheDir(pin.PayloadDir, "manifest.json"))
		require.NoError(statErr, "verification never removes the retained payload")
	})
}

// registryAPIVersion reads the API version of the registry written by a
// successful Pin.
func registryAPIVersion(t *testing.T, f *payloadFixture) string {
	t.Helper()
	data, err := os.ReadFile(registryPath(f))
	require.NoError(t, err)
	var doc struct {
		APIVersion string `json:"apiVersion"`
	}
	require.NoError(t, json.Unmarshal(data, &doc))
	return doc.APIVersion
}

func encodedRegistry(t *testing.T, version string, pins []firmware.Pin) []byte {
	t.Helper()
	data, err := json.Marshal(struct {
		APIVersion string         `json:"apiVersion"`
		Pins       []firmware.Pin `json:"pins"`
	}{version, pins})
	require.NoError(t, err)
	return data
}

func TestRetentionRegistrySizeBound(t *testing.T) {
	const readLimit = 1 << 20
	testCases := []struct {
		name    string
		size    int
		wantErr bool
	}{
		{name: "When the updated registry stays below the read limit it should persist the pin", size: readLimit - 1},
		{name: "When the updated registry is exactly the read limit it should persist the pin", size: readLimit},
		{name: "When the updated registry would exceed the read limit it should refuse the pin and keep the registry", size: readLimit + 1, wantErr: true},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			f := newPayloadFixture(t, defaultPayload(t))
			pin := publishVersion(t, f, 'a')
			require.NoError(newRetention(f).Pin(pin))
			version := registryAPIVersion(t, f)
			next := pin
			next.AttemptID = "next"
			sizeWith := func(fill []firmware.Pin) int {
				return len(encodedRegistry(t, version, append(append(append([]firmware.Pin{}, fill...), pin), next)))
			}
			filler := func(i int) firmware.Pin {
				p := pin
				p.AttemptID = fmt.Sprintf("fill-%07d", i)
				return p
			}
			baseSize := sizeWith(nil)
			perPin := sizeWith([]firmware.Pin{filler(0)}) - baseSize
			// The filler IDs have a fixed encoded width, so size them once.
			fill := make([]firmware.Pin, (tc.size-baseSize)/perPin)
			for i := range fill {
				fill[i] = filler(i)
			}
			gap := tc.size - baseSize - len(fill)*perPin
			for i := 0; gap > 0; i++ {
				k := min(gap, 200)
				fill[i].AttemptID += strings.Repeat("x", k)
				gap -= k
			}
			require.Equal(tc.size, sizeWith(fill))
			prior := encodedRegistry(t, version, append(append([]firmware.Pin{}, fill...), pin))
			require.NoError(os.WriteFile(registryPath(f), prior, 0o600))

			err := newRetention(f).Pin(next)
			after, readErr := os.ReadFile(registryPath(f))
			require.NoError(readErr)
			if tc.wantErr {
				require.Error(err)
				require.Equal(prior, after, "a refused update keeps the prior registry")
				pins, err := newRetention(f).Pins()
				require.NoError(err, "the prior registry stays readable")
				require.Len(pins, len(fill)+1)
				return
			}
			require.NoError(err)
			require.Len(after, tc.size)
			pins, err := newRetention(f).Pins()
			require.NoError(err)
			require.Len(pins, len(fill)+2)
		})
	}
}

func TestRetentionNoOpRetriesAreDurable(t *testing.T) {
	failFirmwareSync := func(synced *int, err error) *hooks {
		return &hooks{sync: func(dir string) error {
			if strings.HasSuffix(dir, "/firmware") {
				*synced++
				return err
			}
			return nil
		}}
	}
	t.Run("When the registry cannot be synced cleanup should leave the payload intact", func(t *testing.T) {
		f := newPayloadFixture(t, defaultPayload(t))
		pin := publishVersion(t, f, 'a')
		synced := 0
		r := evidenceRetention(f, failFirmwareSync(&synced, unix.EIO))
		removed, err := r.CleanupUnreferenced(context.Background())
		require.ErrorIs(t, err, unix.EIO)
		require.Empty(t, removed)
		require.DirExists(t, filepath.Join(f.cacheDir(), pin.PayloadDir))
		require.Equal(t, 1, synced)
	})

	t.Run("When an identical pin is retried it should sync the registry before reporting success", func(t *testing.T) {
		require := require.New(t)
		f := newPayloadFixture(t, defaultPayload(t))
		pin := publishVersion(t, f, 'a')
		require.NoError(newRetention(f).Pin(pin))
		synced := 0
		require.NoError(evidenceRetention(f, failFirmwareSync(&synced, nil)).Pin(pin))
		require.Equal(1, synced)
	})
	t.Run("When syncing the registry fails on an identical pin retry it should report the failure", func(t *testing.T) {
		require := require.New(t)
		f := newPayloadFixture(t, defaultPayload(t))
		pin := publishVersion(t, f, 'a')
		require.NoError(newRetention(f).Pin(pin))
		synced := 0
		err := evidenceRetention(f, failFirmwareSync(&synced, unix.EIO)).Pin(pin)
		require.ErrorIs(err, unix.EIO)
		require.Equal(1, synced)
	})
	t.Run("When a release finds nothing to remove it should sync the registry before reporting success", func(t *testing.T) {
		require := require.New(t)
		f := newPayloadFixture(t, defaultPayload(t))
		pin := publishVersion(t, f, 'a')
		require.NoError(newRetention(f).Pin(pin))
		synced := 0
		require.NoError(evidenceRetention(f, failFirmwareSync(&synced, nil)).Release("other-attempt"))
		require.Equal(1, synced)
	})
	t.Run("When syncing the registry fails on a release that finds nothing to remove it should report the failure", func(t *testing.T) {
		require := require.New(t)
		f := newPayloadFixture(t, defaultPayload(t))
		pin := publishVersion(t, f, 'a')
		require.NoError(newRetention(f).Pin(pin))
		synced := 0
		err := evidenceRetention(f, failFirmwareSync(&synced, unix.EIO)).Release("other-attempt")
		require.ErrorIs(err, unix.EIO)
	})
}
