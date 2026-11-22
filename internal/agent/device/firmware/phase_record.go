package firmware

import (
	"context"
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
	phaseRecordName     = "phase-runner.json"
	phaseRecordVersion  = 1
	maxPhaseRecordBytes = 4 << 20
)

var (
	errPhasePending             = errors.New("firmware phase has an unresolved dispatch")
	errPhaseDurabilityUncertain = errors.New("firmware runner state durability is uncertain")
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
	Checkpoint      *phaseCheckpoint       `json:"checkpoint,omitempty"`
	Selected        *phasePreparedEvidence `json:"selected,omitempty"`
	Accepted        *phaseResult           `json:"accepted,omitempty"`
	PendingReceipt  *policy.Receipt        `json:"pendingReceipt,omitempty"`
	AcceptedReceipt *policy.Receipt        `json:"acceptedReceipt,omitempty"`
}

type phaseRecordStore struct {
	dir fileio.Anchor
}

func (record phaseRecord) checkResultCapacity(maxResultBytes int64) error {
	// Each accepted field is drawn from the response. JSON escaping can expand
	// a byte to six, and the accepted response may also supply the standalone
	// selection and checkpoint. Keep the entire pending record as headroom
	// for any previous state carried into the next commit.
	const maxResultExpansion = 12
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	// Allow for fields introduced when committing the result and receipt.
	const commitOverhead = 1024
	available := maxPhaseRecordBytes - len(data) - commitOverhead
	if available < 0 || maxResultBytes > int64(available/maxResultExpansion) {
		return errors.New("firmware phase result allowance exceeds durable runner storage")
	}
	return nil
}

func (s phaseRecordStore) load() (phaseRecord, error) {
	data, err := s.dir.ReadFile(phaseRecordName, maxPhaseRecordBytes)
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
	if record.Version != phaseRecordVersion || record.AttemptID == "" || record.Dispatches < 0 ||
		(record.Dispatches > 0 && record.Binding == (phaseAttemptBinding{})) ||
		(record.Pending != nil && (record.Pending.AttemptID != record.AttemptID || record.Dispatches == 0)) ||
		(record.Accepted != nil && record.Accepted.Identity.AttemptID != record.AttemptID) {
		return phaseRecord{}, errors.New("inconsistent firmware runner state")
	}
	return record, nil
}

func (s phaseRecordStore) save(record phaseRecord) error {
	record.Version = phaseRecordVersion
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if len(data) > maxPhaseRecordBytes {
		return errors.New("firmware runner state exceeds its storage limit")
	}
	if err := s.dir.ReplaceFileDurable(phaseRecordName, data, 0o600); err != nil {
		return fmt.Errorf("%w: %w", errPhaseDurabilityUncertain, err)
	}
	return nil
}

func (s phaseRecordStore) retire(attemptID string) error {
	if s.dir == nil {
		return nil
	}
	record, err := s.load()
	if err != nil {
		return err
	}
	if record.AttemptID == "" || record.AttemptID != attemptID {
		return nil
	}
	if record.Pending != nil {
		return errPhasePending
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := s.dir.RemoveTree(ctx, phaseRecordName, cleanupBounds()); err != nil {
		return err
	}
	return s.dir.Sync()
}

func phaseHeldPin(dir fileio.Anchor, attemptID string) (bool, error) {
	if dir == nil {
		return false, nil
	}
	record, err := (phaseRecordStore{dir: dir}).load()
	if err != nil {
		return false, err
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
