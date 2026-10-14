package deltastatus

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
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
	dirty      bool
}

type persistedState struct {
	Version      int                          `json:"version"`
	OS           *osState                     `json:"os,omitempty"`
	Applications map[string]*applicationState `json:"applications,omitempty"`
}

type osImageResult struct {
	Image    string                         `json:"image"`
	Digest   string                         `json:"digest"`
	Status   v1beta1.DeviceDeltaApplyStatus `json:"status"`
	LastUsed uint64                         `json:"lastUsed,omitempty"`
}

type osState struct {
	ActiveImageKey string                          `json:"activeImageKey,omitempty"`
	History        map[string]osImageResult        `json:"history,omitempty"`
	TargetKey      string                          `json:"targetKey"`
	Result         *v1beta1.DeviceDeltaApplyStatus `json:"result,omitempty"`
	HistoryClock   uint64                          `json:"historyClock,omitempty"`
	ObservedKey    string                          `json:"observedKey,omitempty"`
}

type applicationState struct {
	History      map[string]applicationObservedResult `json:"history,omitempty"`
	Lifetime     string                               `json:"lifetime,omitempty"`
	SpecKey      string                               `json:"specKey"`
	Targets      map[string]string                    `json:"targets,omitempty"`
	Results      map[string]applicationImageResult    `json:"results,omitempty"`
	Volumes      map[string]applicationVolumeImage    `json:"volumes,omitempty"`
	HistoryClock uint64                               `json:"historyClock,omitempty"`
	ObservedKeys []string                             `json:"observedKeys,omitempty"`
}

type applicationImageResult struct {
	TargetKey string                         `json:"targetKey"`
	Status    v1beta1.DeviceDeltaApplyStatus `json:"status"`
}

// ApplicationImage is a verified observation scoped to its storage owner and OCI type.
// The digest is the single registry identity supplied by the owning runtime.
type ApplicationImage struct {
	Image   string `json:"image"`
	Owner   string `json:"owner"`
	OCIType string `json:"ociType"`
	Digest  string `json:"digest"`
}

type applicationObservedResult struct {
	Image     ApplicationImage               `json:"image"`
	TargetKey string                         `json:"targetKey"`
	Status    v1beta1.DeviceDeltaApplyStatus `json:"status"`
	LastUsed  uint64                         `json:"lastUsed,omitempty"`
	Pending   bool                           `json:"pending,omitempty"`
}

type applicationVolumeImage struct {
	CreatedAt string           `json:"createdAt"`
	Image     ApplicationImage `json:"image"`
}

// ApplicationVolumeBinding is the stored image identity and runtime creation
// identity associated with one installed application volume.
type ApplicationVolumeBinding struct {
	CreatedAt string
	Image     ApplicationImage
}

func applicationHistoryKey(image ApplicationImage) string {
	return Fingerprint(image.Owner, image.Image, image.OCIType, image.Digest)
}

// ApplicationSnapshot is an in-memory copy of an application's current delta
// targets and the results recorded for them.
type ApplicationSnapshot struct {
	Lifetime string
	SpecKey  string
	Targets  map[string]string
	Results  map[string]ApplicationResult
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
		if appState == nil || appState.Lifetime == "" {
			delete(persisted.Applications, application)
			continue
		}
		if appState.Targets == nil {
			appState.Targets = make(map[string]string)
		}
		if appState.Results == nil {
			appState.Results = make(map[string]applicationImageResult)
		}
		if appState.Volumes == nil {
			appState.Volumes = make(map[string]applicationVolumeImage)
		}

		slices.Sort(appState.ObservedKeys)
		appState.ObservedKeys = slices.Compact(appState.ObservedKeys)
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

func newApplicationLifetime() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate application lifetime: %w", err)
	}
	return hex.EncodeToString(value[:]), nil
}

// SetOSTarget updates the active attempt while retaining verified image history.
// An empty key clears the OS scope.
func (s *Store) SetOSTarget(targetKey string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if targetKey == "" {
		if s.state.OS == nil {
			return s.saveIfDirtyLocked()
		}
		s.state.OS = nil
		return s.saveLocked()
	}
	if s.state.OS != nil && s.state.OS.TargetKey == targetKey {
		if s.dirty {
			return s.saveLocked()
		}
		return nil
	}
	if s.state.OS == nil {
		s.state.OS = &osState{}
	}
	s.state.OS.TargetKey = targetKey
	s.state.OS.ActiveImageKey = ""
	s.state.OS.Result = nil
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

// RecordOSImageResult records a verified image association for the active attempt.
// Superseded attempts and unknown identities cannot add a publishable outcome.
func (s *Store) RecordOSImageResult(targetKey, image, digest string, result v1beta1.DeviceDeltaApplyStatus) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if targetKey == "" || image == "" || digest == "" || s.state.OS == nil || s.state.OS.TargetKey != targetKey {
		return nil
	}
	if s.state.OS.History == nil {
		s.state.OS.History = make(map[string]osImageResult)
	}
	key := Fingerprint(image, digest)
	s.state.OS.ActiveImageKey = key
	if _, exists := s.state.OS.History[key]; exists {
		return s.saveLocked()
	}
	s.state.OS.History[key] = osImageResult{Image: image, Digest: digest, Status: cloneStatus(result)}
	pruneOSHistory(s.state.OS)
	return s.saveLocked()
}

// ObservedOSResult selects an owned copy for exactly the booted image and digest.
// It never uses the active desired attempt or performs disk I/O.
func (s *Store) ObservedOSResult(image, digest string) *v1beta1.DeviceDeltaApplyStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	if image == "" || digest == "" || s.state.OS == nil {
		return nil
	}
	os := s.state.OS
	key := Fingerprint(image, digest)
	changed := false
	if os.ObservedKey != key {
		os.ObservedKey = key
		changed = true
	}
	var selected *v1beta1.DeviceDeltaApplyStatus
	if result, exists := os.History[key]; exists && result.Image == image && result.Digest == digest {
		os.HistoryClock++
		result.LastUsed = os.HistoryClock
		os.History[key] = result
		cloned := cloneStatus(result.Status)
		selected = &cloned
		changed = true
	}
	if pruneOSHistory(os) {
		changed = true
	}
	if changed {
		s.dirty = true
	}
	return selected
}

// ReconcileApplicationSpecs advances active attempts without discarding observed
// image history, and removes state for applications no longer present.
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
		var history map[string]applicationObservedResult
		var volumes map[string]applicationVolumeImage
		var historyClock uint64
		var observedKeys []string
		lifetime := ""
		if current != nil {
			history = current.History
			volumes = current.Volumes
			historyClock = current.HistoryClock
			observedKeys = current.ObservedKeys
			lifetime = current.Lifetime
		}
		if lifetime == "" {
			var err error
			lifetime, err = newApplicationLifetime()
			if err != nil {
				return err
			}
		}
		s.state.Applications[application] = &applicationState{
			History:      history,
			Lifetime:     lifetime,
			SpecKey:      specKey,
			Targets:      make(map[string]string),
			Results:      make(map[string]applicationImageResult),
			Volumes:      volumes,
			HistoryClock: historyClock,
			ObservedKeys: observedKeys,
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
	if changed || s.dirty {
		return s.saveLocked()
	}
	return nil
}

// ApplicationLifetime returns the stable identity for an application's current
// installation lifetime. Desired-spec changes retain it; reconciliation after
// removal creates a new value.
func (s *Store) ApplicationLifetime(application string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if current := s.state.Applications[application]; current != nil {
		return current.Lifetime
	}
	return ""
}

// EnsureApplicationLifetime creates an empty application record when runtime
// work begins before dependency prefetch has reconciled the desired spec.
// Existing records and lifetimes are preserved across ordinary updates.
func (s *Store) EnsureApplicationLifetime(application string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if application == "" {
		return "", nil
	}
	if current := s.state.Applications[application]; current != nil && current.Lifetime != "" {
		if s.dirty {
			if err := s.saveLocked(); err != nil {
				return "", err
			}
		}
		return current.Lifetime, nil
	}
	lifetime, err := newApplicationLifetime()
	if err != nil {
		return "", err
	}
	if s.state.Applications == nil {
		s.state.Applications = make(map[string]*applicationState)
	}
	s.state.Applications[application] = &applicationState{
		Lifetime: lifetime,
		Targets:  make(map[string]string),
		Results:  make(map[string]applicationImageResult),
		Volumes:  make(map[string]applicationVolumeImage),
	}
	if err := s.saveLocked(); err != nil {
		return "", err
	}
	return lifetime, nil
}

// RemoveApplication retires the current lifetime immediately and removes all
// results and volume bindings. A subsequent ensure receives a fresh token.
func (s *Store) RemoveApplication(application string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if application == "" {
		return nil
	}
	if _, exists := s.state.Applications[application]; !exists {
		if s.dirty {
			return s.saveLocked()
		}
		return nil
	}
	delete(s.state.Applications, application)
	return s.saveLocked()
}

// ReconcileApplicationTargets updates per-image target identities. On an
// incomplete collection, targets not seen yet are retained so nested images
// discovered in a later collection pass do not discard completed results.
// Results follow an unchanged full target key when its indexing identity changes.
func (s *Store) ReconcileApplicationTargets(targets map[string]map[string]string, complete bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	changed := false
	for application, current := range s.state.Applications {
		if current == nil {
			lifetime, err := newApplicationLifetime()
			if err != nil {
				return err
			}
			current = &applicationState{
				Lifetime: lifetime,
				Targets:  make(map[string]string),
				Results:  make(map[string]applicationImageResult),
				Volumes:  make(map[string]applicationVolumeImage),
			}
			s.state.Applications[application] = current
			changed = true
		}
		if current.Targets == nil {
			current.Targets = make(map[string]string)
		}
		resultsByTargetKey := make(map[string]applicationImageResult, len(current.Results))
		for targetID, result := range current.Results {
			if current.Targets[targetID] == result.TargetKey {
				resultsByTargetKey[result.TargetKey] = result
			}
		}
		currentTargets := targets[application]
		currentTargetKeys := make(map[string]struct{}, len(currentTargets))
		for targetID, targetKey := range currentTargets {
			currentTargetKeys[targetKey] = struct{}{}
			if current.Targets[targetID] == targetKey {
				continue
			}
			current.Targets[targetID] = targetKey
			delete(current.Results, targetID)
			if result, exists := resultsByTargetKey[targetKey]; exists {
				current.Results[targetID] = result
			}
			changed = true
		}
		for targetID, targetKey := range current.Targets {
			if _, exists := currentTargets[targetID]; exists {
				continue
			}
			if _, reindexed := currentTargetKeys[targetKey]; !complete && !reindexed {
				continue
			}
			delete(current.Targets, targetID)
			delete(current.Results, targetID)
			changed = true
		}
	}
	if changed || s.dirty {
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
		Lifetime: current.Lifetime,
		SpecKey:  current.SpecKey,
		Targets:  make(map[string]string, len(current.Targets)),
		Results:  make(map[string]ApplicationResult, len(current.Results)),
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

// ApplicationAttempt captures the immutable scheduling identity of one target.
// Completion and invalidation use the same token so obsolete work is ignored.
type ApplicationAttempt struct {
	Application string
	Lifetime    string
	SpecKey     string
	TargetID    string
	TargetKey   string
}

func (a ApplicationAttempt) matches(current *applicationState) bool {
	return current != nil && a.Lifetime != "" && a.SpecKey != "" && a.TargetID != "" && a.TargetKey != "" &&
		current.Lifetime == a.Lifetime && current.SpecKey == a.SpecKey && current.Targets[a.TargetID] == a.TargetKey
}

// RecordApplicationResult durably records one image result. Results for an
// image target that has since changed are ignored.
func (s *Store) RecordApplicationResult(application, specKey, targetID, targetKey string, result v1beta1.DeviceDeltaApplyStatus) error {
	return s.RecordApplicationResultForAttempt(ApplicationAttempt{Application: application, Lifetime: s.ApplicationLifetime(application), SpecKey: specKey, TargetID: targetID, TargetKey: targetKey}, result)
}

// RecordApplicationResultForAttempt durably records a scheduling result only
// when both the desired attempt and application lifetime are still current.
func (s *Store) RecordApplicationResultForAttempt(attempt ApplicationAttempt, result v1beta1.DeviceDeltaApplyStatus) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	current := s.state.Applications[attempt.Application]
	if !attempt.matches(current) {
		return nil
	}
	if current.Results == nil {
		current.Results = make(map[string]applicationImageResult)
	}
	current.Results[attempt.TargetID] = applicationImageResult{TargetKey: attempt.TargetKey, Status: cloneStatus(result)}
	return s.saveLocked()
}

// RecordApplicationImageResult persists verified history only for an active attempt.
// Attempt-only results are used for scheduling, never for publication.
func (s *Store) RecordApplicationImageResult(application, specKey, targetID, targetKey string, image ApplicationImage, result v1beta1.DeviceDeltaApplyStatus) error {
	return s.RecordApplicationImageResultForAttempt(ApplicationAttempt{Application: application, Lifetime: s.ApplicationLifetime(application), SpecKey: specKey, TargetID: targetID, TargetKey: targetKey}, image, result)
}

// RecordApplicationImageResultForAttempt records digest-associated history only
// for the application lifetime and desired attempt that initiated the work.
func (s *Store) RecordApplicationImageResultForAttempt(attempt ApplicationAttempt, image ApplicationImage, result v1beta1.DeviceDeltaApplyStatus) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.state.Applications[attempt.Application]
	if !attempt.matches(current) || image.Image == "" || image.Owner == "" || image.Digest == "" || image.OCIType == "" {
		return nil
	}
	if current.History == nil {
		current.History = make(map[string]applicationObservedResult)
	}
	key := applicationHistoryKey(image)
	// An accepted identity replaces earlier pending content for this target.
	// Previous complete observations remain protected independently.
	superseded := false
	for previousKey, previous := range current.History {
		if previousKey != key && previous.TargetKey == attempt.TargetKey && previous.Pending {
			previous.Pending = false
			current.History[previousKey] = previous
			superseded = true
		}
	}
	// An already-recorded identity keeps the outcome describing its original use.
	if existing, exists := current.History[key]; exists {
		if superseded || !existing.Pending || existing.TargetKey != attempt.TargetKey {
			existing.Pending = true
			existing.TargetKey = attempt.TargetKey
			current.History[key] = existing
			pruneApplicationHistory(current)
			return s.saveLocked()
		}
		if s.dirty {
			return s.saveLocked()
		}
		return nil
	}
	current.History[key] = applicationObservedResult{Image: image, TargetKey: attempt.TargetKey, Status: cloneStatus(result), Pending: true}
	pruneApplicationHistory(current)
	return s.saveLocked()
}

// ObservedApplicationResult combines only history matching every image in a
// complete observed snapshot. It returns an owned result without runtime or disk I/O.
func (s *Store) ObservedApplicationResult(application string, images []ApplicationImage, complete bool) *v1beta1.DeviceDeltaApplyStatus {
	return s.ObservedApplicationResultForLifetime(application, s.ApplicationLifetime(application), images, complete)
}

// ObservedApplicationResultForLifetime selects an owned aggregate only when the
// complete snapshot belongs to the application's currently registered lifetime.
func (s *Store) ObservedApplicationResultForLifetime(application, lifetime string, images []ApplicationImage, complete bool) *v1beta1.DeviceDeltaApplyStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.state.Applications[application]
	if !complete || current == nil || lifetime == "" || current.Lifetime != lifetime {
		return nil
	}
	keys := make([]string, 0, len(images))
	seen := make(map[string]struct{}, len(images))
	imagesByKey := make(map[string]ApplicationImage, len(images))
	for _, image := range images {
		if image.Image == "" || image.Owner == "" || image.Digest == "" || image.OCIType == "" {
			return nil
		}
		key := applicationHistoryKey(image)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		keys = append(keys, key)
		imagesByKey[key] = image
	}
	slices.Sort(keys)
	changed := !slices.Equal(current.ObservedKeys, keys)
	if changed {
		current.ObservedKeys = slices.Clone(keys)
	}

	results := make(map[string]v1beta1.DeviceDeltaApplyStatus, len(keys))
	matchedKeys := make([]string, 0, len(keys))
	allMatched := len(keys) > 0
	for _, key := range keys {
		result, exists := current.History[key]
		if !exists || result.Image != imagesByKey[key] {
			allMatched = false
			continue
		}
		results[key] = result.Status
		matchedKeys = append(matchedKeys, key)
	}
	if len(matchedKeys) > 0 {
		current.HistoryClock++
		for _, key := range matchedKeys {
			result := current.History[key]
			result.LastUsed = current.HistoryClock
			current.History[key] = result
		}
		changed = true
	}

	var selected *v1beta1.DeviceDeltaApplyStatus
	if allMatched {
		selected = aggregateApplicationResults(results)
	}
	if selected != nil {
		for _, key := range matchedKeys {
			result := current.History[key]
			if result.Pending {
				result.Pending = false
				current.History[key] = result
				changed = true
			}
		}
	}
	if pruneApplicationHistory(current) {
		changed = true
	}
	if changed {
		s.dirty = true
	}
	return selected
}

func pruneOSHistory(current *osState) bool {
	if current == nil || len(current.History) <= 2 {
		return false
	}
	protected := make(map[string]struct{}, len(current.History))
	if current.ObservedKey != "" {
		protected[current.ObservedKey] = struct{}{}
	}
	// Only the latest accepted staged identity is still active. A stable
	// mutable reference does not keep replaced stages protected forever.
	if current.ActiveImageKey != "" {
		protected[current.ActiveImageKey] = struct{}{}
	}
	candidates := make([]historyCandidate, 0, max(0, len(current.History)-len(protected)))
	for key, result := range current.History {
		if _, keep := protected[key]; keep {
			continue
		}
		candidates = append(candidates, historyCandidate{key: key, lastUsed: result.LastUsed})
	}
	sortHistoryCandidates(candidates)
	removed := 0
	for _, candidate := range candidates {
		if len(current.History) <= 2 {
			break
		}
		delete(current.History, candidate.key)
		removed++
	}
	return removed > 0
}

func pruneApplicationHistory(current *applicationState) bool {
	if current == nil || len(current.History) == 0 {
		return false
	}
	observed := make(map[string]struct{}, len(current.ObservedKeys))
	for _, key := range current.ObservedKeys {
		observed[key] = struct{}{}
	}
	activeTargetKeys := make(map[string]struct{}, len(current.Targets))
	for _, targetKey := range current.Targets {
		if targetKey != "" {
			activeTargetKeys[targetKey] = struct{}{}
		}
	}
	capacity := 2 * max(len(observed), len(activeTargetKeys))
	if len(current.History) <= capacity {
		return false
	}
	protected := make(map[string]struct{}, len(current.History))
	for key := range observed {
		protected[key] = struct{}{}
	}
	for key, result := range current.History {
		if _, active := activeTargetKeys[result.TargetKey]; active && result.Pending {
			protected[key] = struct{}{}
		}
	}
	candidates := make([]historyCandidate, 0, max(0, len(current.History)-len(protected)))
	for key, result := range current.History {
		if _, keep := protected[key]; keep {
			continue
		}
		candidates = append(candidates, historyCandidate{key: key, lastUsed: result.LastUsed})
	}
	sortHistoryCandidates(candidates)
	removed := 0
	for _, candidate := range candidates {
		if len(current.History) <= capacity {
			break
		}
		delete(current.History, candidate.key)
		removed++
	}
	return removed > 0
}

type historyCandidate struct {
	key      string
	lastUsed uint64
}

func sortHistoryCandidates(candidates []historyCandidate) {
	slices.SortFunc(candidates, func(left, right historyCandidate) int {
		if left.lastUsed < right.lastUsed {
			return -1
		}
		if left.lastUsed > right.lastUsed {
			return 1
		}
		return strings.Compare(left.key, right.key)
	})
}

// RecordApplicationVolumeImage binds an image-backed volume's exact installed
// content to its public Podman creation identity. A stale lifetime cannot
// repopulate evidence after application removal and recreation.
func (s *Store) RecordApplicationVolumeImage(application, lifetime, volumeID, createdAt string, image ApplicationImage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.state.Applications[application]
	if current == nil || lifetime == "" || current.Lifetime != lifetime || volumeID == "" || createdAt == "" || image.Image == "" || image.Owner == "" || image.OCIType == "" || image.Digest == "" {
		return nil
	}
	if current.Volumes == nil {
		current.Volumes = make(map[string]applicationVolumeImage)
	}
	current.Volumes[volumeID] = applicationVolumeImage{CreatedAt: createdAt, Image: image}
	return s.saveLocked()
}

// ApplicationVolumeImage returns the image provenance recorded for one
// application volume only when the application's lifetime and volume
// incarnation still match.
func (s *Store) ApplicationVolumeImage(application, lifetime, volumeID, createdAt string) (ApplicationImage, bool) {
	binding, ok := s.ApplicationVolumeBinding(application, lifetime, volumeID)
	if !ok || binding.CreatedAt != createdAt || createdAt == "" {
		return ApplicationImage{}, false
	}
	return binding.Image, true
}

// ApplicationVolumeBinding returns the current stored binding for one volume.
// Callers that inspect the live volume should read it before and after that
// inspection and require the binding to remain unchanged.
func (s *Store) ApplicationVolumeBinding(application, lifetime, volumeID string) (ApplicationVolumeBinding, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.state.Applications[application]
	if current == nil || lifetime == "" || current.Lifetime != lifetime || volumeID == "" {
		return ApplicationVolumeBinding{}, false
	}
	observation, ok := current.Volumes[volumeID]
	if !ok || observation.CreatedAt == "" {
		return ApplicationVolumeBinding{}, false
	}
	return ApplicationVolumeBinding(observation), true
}

// ClearApplicationVolumeImage invalidates a volume binding before its contents
// or runtime object are changed.
func (s *Store) ClearApplicationVolumeImage(application, lifetime, volumeID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.state.Applications[application]
	if current == nil || lifetime == "" || current.Lifetime != lifetime || volumeID == "" {
		return s.saveIfDirtyLocked()
	}
	if _, ok := current.Volumes[volumeID]; !ok {
		return s.saveIfDirtyLocked()
	}
	delete(current.Volumes, volumeID)
	return s.saveLocked()
}

func aggregateApplicationResults(results map[string]v1beta1.DeviceDeltaApplyStatus) *v1beta1.DeviceDeltaApplyStatus {
	statuses := make([]v1beta1.DeviceDeltaApplyStatus, 0, len(results))
	for _, result := range results {
		statuses = append(statuses, result)
	}
	return AggregateApplicationResults(statuses)
}

// AggregateApplicationResults applies the shared precedence and fallback-reason policy.
func AggregateApplicationResults(results []v1beta1.DeviceDeltaApplyStatus) *v1beta1.DeviceDeltaApplyStatus {
	var applied, fallback, notUsed, notRequired int
	var reasons []string
	for _, result := range results {
		switch result.Outcome {
		case v1beta1.DeviceDeltaApplyOutcomeApplied:
			applied++
		case v1beta1.DeviceDeltaApplyOutcomeFallback:
			fallback++
			if result.FallbackReason != nil && *result.FallbackReason != "" {
				reasons = append(reasons, *result.FallbackReason)
			}
		case v1beta1.DeviceDeltaApplyOutcomeNotUsed:
			notUsed++
		case v1beta1.DeviceDeltaApplyOutcomeNotRequired:
			notRequired++
		default:
			return nil
		}
	}
	if applied+fallback+notUsed+notRequired == 0 {
		return nil
	}
	status := &v1beta1.DeviceDeltaApplyStatus{}
	switch {
	case applied > 0 && (fallback > 0 || notUsed > 0):
		status.Outcome = v1beta1.DeviceDeltaApplyOutcomePartial
	case applied > 0:
		status.Outcome = v1beta1.DeviceDeltaApplyOutcomeApplied
	case fallback > 0:
		status.Outcome = v1beta1.DeviceDeltaApplyOutcomeFallback
	case notUsed > 0:
		status.Outcome = v1beta1.DeviceDeltaApplyOutcomeNotUsed
	default:
		status.Outcome = v1beta1.DeviceDeltaApplyOutcomeNotRequired
	}
	if len(reasons) > 0 {
		slices.Sort(reasons)
		reason := reasons[0]
		status.FallbackReason = &reason
	}
	return status
}

// ClearApplicationResult invalidates an outcome when the active target's content changes.
// Invalidations for superseded specs or targets are ignored.
func (s *Store) ClearApplicationResult(application, specKey, targetID, targetKey string) error {
	return s.ClearApplicationResultForAttempt(ApplicationAttempt{Application: application, Lifetime: s.ApplicationLifetime(application), SpecKey: specKey, TargetID: targetID, TargetKey: targetKey})
}

// ClearApplicationResultForAttempt ignores invalidations from an obsolete
// application lifetime as well as from superseded desired targets.
func (s *Store) ClearApplicationResultForAttempt(attempt ApplicationAttempt) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.state.Applications[attempt.Application]
	if !attempt.matches(current) {
		return s.saveIfDirtyLocked()
	}
	if _, exists := current.Results[attempt.TargetID]; !exists {
		return s.saveIfDirtyLocked()
	}
	delete(current.Results, attempt.TargetID)
	return s.saveLocked()
}

func (s *Store) saveLocked() error {
	s.dirty = true
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
	s.dirty = false
	return nil
}

func (s *Store) saveIfDirtyLocked() error {
	if !s.dirty {
		return nil
	}
	return s.saveLocked()
}

func cloneStatus(status v1beta1.DeviceDeltaApplyStatus) v1beta1.DeviceDeltaApplyStatus {
	cloned := status
	if status.FallbackReason != nil {
		reason := *status.FallbackReason
		cloned.FallbackReason = &reason
	}
	return cloned
}
