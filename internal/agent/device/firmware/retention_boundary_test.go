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

	deviceerrors "github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/firmware"
	"github.com/stretchr/testify/require"
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
