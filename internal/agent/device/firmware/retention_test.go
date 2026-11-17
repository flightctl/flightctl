package firmware_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/agent/device/firmware"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/stretchr/testify/require"
)

func digestFor(c byte) (string, string) {
	hex := strings.Repeat(string(c), 64)
	return "sha256:" + hex, hex
}

func newRetention(f *payloadFixture, opts ...firmware.RetentionOption) *firmware.Retention {
	return firmware.NewRetention(cacheData, f.rw, log.NewPrefixLogger("retention-test"), opts...)
}

// publishVersion stages and publishes the fixture payload as the version
// directory of the package digest made of c.
func publishVersion(t *testing.T, f *payloadFixture, c byte) firmware.Pin {
	t.Helper()
	digest, hex := digestFor(c)
	st, err := f.stage(context.Background(), f.src(), f.cache(), testLimits())
	require.NoError(t, err)
	_, err = st.Publish(context.Background(), hex)
	require.NoError(t, err)
	return firmware.Pin{
		AttemptID:        "attempt-" + string(c),
		Role:             firmware.PinRoleActive,
		Reference:        "quay.io/example/updater@" + digest,
		PackageDigest:    digest,
		ManifestDigest:   "sha256:" + sha256Hex(f.manifestBytes),
		DescriptorDigest: "sha256:" + strings.Repeat("f", 64),
		PayloadDir:       hex,
		Limits:           testLimits(),
	}
}

func registryPath(f *payloadFixture) string {
	return filepath.Join(f.root, "data", "firmware", "retained.json")
}

func TestRetentionPins(t *testing.T) {
	t.Run("When a published version is pinned it should persist across restarts and release", func(t *testing.T) {
		require := require.New(t)
		f := newPayloadFixture(t, defaultPayload(t))
		pin := publishVersion(t, f, 'a')
		r := newRetention(f)
		require.NoError(r.Pin(pin))
		require.NoError(r.Pin(pin), "pinning the same attempt again is idempotent")
		info, err := os.Stat(registryPath(f))
		require.NoError(err)
		require.Equal(os.FileMode(0o600), info.Mode().Perm())

		restarted := newRetention(f)
		pins, err := restarted.Pins()
		require.NoError(err)
		require.Equal([]firmware.Pin{pin}, pins)
		got, ok, err := restarted.PinFor(pin.PackageDigest)
		require.NoError(err)
		require.True(ok)
		require.Equal(pin, *got)
		refs, err := restarted.RetainedImageReferences(context.Background())
		require.NoError(err)
		require.Equal([]string{pin.Reference}, refs)

		require.NoError(restarted.Release(pin.AttemptID))
		require.NoError(restarted.Release(pin.AttemptID), "releasing twice is idempotent")
		_, ok, err = newRetention(f).PinFor(pin.PackageDigest)
		require.NoError(err)
		require.False(ok)
	})
	t.Run("When an attempt pins a recovery version too it should protect both", func(t *testing.T) {
		require := require.New(t)
		f := newPayloadFixture(t, defaultPayload(t))
		active := publishVersion(t, f, 'a')
		recovery := publishVersion(t, f, 'b')
		recovery.AttemptID = active.AttemptID
		recovery.Role = firmware.PinRoleRecovery
		r := newRetention(f)
		require.NoError(r.Pin(active))
		require.NoError(r.Pin(recovery))
		refs, err := r.RetainedImageReferences(context.Background())
		require.NoError(err)
		sort.Strings(refs)
		require.Equal([]string{active.Reference, recovery.Reference}, refs)
		require.NoError(r.Release(active.AttemptID))
		pins, err := r.Pins()
		require.NoError(err)
		require.Empty(pins)
	})
	invalid := []struct {
		name   string
		mutate func(p *firmware.Pin)
		want   error
	}{
		{name: "When the payload directory is not the package digest it should refuse the pin", mutate: func(p *firmware.Pin) { p.PayloadDir = strings.Repeat("b", 64) }},
		{name: "When the version directory is missing it should refuse the pin", mutate: func(p *firmware.Pin) {
			digest, hex := digestFor('9')
			p.PackageDigest, p.PayloadDir, p.Reference = digest, hex, "quay.io/example/updater@"+digest
		}, want: firmware.ErrRetainedPayloadMissing},
		{name: "When the reference is not pinned to the package digest it should refuse the pin", mutate: func(p *firmware.Pin) { p.Reference = "quay.io/example/updater:latest" }},
		{name: "When the role is unknown it should refuse the pin", mutate: func(p *firmware.Pin) { p.Role = "spare" }},
		{name: "When the attempt is empty it should refuse the pin", mutate: func(p *firmware.Pin) { p.AttemptID = "" }},
		{name: "When the limits are invalid it should refuse the pin", mutate: func(p *firmware.Pin) { p.Limits.MaxExtractedFiles = 0 }},
	}
	for _, tc := range invalid {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			f := newPayloadFixture(t, defaultPayload(t))
			pin := publishVersion(t, f, 'a')
			tc.mutate(&pin)
			err := newRetention(f).Pin(pin)
			require.Error(err)
			if tc.want != nil {
				require.ErrorIs(err, tc.want)
			}
			_, statErr := os.Stat(registryPath(f))
			require.True(os.IsNotExist(statErr))
		})
	}
	t.Run("When another attempt pins the same digest with a different identity it should report a conflict", func(t *testing.T) {
		require := require.New(t)
		f := newPayloadFixture(t, defaultPayload(t))
		pin := publishVersion(t, f, 'a')
		r := newRetention(f)
		require.NoError(r.Pin(pin))
		other := pin
		other.AttemptID = "attempt-other"
		other.ManifestDigest = "sha256:" + strings.Repeat("0", 64)
		require.ErrorIs(r.Pin(other), firmware.ErrPinConflict)
		changed := pin
		changed.DescriptorDigest = "sha256:" + strings.Repeat("1", 64)
		require.ErrorIs(r.Pin(changed), firmware.ErrPinConflict)
	})
	t.Run("When the registry is malformed every operation should fail closed", func(t *testing.T) {
		require := require.New(t)
		f := newPayloadFixture(t, defaultPayload(t))
		pin := publishVersion(t, f, 'a')
		require.NoError(os.MkdirAll(filepath.Dir(registryPath(f)), 0o700))
		for _, content := range []string{"{", `{"apiVersion":"updater-retention/v9","pins":[]}`, `{"apiVersion":"updater-retention/v1","pins":[],"extra":1}`, strings.Repeat(" ", 2<<20)} {
			require.NoError(os.WriteFile(registryPath(f), []byte(content), 0o600))
			r := newRetention(f)
			_, err := r.Pins()
			require.Error(err)
			_, _, err = r.PinFor(pin.PackageDigest)
			require.Error(err)
			_, err = r.RetainedImageReferences(context.Background())
			require.Error(err)
			require.Error(r.Pin(pin))
			_, err = r.CleanupUnreferenced(context.Background())
			require.Error(err)
			_, statErr := os.Stat(f.cacheDir(pin.PayloadDir))
			require.NoError(statErr, "a malformed registry never leads to deletion")
		}
	})
	t.Run("When no firmware state exists the retention stays passive", func(t *testing.T) {
		require := require.New(t)
		root := t.TempDir()
		require.NoError(os.MkdirAll(filepath.Join(root, "data"), 0o700))
		rw := fileio.NewReadWriter(fileio.NewReader(fileio.WithReaderRootDir(root)), fileio.NewWriter(fileio.WithWriterRootDir(root)))
		r := firmware.NewRetention(cacheData, rw, log.NewPrefixLogger("retention-test"))
		refs, err := r.RetainedImageReferences(context.Background())
		require.NoError(err)
		require.Empty(refs)
		removed, err := r.CleanupUnreferenced(context.Background())
		require.NoError(err)
		require.Empty(removed)
		entries, err := os.ReadDir(filepath.Join(root, "data"))
		require.NoError(err)
		require.Empty(entries, "passive retention creates nothing")
	})
}

func TestRetentionVerifyPublished(t *testing.T) {
	t.Run("When the retained payload is intact it should verify", func(t *testing.T) {
		require := require.New(t)
		f := newPayloadFixture(t, defaultPayload(t))
		pin := publishVersion(t, f, 'a')
		_, err := newRetention(f).VerifyPublished(context.Background(), pin)
		require.NoError(err)
	})
	t.Run("When the retained payload is missing it should report it missing", func(t *testing.T) {
		require := require.New(t)
		f := newPayloadFixture(t, defaultPayload(t))
		pin := publishVersion(t, f, 'a')
		require.NoError(os.RemoveAll(f.cacheDir(pin.PayloadDir)))
		_, err := newRetention(f).VerifyPublished(context.Background(), pin)
		require.ErrorIs(err, firmware.ErrRetainedPayloadMissing)
		requireFetchError(t, err, firmware.ReasonUpdaterUnavailable, false)
	})
	changes := []struct {
		name   string
		mutate func(t *testing.T, f *payloadFixture, dir string)
	}{
		{name: "When an extra file appears it should report the payload changed", mutate: func(t *testing.T, f *payloadFixture, dir string) {
			require.NoError(t, os.WriteFile(filepath.Join(dir, "payload", "extra"), []byte("x"), 0o400))
		}},
		{name: "When thousands of extra files appear it should report the payload changed", mutate: func(t *testing.T, f *payloadFixture, dir string) {
			flood := filepath.Join(dir, "payload", "flood")
			require.NoError(t, os.Mkdir(flood, 0o700))
			for i := 0; i < 20000; i++ {
				file, err := os.Create(filepath.Join(flood, strconv.Itoa(i)))
				require.NoError(t, err)
				require.NoError(t, file.Close())
			}
		}},
		{name: "When a file is swapped for a symlink it should report the payload changed", mutate: func(t *testing.T, f *payloadFixture, dir string) {
			p := filepath.Join(dir, "payload", "share", "doc.txt")
			require.NoError(t, os.Remove(p))
			require.NoError(t, os.Symlink("/etc/hostname", p))
		}},
		{name: "When a file mode changes it should report the payload changed", mutate: func(t *testing.T, f *payloadFixture, dir string) {
			require.NoError(t, os.Chmod(filepath.Join(dir, "payload", "share", "doc.txt"), 0o600))
		}},
		{name: "When a directory mode changes it should report the payload changed", mutate: func(t *testing.T, f *payloadFixture, dir string) {
			require.NoError(t, os.Chmod(filepath.Join(dir, "payload", "share"), 0o755))
		}},
		{name: "When a file gains a hard link it should report the payload changed", mutate: func(t *testing.T, f *payloadFixture, dir string) {
			require.NoError(t, os.Link(filepath.Join(dir, "payload", "share", "doc.txt"), filepath.Join(f.root, "link")))
		}},
		{name: "When file content changes it should report the payload changed", mutate: func(t *testing.T, f *payloadFixture, dir string) {
			p := filepath.Join(dir, "payload", "share", "doc.txt")
			require.NoError(t, os.Chmod(p, 0o600))
			require.NoError(t, os.WriteFile(p, []byte("DOCUMENTATION"), 0o600))
			require.NoError(t, os.Chmod(p, 0o400))
		}},
		{name: "When a file grows past its listed size it should report the payload changed", mutate: func(t *testing.T, f *payloadFixture, dir string) {
			p := filepath.Join(dir, "payload", "share", "doc.txt")
			require.NoError(t, os.Chmod(p, 0o600))
			require.NoError(t, os.WriteFile(p, []byte("documentation, longer"), 0o600))
			require.NoError(t, os.Chmod(p, 0o400))
		}},
		{name: "When the manifest changes it should report the payload changed", mutate: func(t *testing.T, f *payloadFixture, dir string) {
			p := filepath.Join(dir, "manifest.json")
			require.NoError(t, os.Chmod(p, 0o600))
			data, err := os.ReadFile(p)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(p, append(data, ' '), 0o600))
			require.NoError(t, os.Chmod(p, 0o400))
		}},
		{name: "When the version directory is replaced by a symlink it should report the payload changed", mutate: func(t *testing.T, f *payloadFixture, dir string) {
			require.NoError(t, os.Rename(dir, dir+".moved"))
			require.NoError(t, os.Symlink(dir+".moved", dir))
		}},
	}
	for _, tc := range changes {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			f := newPayloadFixture(t, defaultPayload(t))
			pin := publishVersion(t, f, 'a')
			tc.mutate(t, f, f.cacheDir(pin.PayloadDir))
			_, err := newRetention(f).VerifyPublished(context.Background(), pin)
			require.ErrorIs(err, firmware.ErrRetainedPayloadChanged)
			requireFetchError(t, err, firmware.ReasonUpdaterUnavailable, false)
		})
	}
	t.Run("When the owner of a file changes it should report the payload changed", func(t *testing.T) {
		if os.Geteuid() != 0 {
			t.Skip("changing ownership requires root")
		}
		require := require.New(t)
		f := newPayloadFixture(t, defaultPayload(t))
		pin := publishVersion(t, f, 'a')
		require.NoError(os.Lchown(f.cacheDir(pin.PayloadDir, "payload", "share", "doc.txt"), 12345, 12345))
		_, err := newRetention(f).VerifyPublished(context.Background(), pin)
		require.ErrorIs(err, firmware.ErrRetainedPayloadChanged)
	})
	t.Run("When the pinned limits exceed the agent ceilings it should verify under the ceilings", func(t *testing.T) {
		require := require.New(t)
		f := newPayloadFixture(t, defaultPayload(t))
		pin := publishVersion(t, f, 'a')
		ceilings := firmware.DefaultExtractionCeilings()
		ceilings.MaxExtractedFileBytes = 1000
		_, err := newRetention(f, firmware.WithRetentionCeilings(ceilings)).VerifyPublished(context.Background(), pin)
		require.ErrorIs(err, firmware.ErrRetainedPayloadChanged)
	})
	t.Run("When the verification deadline expires it should report the verification incomplete", func(t *testing.T) {
		require := require.New(t)
		f := newPayloadFixture(t, defaultPayload(t))
		pin := publishVersion(t, f, 'a')
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		defer cancel()
		_, err := newRetention(f).VerifyPublished(ctx, pin)
		require.ErrorIs(err, firmware.ErrRetainedVerificationIncomplete)
		requireFetchError(t, err, firmware.ReasonUpdaterUnavailable, true)
	})
	t.Run("When the caller cancels it should stop with the cancellation", func(t *testing.T) {
		require := require.New(t)
		f := newPayloadFixture(t, defaultPayload(t))
		pin := publishVersion(t, f, 'a')
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := newRetention(f).VerifyPublished(ctx, pin)
		require.ErrorIs(err, context.Canceled)
	})
}

func TestRetentionCleanup(t *testing.T) {
	t.Run("When versions and leftovers are unreferenced it should remove only owned entries", func(t *testing.T) {
		require := require.New(t)
		f := newPayloadFixture(t, defaultPayload(t))
		pinned := publishVersion(t, f, 'a')
		unpinned := publishVersion(t, f, 'b')
		r := newRetention(f)
		require.NoError(r.Pin(pinned))
		require.NoError(os.Mkdir(f.cacheDir(".staging-0123456789abcdef"), 0o700))
		require.NoError(os.WriteFile(f.cacheDir(".staging-0123456789abcdef", "f"), []byte("x"), 0o400))
		require.NoError(os.Mkdir(f.cacheDir(".quarantine-0123456789abcdef"), 0o700))
		for _, foreign := range []string{"notes", strings.Repeat("A", 64), strings.Repeat("a", 63)} {
			require.NoError(os.Mkdir(f.cacheDir(foreign), 0o700))
		}
		siblings := map[string]string{"packages": "record.json", "signature-policy": "policy.json", "attempts": "journal"}
		for dir, file := range siblings {
			require.NoError(os.MkdirAll(filepath.Join(f.root, "data", "firmware", dir), 0o700))
			require.NoError(os.WriteFile(filepath.Join(f.root, "data", "firmware", dir, file), []byte("x"), 0o600))
		}
		removed, err := r.CleanupUnreferenced(context.Background())
		require.NoError(err)
		sort.Strings(removed)
		require.Equal([]string{".quarantine-0123456789abcdef", ".staging-0123456789abcdef", unpinned.PayloadDir}, removed)
		entries := f.cacheEntries()
		sort.Strings(entries)
		require.Equal([]string{strings.Repeat("A", 64), strings.Repeat("a", 63), pinned.PayloadDir, "notes"}, entries)
		for dir, file := range siblings {
			_, statErr := os.Stat(filepath.Join(f.root, "data", "firmware", dir, file))
			require.NoError(statErr)
		}
		_, err = r.VerifyPublished(context.Background(), pinned)
		require.NoError(err)
	})
	t.Run("When more trees are removable than a pass allows it should remove four per pass", func(t *testing.T) {
		require := require.New(t)
		f := newPayloadFixture(t, defaultPayload(t))
		for _, c := range []byte("abcdef") {
			publishVersion(t, f, c)
		}
		r := newRetention(f)
		removed, err := r.CleanupUnreferenced(context.Background())
		require.NoError(err)
		require.Len(removed, firmware.CleanupTreesPerPass)
		removed, err = r.CleanupUnreferenced(context.Background())
		require.NoError(err)
		require.Len(removed, 2)
		require.Empty(f.cacheEntries())
	})
	t.Run("When the cache lists more entries than the listing cap it should stop with an error after the entries seen", func(t *testing.T) {
		require := require.New(t)
		f := newPayloadFixture(t, defaultPayload(t))
		f.cache()
		for i := 0; i < 12; i++ {
			require.NoError(os.Mkdir(f.cacheDir("foreign-"+strconv.Itoa(i)), 0o700))
		}
		r := newRetention(f, firmware.WithCleanupLimits(4, 10, firmware.CleanupPassMaxWork))
		_, err := r.CleanupUnreferenced(context.Background())
		require.Error(err)
		require.Len(f.cacheEntries(), 12, "foreign entries are never removed")
	})
	t.Run("When the pass work cap is reached it should stop with a partial outcome that the next pass continues", func(t *testing.T) {
		require := require.New(t)
		f := newPayloadFixture(t, defaultPayload(t))
		f.cache()
		for i := 0; i < 20; i++ {
			require.NoError(os.Mkdir(f.cacheDir(".staging-"+strings.Repeat("0", 14)+strconv.Itoa(10+i)), 0o700))
		}
		r := newRetention(f, firmware.WithCleanupLimits(100, firmware.MaxCacheListingEntries, 20))
		removed, err := r.CleanupUnreferenced(context.Background())
		require.Error(err)
		require.Less(len(removed), 20)
		total := len(removed)
		for i := 0; i < 20 && len(f.cacheEntries()) > 0; i++ {
			removed, _ = r.CleanupUnreferenced(context.Background())
			total += len(removed)
		}
		require.Equal(20, total)
		require.Empty(f.cacheEntries())
	})
	t.Run("When a pass is interrupted the next pass should finish", func(t *testing.T) {
		require := require.New(t)
		f := newPayloadFixture(t, defaultPayload(t))
		publishVersion(t, f, 'a')
		r := newRetention(f)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := r.CleanupUnreferenced(ctx)
		require.ErrorIs(err, context.Canceled)
		removed, err := r.CleanupUnreferenced(context.Background())
		require.NoError(err)
		require.Len(removed, 1)
		require.Empty(f.cacheEntries())
	})
}

func TestRetentionRegistryFormat(t *testing.T) {
	require := require.New(t)
	f := newPayloadFixture(t, defaultPayload(t))
	pin := publishVersion(t, f, 'a')
	require.NoError(newRetention(f).Pin(pin))
	data, err := os.ReadFile(registryPath(f))
	require.NoError(err)
	var doc map[string]any
	require.NoError(json.Unmarshal(data, &doc))
	require.Equal("updater-retention/v1", doc["apiVersion"])
	require.Len(doc["pins"], 1)
}
