package firmware_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/agent/device/firmware"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func (a hooked) GetXattr(rel, attr string, limit int) ([]byte, fileio.XattrState, error) {
	if a.h.xattr != nil {
		if v, state, err, ok := a.h.xattr(rel, attr); ok {
			return v, state, err
		}
	}
	return a.Anchor.GetXattr(rel, attr, limit)
}

func (a hooked) ReplaceFileDurable(name string, data []byte, perm fs.FileMode) error {
	if a.h.replace != nil {
		if err := a.h.replace(a.Path(), name); err != nil {
			return err
		}
	}
	return a.Anchor.ReplaceFileDurable(name, data, perm)
}

// hookedRW opens every anchor through the hooks.
type hookedRW struct {
	fileio.ReadWriter
	h *hooks
}

func (rw hookedRW) OpenAnchor(path string, opts fileio.DirOptions) (fileio.Anchor, error) {
	a, err := rw.ReadWriter.OpenAnchor(path, opts)
	if err != nil {
		return nil, err
	}
	return hooked{Anchor: a, h: rw.h}, nil
}

func setHostFile(t *testing.T, f *payloadFixture, rel, content string) {
	t.Helper()
	p := filepath.Join(f.root, rel)
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
	require.NoError(t, os.WriteFile(p, []byte(content), 0o600))
}

func setMode(t *testing.T, f *payloadFixture, mode string) {
	t.Helper()
	if mode == "" {
		require.NoError(t, os.RemoveAll(filepath.Join(f.root, "sys")))
		return
	}
	setHostFile(t, f, "sys/fs/selinux/enforce", mode)
}

func labelHooks(root, entry fileio.XattrState, value string) *hooks {
	return &hooks{xattr: func(rel, attr string) ([]byte, fileio.XattrState, error, bool) {
		if attr != "security.selinux" {
			return nil, "", nil, false
		}
		state := entry
		if rel == "." {
			state = root
		}
		switch state {
		case fileio.XattrPresent:
			return []byte(value + "\x00"), state, nil, true
		case fileio.XattrUnknown:
			return nil, state, fmt.Errorf("read label: %w", unix.EACCES), true
		default:
			return nil, state, nil, true
		}
	}}
}

func evidenceRetention(f *payloadFixture, h *hooks) *firmware.Retention {
	var rw fileio.ReadWriter = f.rw
	if h != nil {
		rw = hookedRW{ReadWriter: f.rw, h: h}
	}
	return firmware.NewRetention(cacheData, rw, log.NewPrefixLogger("evidence-test"))
}

func evidencePath(f *payloadFixture, pin firmware.Pin) string {
	return filepath.Join(f.root, "data", "firmware", "evidence", pin.PayloadDir+".json")
}

func TestEvidenceRecordsTheObservedHostState(t *testing.T) {
	modes := []struct {
		name    string
		content string
		want    firmware.SELinuxMode
		wantErr bool
	}{
		{name: "When SELinux is enforcing it should record enforcing and still verify", content: "1", want: firmware.SELinuxEnforcing},
		{name: "When SELinux is permissive it should record permissive and still verify", content: "0\n", want: firmware.SELinuxPermissive},
		{name: "When SELinux is disabled it should record disabled and still verify", content: "", want: firmware.SELinuxDisabled},
		{name: "When the mode file holds an unexpected value it should record unknown with an error", content: "2", want: firmware.SELinuxUnknown, wantErr: true},
		{name: "When the mode file is oversized it should record unknown with an error", content: strings.Repeat("1", 40), want: firmware.SELinuxUnknown, wantErr: true},
	}
	for _, tc := range modes {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			f := newPayloadFixture(t, defaultPayload(t))
			pin := publishVersion(t, f, 'a')
			setMode(t, f, tc.content)
			setHostFile(t, f, "proc/sys/kernel/random/boot_id", "boot-1\n")
			r := evidenceRetention(f, labelHooks(fileio.XattrPresent, fileio.XattrPresent, "system_u:object_r:var_lib_t:s0"))
			res, err := r.VerifyPublished(context.Background(), pin)
			require.NoError(err)
			require.True(res.EvidencePersisted)
			require.NoError(res.EvidenceErr)
			obs := res.Observation
			require.Equal(tc.want, obs.Mode)
			require.Equal(tc.wantErr, obs.ModeError != "")
			require.Equal("boot-1", obs.BootID)
			require.Equal("verify", obs.Event)
			require.False(obs.ObservedAt.IsZero())
			require.True(obs.RecordedAfterPublication, "verification records evidence after publication")
			require.Equal(firmware.LabelObservation{Path: ".", State: fileio.XattrPresent, Value: "system_u:object_r:var_lib_t:s0"}, obs.Labels[0])
			require.Equal("payload/bin/updater", obs.Labels[1].Path)

			ev, err := r.Evidence(pin.PackageDigest)
			require.NoError(err)
			require.Equal("updater-host-evidence/v1", ev.APIVersion)
			require.Equal(pin.PackageDigest, ev.PackageDigest)
			require.Equal(pin.ManifestDigest, ev.ManifestDigest)
			require.Equal(pin.DescriptorDigest, ev.DescriptorDigest)
			require.Equal(pin.PayloadDir, ev.PayloadDir)
			require.Equal("payload/bin/updater", ev.Entrypoint)
			require.Equal(ev.Published, ev.Latest)
			require.Zero(ev.ObservationsSincePublished)
			info, err := os.Stat(evidencePath(f, pin))
			require.NoError(err)
			require.Equal(os.FileMode(0o600), info.Mode().Perm())
			require.LessOrEqual(info.Size(), int64(firmware.MaxEvidenceBytes))
		})
	}
}

func TestEvidenceLabelStates(t *testing.T) {
	testCases := []struct {
		name  string
		state fileio.XattrState
		err   bool
	}{
		{name: "When labels are absent it should record them absent", state: fileio.XattrAbsent},
		{name: "When labels are unsupported it should record them unsupported", state: fileio.XattrUnsupported},
		{name: "When reading labels is denied it should record them unknown with an error", state: fileio.XattrUnknown, err: true},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			f := newPayloadFixture(t, defaultPayload(t))
			pin := publishVersion(t, f, 'a')
			res, err := evidenceRetention(f, labelHooks(tc.state, tc.state, "")).VerifyPublished(context.Background(), pin)
			require.NoError(err)
			for _, l := range res.Observation.Labels {
				require.Equal(tc.state, l.State)
				require.Empty(l.Value)
				require.Equal(tc.err, l.Error != "")
				require.LessOrEqual(len(l.Error), 256)
			}
		})
	}
	t.Run("When a label is longer than the bound it should record it unknown", func(t *testing.T) {
		require := require.New(t)
		f := newPayloadFixture(t, defaultPayload(t))
		pin := publishVersion(t, f, 'a')
		h := &hooks{xattr: func(rel, attr string) ([]byte, fileio.XattrState, error, bool) {
			return nil, "", nil, false
		}}
		setMode(t, f, "1")
		long := strings.Repeat("l", firmware.MaxLabelBytes+10)
		dir := f.cacheDir(pin.PayloadDir)
		if err := unix.Setxattr(dir, "user.probe", []byte("x"), 0); err != nil {
			t.Skipf("filesystem does not support extended attributes: %v", err)
		}
		h.xattr = func(rel, attr string) ([]byte, fileio.XattrState, error, bool) {
			if attr != "security.selinux" || rel != "." {
				return nil, "", nil, false
			}
			return nil, fileio.XattrUnknown, fmt.Errorf("attribute longer than %d bytes", len(long)-10), true
		}
		res, err := evidenceRetention(f, h).VerifyPublished(context.Background(), pin)
		require.NoError(err)
		require.Equal(fileio.XattrUnknown, res.Observation.Labels[0].State)
		require.NotEmpty(res.Observation.Labels[0].Error)
	})
}

func TestEvidenceAcrossObservations(t *testing.T) {
	require := require.New(t)
	f := newPayloadFixture(t, defaultPayload(t))
	pin := publishVersion(t, f, 'a')
	setMode(t, f, "1")
	setHostFile(t, f, "proc/sys/kernel/random/boot_id", "boot-1")
	label := "system_u:object_r:var_lib_t:s0"
	first, err := evidenceRetention(f, labelHooks(fileio.XattrPresent, fileio.XattrPresent, label)).VerifyPublished(context.Background(), pin)
	require.NoError(err)

	steps := []struct {
		mode  string
		state fileio.XattrState
		label string
	}{
		{mode: "0", state: fileio.XattrPresent, label: label},
		{mode: "", state: fileio.XattrAbsent},
		{mode: "1", state: fileio.XattrPresent, label: "system_u:object_r:other_t:s0"},
		{mode: "1", state: fileio.XattrPresent, label: label},
	}
	for i, step := range steps {
		setMode(t, f, step.mode)
		setHostFile(t, f, "proc/sys/kernel/random/boot_id", fmt.Sprintf("boot-%d", i+2))
		r := evidenceRetention(f, labelHooks(step.state, step.state, step.label))
		res, err := r.VerifyPublished(context.Background(), pin)
		require.NoError(err, "mode and label changes never fail verification")
		require.True(res.EvidencePersisted)
		ev, err := r.Evidence(pin.PackageDigest)
		require.NoError(err)
		require.Equal(first.Observation, ev.Published, "the first observation is kept")
		require.Equal(res.Observation, ev.Latest)
		require.Equal(fmt.Sprintf("boot-%d", i+2), ev.Latest.BootID)
		require.EqualValues(i+1, ev.ObservationsSincePublished)
		require.Equal(i != len(steps)-1, ev.LatestDiffersFromPublished)
	}
}

func TestEvidencePersistenceFailure(t *testing.T) {
	require := require.New(t)
	f := newPayloadFixture(t, defaultPayload(t))
	pin := publishVersion(t, f, 'a')
	h := &hooks{replace: func(dir, name string) error {
		if strings.HasSuffix(dir, "evidence") {
			return fmt.Errorf("injected: %w", unix.EIO)
		}
		return nil
	}}
	res, err := evidenceRetention(f, h).VerifyPublished(context.Background(), pin)
	require.NoError(err, "the byte verdict does not depend on evidence persistence")
	require.False(res.EvidencePersisted)
	require.ErrorIs(res.EvidenceErr, firmware.ErrEvidenceNotPersisted)
	_, err = os.Stat(f.cacheDir(pin.PayloadDir))
	require.NoError(err, "nothing is deleted")

	res, err = evidenceRetention(f, nil).VerifyPublished(context.Background(), pin)
	require.NoError(err)
	require.True(res.EvidencePersisted)
	require.True(res.Observation.RecordedAfterPublication)
}

func TestEvidenceRewritesInvalidRecords(t *testing.T) {
	testCases := []struct {
		name    string
		content func(pin firmware.Pin) []byte
	}{
		{name: "When the record is malformed it should rewrite it with a discarded reason", content: func(firmware.Pin) []byte { return []byte("{") }},
		{name: "When the record names another package it should rewrite it with a discarded reason", content: func(pin firmware.Pin) []byte {
			data, _ := json.Marshal(map[string]any{"apiVersion": "updater-host-evidence/v1", "packageDigest": "sha256:" + strings.Repeat("0", 64)})
			return data
		}},
		{name: "When the record is oversized it should rewrite it with a discarded reason", content: func(firmware.Pin) []byte { return []byte(strings.Repeat(" ", 20<<10)) }},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			f := newPayloadFixture(t, defaultPayload(t))
			pin := publishVersion(t, f, 'a')
			require.NoError(os.MkdirAll(filepath.Dir(evidencePath(f, pin)), 0o700))
			require.NoError(os.WriteFile(evidencePath(f, pin), tc.content(pin), 0o600))
			r := evidenceRetention(f, nil)
			res, err := r.VerifyPublished(context.Background(), pin)
			require.NoError(err)
			require.True(res.EvidencePersisted)
			ev, err := r.Evidence(pin.PackageDigest)
			require.NoError(err)
			require.NotEmpty(ev.DiscardedReason)
			require.Equal(pin.PackageDigest, ev.PackageDigest)
			require.True(ev.Published.RecordedAfterPublication)
		})
	}
}

func TestEvidenceAccessAndCleanup(t *testing.T) {
	require := require.New(t)
	f := newPayloadFixture(t, defaultPayload(t))
	kept := publishVersion(t, f, 'a')
	orphan := publishVersion(t, f, 'b')
	r := evidenceRetention(f, nil)
	_, err := r.Evidence(kept.PackageDigest)
	require.ErrorIs(err, fs.ErrNotExist)
	require.NoError(r.Pin(kept))
	for _, pin := range []firmware.Pin{kept, orphan} {
		_, err := r.VerifyPublished(context.Background(), pin)
		require.NoError(err)
	}
	ev, err := r.Evidence(kept.PackageDigest)
	require.NoError(err)
	ev.DiscardedReason = "edited copy"
	again, err := r.Evidence(kept.PackageDigest)
	require.NoError(err)
	require.Empty(again.DiscardedReason, "Evidence returns a copy")
	require.NoError(os.WriteFile(filepath.Join(filepath.Dir(evidencePath(f, kept)), "notes.txt"), []byte("x"), 0o600))

	removed, err := r.CleanupUnreferenced(context.Background())
	require.NoError(err)
	require.Contains(removed, orphan.PayloadDir)
	require.Contains(removed, orphan.PayloadDir+".json")
	_, err = os.Stat(evidencePath(f, orphan))
	require.True(os.IsNotExist(err))
	_, err = os.Stat(evidencePath(f, kept))
	require.NoError(err)
	_, err = os.Stat(filepath.Join(filepath.Dir(evidencePath(f, kept)), "notes.txt"))
	require.NoError(err, "foreign files are never removed")
}
