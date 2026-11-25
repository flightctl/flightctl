package firmware

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io/fs"
	"time"

	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	policy "github.com/flightctl/flightctl/internal/firmwarepolicy"
)

const (
	phaseRecordPrefix   = "phase-runner-"
	phaseRecordVersion  = 2
	maxPhaseRecordBytes = 8 << 20
)

var (
	errPhasePending             = errors.New("firmware phase has an unresolved dispatch")
	errPhaseDurabilityUncertain = errors.New("firmware runner state durability is uncertain")
	errPhaseRetired             = errors.New("firmware phase attempt has been retired")
)

type phaseAttemptBinding struct {
	DescriptorDigest string `json:"descriptorDigest"`
	PackageDigest    string `json:"packageDigest"`
	ManifestDigest   string `json:"manifestDigest"`
	ConfigDigest     string `json:"configDigest"`
	IntentDigest     string `json:"intentDigest"`
}

func phaseBinding(id phaseIdentity) phaseAttemptBinding {
	return phaseAttemptBinding{id.DescriptorDigest, id.PackageDigest, id.ManifestDigest, id.ConfigDigest, id.IntentDigest}
}

type phaseRecord struct {
	Version         int                    `json:"version"`
	AttemptID       string                 `json:"attemptID"`
	Binding         phaseAttemptBinding    `json:"binding"`
	Dispatches      int64                  `json:"dispatches"`
	Pending         *phaseIdentity         `json:"pending,omitempty"`
	CheckpointRaw   []byte                 `json:"checkpoint,omitempty"`
	SelectedRaw     []byte                 `json:"selected,omitempty"`
	Checkpoint      *phaseCheckpoint       `json:"-"`
	Selected        *phasePreparedEvidence `json:"-"`
	Accepted        []byte                 `json:"accepted,omitempty"`
	PendingReceipt  *policy.Receipt        `json:"pendingReceipt,omitempty"`
	AcceptedReceipt *policy.Receipt        `json:"acceptedReceipt,omitempty"`
	Retired         bool                   `json:"-"`
}

type phaseRetiredMarker struct {
	Version   int    `json:"version"`
	AttemptID string `json:"attemptID"`
}

type phaseRecordStore struct {
	dir       fileio.Anchor
	attemptID string
}

func (record phaseRecord) checkResultCapacity(maxResultBytes int64) error {
	// JSON encodes these byte slices in base64. Reserve the encoded size of the
	// accepted result and retained subobjects before charging the dispatch.
	baseline := record
	baseline.Pending = nil
	baseline.CheckpointRaw = nil
	baseline.SelectedRaw = nil
	baseline.PendingReceipt = nil
	baseline.Accepted = nil
	baseline.AcceptedReceipt = nil
	data, err := marshalPhaseRecord(baseline)
	if err != nil {
		return err
	}
	const commitOverhead = 4096
	if maxResultBytes <= 0 || maxResultBytes > maxPhaseRecordBytes-commitOverhead {
		return errors.New("firmware phase result allowance exceeds durable runner storage")
	}
	available := maxPhaseRecordBytes - len(data) - commitOverhead
	resultBytes := phaseRecordByteFieldSize(maxResultBytes)
	selectedBytes := phaseRecordByteFieldSize(int64(len(record.SelectedRaw)))
	checkpointBytes := phaseRecordByteFieldSize(int64(len(record.CheckpointRaw)))
	required := resultBytes + selectedBytes + checkpointBytes
	if record.Pending != nil && record.Pending.Action == phasePrepare {
		// A prepare result may replace either retained subobject, or both. When
		// both occur in one response their raw encodings are bounded by that one
		// response; when only one changes, reserve the unchanged other subobject.
		required = max(required, 2*resultBytes+selectedBytes, 2*resultBytes+checkpointBytes, 2*resultBytes+8)
	}
	if available < 0 || required > int64(available) {
		return errors.New("firmware phase result allowance exceeds durable runner storage")
	}
	return nil
}

func phaseRecordByteFieldSize(size int64) int64 {
	if size <= 0 {
		return 0
	}
	return 4 * ((size + 2) / 3)
}

func (s phaseRecordStore) load() (phaseRecord, error) {
	name, err := s.fileName()
	if err != nil {
		return phaseRecord{}, err
	}
	retired, err := s.isRetired()
	if err != nil {
		return phaseRecord{}, err
	}
	if retired {
		return phaseRecord{Version: phaseRecordVersion, AttemptID: s.attemptID, Retired: true}, nil
	}
	data, err := s.dir.ReadFile(name, maxPhaseRecordBytes)
	if errors.Is(err, fs.ErrNotExist) {
		return phaseRecord{}, nil
	}
	if err != nil {
		return phaseRecord{}, fmt.Errorf("read firmware runner state: %w", err)
	}
	var record phaseRecord
	if err := jsonv2.Unmarshal(data, &record, jsonv2.RejectUnknownMembers(true)); err != nil {
		return phaseRecord{}, errors.New("invalid firmware runner state")
	}
	if record.Version != phaseRecordVersion || record.AttemptID != s.attemptID || record.Dispatches < 0 ||
		(record.Dispatches > 0 && record.Binding == (phaseAttemptBinding{})) ||
		(record.Pending != nil && (record.Pending.AttemptID != record.AttemptID || record.Dispatches == 0)) {
		return phaseRecord{}, errors.New("inconsistent firmware runner state")
	}
	if len(record.Accepted) != 0 {
		var accepted phaseResult
		if err := jsonv2.Unmarshal(record.Accepted, &accepted, jsonv2.RejectUnknownMembers(true)); err != nil || accepted.Identity.AttemptID != record.AttemptID {
			return phaseRecord{}, errors.New("inconsistent firmware runner state")
		}
		if accepted.Checkpoint != nil {
			raw, err := phaseResultRawField(record.Accepted, "checkpoint")
			if err != nil || !bytes.Equal(raw, record.CheckpointRaw) {
				return phaseRecord{}, errors.New("inconsistent firmware runner checkpoint")
			}
		}
		if accepted.Prepared != nil {
			raw, err := phaseResultRawField(record.Accepted, "prepared")
			if err != nil || !bytes.Equal(raw, record.SelectedRaw) {
				return phaseRecord{}, errors.New("inconsistent firmware runner selection")
			}
		}
	}
	if len(record.CheckpointRaw) != 0 {
		var checkpoint phaseCheckpoint
		if err := jsonv2.Unmarshal(record.CheckpointRaw, &checkpoint, jsonv2.RejectUnknownMembers(true)); err != nil {
			return phaseRecord{}, errors.New("inconsistent firmware runner checkpoint")
		}
		record.Checkpoint = &checkpoint
	}
	if len(record.SelectedRaw) != 0 {
		var selected phasePreparedEvidence
		if err := jsonv2.Unmarshal(record.SelectedRaw, &selected, jsonv2.RejectUnknownMembers(true)); err != nil {
			return phaseRecord{}, errors.New("inconsistent firmware runner selection")
		}
		record.Selected = &selected
	}
	return record, nil
}

func (s phaseRecordStore) save(record phaseRecord) error {
	name, err := s.fileName()
	if err != nil {
		return err
	}
	if record.AttemptID != s.attemptID {
		return errors.New("firmware runner state does not match its attempt")
	}
	retired, err := s.isRetired()
	if err != nil {
		return err
	}
	if retired {
		return errPhaseRetired
	}
	record.Version = phaseRecordVersion
	data, err := marshalPhaseRecord(record)
	if err != nil {
		return err
	}
	if len(data) > maxPhaseRecordBytes {
		return errors.New("firmware runner state exceeds its storage limit")
	}
	if err := s.dir.ReplaceFileDurable(name, data, 0o600); err != nil {
		return fmt.Errorf("%w: %w", errPhaseDurabilityUncertain, err)
	}
	return nil
}

func marshalPhaseRecord(record phaseRecord) ([]byte, error) {
	var data bytes.Buffer
	encoder := json.NewEncoder(&data)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(record); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(data.Bytes(), []byte{'\n'}), nil
}

func phaseResultRawField(data []byte, name string) (json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, err
	}
	field, found := fields[name]
	if !found || len(field) == 0 {
		return nil, nil
	}
	return bytes.Clone(field), nil
}

func (s phaseRecordStore) retire() error {
	if s.dir == nil {
		return nil
	}
	name, err := s.fileName()
	if err != nil {
		return err
	}
	record, err := s.load()
	if err != nil {
		return err
	}
	if !record.Retired && record.Pending != nil {
		return errPhasePending
	}
	if !record.Retired {
		marker, err := json.Marshal(phaseRetiredMarker{Version: phaseRecordVersion, AttemptID: s.attemptID})
		if err != nil {
			return err
		}
		markerName, err := s.retiredFileName()
		if err != nil {
			return err
		}
		if err := s.dir.ReplaceFileDurable(markerName, marker, 0o600); err != nil {
			return fmt.Errorf("persist retired firmware attempt: %w", err)
		}
	}
	if record.AttemptID == "" {
		return s.dir.Sync()
	}
	if _, err := s.dir.ReadFile(name, maxPhaseRecordBytes); errors.Is(err, fs.ErrNotExist) {
		return s.dir.Sync()
	} else if err != nil {
		return err
	}
	if err := s.dir.Sync(); err != nil {
		return fmt.Errorf("sync retirement marker before removing runner record: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := s.dir.RemoveTree(ctx, name, cleanupBounds()); err != nil {
		return err
	}
	return s.dir.Sync()
}

func (s phaseRecordStore) isRetired() (bool, error) {
	if s.dir == nil {
		return false, nil
	}
	name, err := s.retiredFileName()
	if err != nil {
		return false, err
	}
	data, err := s.dir.ReadFile(name, 2048)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read retired firmware attempt marker: %w", err)
	}
	var marker phaseRetiredMarker
	if err := jsonv2.Unmarshal(data, &marker, jsonv2.RejectUnknownMembers(true)); err != nil ||
		marker.Version != phaseRecordVersion || marker.AttemptID != s.attemptID {
		return false, errors.New("invalid retired firmware attempt marker")
	}
	return true, nil
}

func (s phaseRecordStore) fileName() (string, error) {
	if s.attemptID == "" {
		return "", errors.New("firmware runner attempt identity is required")
	}
	digest := sha256.Sum256([]byte(s.attemptID))
	return phaseRecordPrefix + hex.EncodeToString(digest[:]) + ".json", nil
}

func (s phaseRecordStore) retiredFileName() (string, error) {
	name, err := s.fileName()
	if err != nil {
		return "", err
	}
	return name[:len(name)-len(".json")] + ".retired", nil
}

func phaseHeldPin(dir fileio.Anchor, attemptID string) (bool, error) {
	if dir == nil {
		return false, nil
	}
	record, err := (phaseRecordStore{dir: dir, attemptID: attemptID}).load()
	if err != nil {
		return false, err
	}
	if record.Retired {
		return false, nil
	}
	if record.Pending != nil && record.AttemptID == attemptID {
		return true, nil
	}
	parent, err := dir.OpenDir("phase-roots", fileio.DirOptions{Trusted: true, ReadOnly: true})
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer parent.Close()
	root, err := parent.OpenDir("active", fileio.DirOptions{Trusted: true, ReadOnly: true})
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer root.Close()
	data, err := root.ReadFile("attempt-id", maxAttemptIDBytes)
	if errors.Is(err, fs.ErrNotExist) {
		// Setup may have stopped before recording the owner.
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return len(data) == 0 || string(data) == attemptID, nil
}
