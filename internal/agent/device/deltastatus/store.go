package deltastatus

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"path/filepath"
	"sync"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/log"
)

const (
	// FileName is the dedicated on-disk state file for agent-reported delta outcomes.
	FileName                 = "delta-status.json"
	fileMode     fs.FileMode = 0o600
	stateVersion             = 1
)

// Store persists the most recent delta results independently of status delivery.
// It is shared by the OS and application prefetch managers.
type Store struct {
	mu         sync.Mutex
	readWriter fileio.ReadWriter
	path       string
	log        *log.PrefixLogger
	state      persistedState
}

type persistedState struct {
	Version      int                          `json:"version"`
	OS           *osState                     `json:"os,omitempty"`
	Applications map[string]*applicationState `json:"applications,omitempty"`
}

type osState struct {
	TargetKey string                          `json:"targetKey"`
	Result    *v1beta1.DeviceDeltaApplyStatus `json:"result,omitempty"`
}

type applicationState struct {
	SpecKey string                            `json:"specKey"`
	Targets map[string]string                 `json:"targets,omitempty"`
	Results map[string]applicationImageResult `json:"results,omitempty"`
}

type applicationImageResult struct {
	TargetKey string                         `json:"targetKey"`
	Status    v1beta1.DeviceDeltaApplyStatus `json:"status"`
}

// ApplicationSnapshot is an in-memory copy of an application's current delta
// targets and the results recorded for them.
type ApplicationSnapshot struct {
	SpecKey string
	Targets map[string]string
	Results map[string]ApplicationResult
}

// New loads persisted delta outcomes. Invalid or unreadable state is logged and
// ignored so a status file problem does not prevent the agent from starting.
func New(readWriter fileio.ReadWriter, dataDir string, logger *log.PrefixLogger) *Store {
	store := &Store{
		readWriter: readWriter,
		path:       filepath.Join(dataDir, FileName),
		log:        logger,
		state: persistedState{
			Version:      stateVersion,
			Applications: make(map[string]*applicationState),
		},
	}

	data, err := readWriter.ReadFile(store.path)
	if err != nil {
		if !fileio.IsNotExist(err) {
			logger.Warnf("Failed to read persisted delta status: %v", err)
		}
		return store
	}

	var persisted persistedState
	if err := json.Unmarshal(data, &persisted); err != nil {
		logger.Warnf("Ignoring invalid persisted delta status: %v", err)
		return store
	}
	if persisted.Version != stateVersion {
		logger.Warnf("Ignoring persisted delta status with unsupported version %d", persisted.Version)
		return store
	}
	if persisted.Applications == nil {
		persisted.Applications = make(map[string]*applicationState)
	}
	for application, appState := range persisted.Applications {
		if appState == nil {
			delete(persisted.Applications, application)
			continue
		}
		if appState.Targets == nil {
			appState.Targets = make(map[string]string)
		}
		if appState.Results == nil {
			appState.Results = make(map[string]applicationImageResult)
		}
		for targetID, result := range appState.Results {
			if appState.Targets[targetID] != result.TargetKey {
				delete(appState.Results, targetID)
			}
		}
	}
	store.state = persisted
	return store
}

// Fingerprint returns a stable key for a set of target attributes.
func Fingerprint(values ...string) string {
	data, _ := json.Marshal(values)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// SetOSTarget keeps the persisted OS result only while its rendered target is
// unchanged. An empty key clears the OS entry.
func (s *Store) SetOSTarget(targetKey string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if targetKey == "" {
		if s.state.OS == nil {
			return nil
		}
		s.state.OS = nil
		return s.saveLocked()
	}
	if s.state.OS != nil && s.state.OS.TargetKey == targetKey {
		return nil
	}
	s.state.OS = &osState{TargetKey: targetKey}
	return s.saveLocked()
}

// OSResult returns a copy of the result for the requested target.
func (s *Store) OSResult(targetKey string) *v1beta1.DeviceDeltaApplyStatus {
	s.mu.Lock()
	defer s.mu.Unlock()

	if targetKey == "" || s.state.OS == nil || s.state.OS.TargetKey != targetKey || s.state.OS.Result == nil {
		return nil
	}
	result := cloneStatus(*s.state.OS.Result)
	return &result
}

// RecordOSResult durably records the result for the active OS target. A result
// from a superseded target is ignored.
func (s *Store) RecordOSResult(targetKey string, result v1beta1.DeviceDeltaApplyStatus) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if targetKey == "" || s.state.OS == nil || s.state.OS.TargetKey != targetKey {
		return nil
	}
	cloned := cloneStatus(result)
	s.state.OS.Result = &cloned
	return s.saveLocked()
}

// ReconcileApplicationSpecs resets stored results when an application spec
// changes and removes state for applications no longer present.
func (s *Store) ReconcileApplicationSpecs(specKeys map[string]string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.state.Applications == nil {
		s.state.Applications = make(map[string]*applicationState)
	}
	changed := false
	for application, specKey := range specKeys {
		current := s.state.Applications[application]
		if current != nil && current.SpecKey == specKey {
			continue
		}
		s.state.Applications[application] = &applicationState{
			SpecKey: specKey,
			Targets: make(map[string]string),
			Results: make(map[string]applicationImageResult),
		}
		changed = true
	}
	for application := range s.state.Applications {
		if _, exists := specKeys[application]; exists {
			continue
		}
		delete(s.state.Applications, application)
		changed = true
	}
	if changed {
		return s.saveLocked()
	}
	return nil
}

// ReconcileApplicationTargets updates per-image target identities. On an
// incomplete collection, targets not seen yet are retained so nested images
// discovered in a later collection pass do not discard completed results.
func (s *Store) ReconcileApplicationTargets(targets map[string]map[string]string, complete bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	changed := false
	for application, current := range s.state.Applications {
		if current == nil {
			current = &applicationState{
				Targets: make(map[string]string),
				Results: make(map[string]applicationImageResult),
			}
			s.state.Applications[application] = current
			changed = true
		}
		if current.Targets == nil {
			current.Targets = make(map[string]string)
		}
		currentTargets := targets[application]
		for targetID, targetKey := range currentTargets {
			if current.Targets[targetID] == targetKey {
				continue
			}
			current.Targets[targetID] = targetKey
			delete(current.Results, targetID)
			changed = true
		}
		if !complete {
			continue
		}
		for targetID := range current.Targets {
			if _, exists := currentTargets[targetID]; exists {
				continue
			}
			delete(current.Targets, targetID)
			delete(current.Results, targetID)
			changed = true
		}
	}
	if changed {
		return s.saveLocked()
	}
	return nil
}

// ApplicationResults returns copies of results saved for an application.
func (s *Store) ApplicationResults(application string) map[string]ApplicationResult {
	snapshot := s.ApplicationSnapshot(application)
	if snapshot == nil {
		return nil
	}
	return snapshot.Results
}

// ApplicationSnapshot returns the current target set and its recorded results.
// The store is loaded from disk once by New; this method only copies in-memory
// state so status collection does not perform disk I/O.
func (s *Store) ApplicationSnapshot(application string) *ApplicationSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()

	current := s.state.Applications[application]
	if current == nil {
		return nil
	}
	snapshot := &ApplicationSnapshot{
		SpecKey: current.SpecKey,
		Targets: make(map[string]string, len(current.Targets)),
		Results: make(map[string]ApplicationResult, len(current.Results)),
	}
	for targetID, targetKey := range current.Targets {
		snapshot.Targets[targetID] = targetKey
	}
	for targetID, result := range current.Results {
		snapshot.Results[targetID] = ApplicationResult{
			TargetKey: result.TargetKey,
			Status:    cloneStatus(result.Status),
		}
	}
	return snapshot
}

// ApplicationResult is one image's persisted delta result.
type ApplicationResult struct {
	TargetKey string
	Status    v1beta1.DeviceDeltaApplyStatus
}

// RecordApplicationResult durably records one image result. Results for an
// image target that has since changed are ignored.
func (s *Store) RecordApplicationResult(application, specKey, targetID, targetKey string, result v1beta1.DeviceDeltaApplyStatus) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	current := s.state.Applications[application]
	if specKey == "" || targetID == "" || targetKey == "" || current == nil || current.SpecKey != specKey || current.Targets[targetID] != targetKey {
		return nil
	}
	if current.Results == nil {
		current.Results = make(map[string]applicationImageResult)
	}
	current.Results[targetID] = applicationImageResult{TargetKey: targetKey, Status: cloneStatus(result)}
	return s.saveLocked()
}

// ClearApplicationResult invalidates an outcome when the active target's content changes.
// Invalidations for superseded specs or targets are ignored.
func (s *Store) ClearApplicationResult(application, specKey, targetID, targetKey string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.state.Applications[application]
	if current == nil || specKey == "" || current.SpecKey != specKey || targetKey == "" || current.Targets[targetID] != targetKey {
		return nil
	}
	if _, exists := current.Results[targetID]; !exists {
		return nil
	}
	delete(current.Results, targetID)
	return s.saveLocked()
}

func (s *Store) saveLocked() error {
	data, err := json.Marshal(s.state)
	if err != nil {
		return fmt.Errorf("marshal persisted delta status: %w", err)
	}
	if err := s.readWriter.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("create delta status directory: %w", err)
	}
	if err := s.readWriter.WriteFile(s.path, data, fileMode); err != nil {
		return fmt.Errorf("write persisted delta status: %w", err)
	}
	return nil
}

func cloneStatus(status v1beta1.DeviceDeltaApplyStatus) v1beta1.DeviceDeltaApplyStatus {
	cloned := status
	if status.FallbackReason != nil {
		reason := *status.FallbackReason
		cloned.FallbackReason = &reason
	}
	return cloned
}
