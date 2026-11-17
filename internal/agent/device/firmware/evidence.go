package firmware

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/flightctl/flightctl/internal/agent/device/fileio"
)

const (
	// MaxLabelBytes bounds a recorded SELinux label.
	MaxLabelBytes = 1024
	// MaxEvidenceBytes bounds a host evidence record.
	MaxEvidenceBytes = 16 << 10

	evidenceAPIVersion    = "updater-host-evidence/v1"
	evidenceDirName       = "evidence"
	maxEvidenceErrorBytes = 256
	selinuxEnforcePath    = "sys/fs/selinux/enforce"
	bootIDPath            = "proc/sys/kernel/random/boot_id"
	selinuxLabelXattr     = "security.selinux"
	// sysfs reports a page as the size of its attribute files.
	hostFileReadLimit = 4096
	maxModeBytes      = 16

	evidenceEventVerify = "verify"
)

// SELinuxMode is the SELinux mode observed on the host.
type SELinuxMode string

const (
	SELinuxEnforcing  SELinuxMode = "enforcing"
	SELinuxPermissive SELinuxMode = "permissive"
	SELinuxDisabled   SELinuxMode = "disabled"
	SELinuxUnknown    SELinuxMode = "unknown"
)

// LabelObservation is the SELinux label observed on one published entry.
type LabelObservation struct {
	// Path is "." for the version directory or the manifest entrypoint.
	Path  string            `json:"path"`
	State fileio.XattrState `json:"state"`
	// Value is the exact label when State is present.
	Value string `json:"value,omitempty"`
	// Error is a sanitized description of a failed read, at most 256 bytes.
	Error string `json:"error,omitempty"`
}

// HostObservation records the host SELinux state seen for a published
// payload. It is evidence only: it neither admits nor denies execution.
type HostObservation struct {
	ObservedAt time.Time `json:"observedAt"`
	BootID     string    `json:"bootID"`
	// Event is publish, reuse or verify.
	Event                    string              `json:"event"`
	RecordedAfterPublication bool                `json:"recordedAfterPublication"`
	Mode                     SELinuxMode         `json:"mode"`
	ModeError                string              `json:"modeError,omitempty"`
	Labels                   [2]LabelObservation `json:"labels"`
}

// HostEvidence is the durable record of the first and the latest host
// observation of a published payload.
type HostEvidence struct {
	APIVersion                 string          `json:"apiVersion"`
	PackageDigest              string          `json:"packageDigest"`
	ManifestDigest             string          `json:"manifestDigest"`
	DescriptorDigest           string          `json:"descriptorDigest"`
	PayloadDir                 string          `json:"payloadDir"`
	Entrypoint                 string          `json:"entrypoint"`
	Published                  HostObservation `json:"published"`
	Latest                     HostObservation `json:"latest"`
	ObservationsSincePublished int64           `json:"observationsSincePublished"`
	LatestDiffersFromPublished bool            `json:"latestDiffersFromPublished"`
	DiscardedReason            string          `json:"discardedReason,omitempty"`
}

type evidenceIdentity struct {
	packageDigest    string
	manifestDigest   string
	descriptorDigest string
	payloadDir       string
	entrypoint       string
}

func (id evidenceIdentity) matches(e *HostEvidence) bool {
	return e.PackageDigest == id.packageDigest && e.ManifestDigest == id.manifestDigest &&
		e.DescriptorDigest == id.descriptorDigest && e.PayloadDir == id.payloadDir && e.Entrypoint == id.entrypoint
}

// boundedText keeps printable runes and truncates to limit bytes.
func boundedText(s string, limit int) string {
	var b strings.Builder
	for _, r := range s {
		if !unicode.IsPrint(r) {
			r = '?'
		}
		if b.Len()+utf8.RuneLen(r) > limit {
			break
		}
		b.WriteRune(r)
	}
	return b.String()
}

// observeHost reads the SELinux mode, the boot ID and the labels of the
// version directory and its entrypoint.
func observeHost(rw fileio.ReadWriter, final fileio.Anchor, entrypoint, event string, now time.Time) HostObservation {
	obs := HostObservation{ObservedAt: now.UTC(), Event: event}
	obs.Mode, obs.ModeError = readSELinuxMode(rw)
	if data, err := rw.ReadFileInRoot("/", bootIDPath, hostFileReadLimit); err == nil {
		obs.BootID = boundedText(strings.TrimSpace(string(data)), maxEvidenceErrorBytes)
	}
	obs.Labels[0] = observeLabel(final, ".")
	obs.Labels[1] = observeLabel(final, entrypoint)
	return obs
}

func readSELinuxMode(rw fileio.ReadWriter) (SELinuxMode, string) {
	data, err := rw.ReadFileInRoot("/", selinuxEnforcePath, hostFileReadLimit)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return SELinuxDisabled, ""
		}
		return SELinuxUnknown, boundedText(err.Error(), maxEvidenceErrorBytes)
	}
	if len(data) > maxModeBytes {
		return SELinuxUnknown, fmt.Sprintf("SELinux mode file holds %d bytes", len(data))
	}
	switch value := strings.TrimSpace(string(data)); value {
	case "0":
		return SELinuxPermissive, ""
	case "1":
		return SELinuxEnforcing, ""
	default:
		return SELinuxUnknown, boundedText(fmt.Sprintf("unexpected SELinux mode value %q", value), maxEvidenceErrorBytes)
	}
}

func observeLabel(final fileio.Anchor, rel string) LabelObservation {
	value, state, err := final.GetXattr(rel, selinuxLabelXattr, MaxLabelBytes)
	l := LabelObservation{Path: rel, State: state}
	if state == fileio.XattrPresent {
		l.Value = strings.TrimRight(string(value), "\x00")
	}
	if err != nil {
		l.State = fileio.XattrUnknown
		l.Value = ""
		l.Error = boundedText(err.Error(), maxEvidenceErrorBytes)
	}
	return l
}

func observationsDiffer(a, b HostObservation) bool {
	if a.Mode != b.Mode {
		return true
	}
	for i := range a.Labels {
		if a.Labels[i].Path != b.Labels[i].Path || a.Labels[i].State != b.Labels[i].State || a.Labels[i].Value != b.Labels[i].Value {
			return true
		}
	}
	return false
}

func evidenceFileName(payloadDir string) string { return payloadDir + ".json" }

// decodeEvidence strictly decodes a record and returns why it is unusable.
func decodeEvidence(data []byte) (*HostEvidence, string) {
	var e HostEvidence
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&e); err != nil {
		return nil, "malformed record"
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, "malformed record"
	}
	if e.APIVersion != evidenceAPIVersion {
		return nil, "unsupported record version"
	}
	return &e, ""
}

// readEvidence returns the valid record of id, or the reason an existing
// record cannot be used.
func readEvidence(dir fileio.Anchor, id evidenceIdentity) (*HostEvidence, string) {
	data, err := dir.ReadFile(evidenceFileName(id.payloadDir), MaxEvidenceBytes)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, ""
		}
		return nil, boundedText("unreadable record: "+err.Error(), maxEvidenceErrorBytes)
	}
	e, reason := decodeEvidence(data)
	if e == nil {
		return nil, reason
	}
	if !id.matches(e) {
		return nil, "record identity differs"
	}
	return e, ""
}

// recordEvidence persists obs for the published payload id. A fresh
// publication starts a new record; otherwise obs updates the latest slot of a
// valid record, or starts a record marked as recorded after publication.
func recordEvidence(dir fileio.Anchor, id evidenceIdentity, obs HostObservation, fresh bool) (HostObservation, error) {
	existing, reason := readEvidence(dir, id)
	var rec HostEvidence
	if !fresh && existing != nil {
		rec = *existing
		rec.Latest = obs
		if rec.ObservationsSincePublished < math.MaxInt64 {
			rec.ObservationsSincePublished++
		}
		rec.LatestDiffersFromPublished = observationsDiffer(rec.Published, obs)
	} else {
		obs.RecordedAfterPublication = !fresh
		rec = HostEvidence{
			APIVersion:       evidenceAPIVersion,
			PackageDigest:    id.packageDigest,
			ManifestDigest:   id.manifestDigest,
			DescriptorDigest: id.descriptorDigest,
			PayloadDir:       id.payloadDir,
			Entrypoint:       id.entrypoint,
			Published:        obs,
			Latest:           obs,
			DiscardedReason:  reason,
		}
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return obs, fmt.Errorf("%w: %w", ErrEvidenceNotPersisted, err)
	}
	if len(data) > MaxEvidenceBytes {
		return obs, fmt.Errorf("%w: record of %d bytes exceeds %d bytes", ErrEvidenceNotPersisted, len(data), MaxEvidenceBytes)
	}
	if err := dir.ReplaceFileDurable(evidenceFileName(id.payloadDir), data, 0o600); err != nil {
		return obs, fmt.Errorf("%w: %w", ErrEvidenceNotPersisted, err)
	}
	return obs, nil
}
