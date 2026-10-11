package deltastatus

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"path/filepath"
	"slices"
	"sync"

	"github.com/containers/image/v5/docker/reference"
	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/opencontainers/go-digest"
)

const (
	FileName                               = "delta-status.json"
	fileMode                   fs.FileMode = 0o600
	stateVersion                           = 2
	applicationHistoryCapacity             = 8
	applicationContextCapacity             = 256
)

// Store owns bounded application digest histories and the independent OS outcome.
type Store struct {
	mu           sync.Mutex
	readWriter   fileio.ReadWriter
	path         string
	log          *log.PrefixLogger
	state        persistedState
	observations map[string]ApplicationObservation
}

type persistedState struct {
	Version       int                          `json:"version"`
	OS            *osState                     `json:"os,omitempty"`
	NextLifecycle uint64                       `json:"nextLifecycle"`
	Applications  map[string]*applicationState `json:"applications,omitempty"`
	Histories     []applicationHistory         `json:"histories,omitempty"` // least recently used first
}
type osState struct {
	TargetKey string                          `json:"targetKey"`
	Result    *v1beta1.DeviceDeltaApplyStatus `json:"result,omitempty"`
}
type applicationState struct {
	Lifecycle uint64 `json:"lifecycle"`
	SpecKey   string `json:"specKey"`
	Desired   bool   `json:"desired"`
}

// ApplicationContext identifies preparation and observation provenance.
type ApplicationContext struct {
	Application string `json:"application"`
	Lifecycle   uint64 `json:"lifecycle"`
	Reference   string `json:"reference"`
	Runtime     string `json:"runtime"`
	Owner       string `json:"owner"`
	Destination string `json:"destination"`
}
type applicationHistory struct {
	Context ApplicationContext  `json:"context"`
	Entries []ApplicationResult `json:"entries"` // least recently used first
}
type ApplicationResult struct {
	Digest string                         `json:"digest"`
	Status v1beta1.DeviceDeltaApplyStatus `json:"status"`
}
type ApplicationSnapshot struct {
	Lifecycle uint64
	Histories map[ApplicationContext][]ApplicationResult
}
type ApplicationLookup struct {
	Context ApplicationContext
	Digest  string
}
type ApplicationLookupResult struct {
	Status *v1beta1.DeviceDeltaApplyStatus
}

// ApplicationObservation is private runtime ownership evidence for one status tick.
type ApplicationObservation struct {
	Application string
	Runtime     string
	Owner       string
	Destination string
	Complete    bool
}

func NewInMemory(logger *log.PrefixLogger) *Store {
	return &Store{log: logger, state: persistedState{Version: stateVersion, Applications: make(map[string]*applicationState)}, observations: make(map[string]ApplicationObservation)}
}

// New explicitly migrates version 1: OS state survives, unsafe application identities do not.
func New(readWriter fileio.ReadWriter, dataDir string, logger *log.PrefixLogger) *Store {
	s := NewInMemory(logger)
	s.readWriter = readWriter
	s.path = filepath.Join(dataDir, FileName)
	data, err := readWriter.ReadFile(s.path)
	if err != nil {
		if !fileio.IsNotExist(err) {
			logger.Warnf("Failed to read persisted delta status: %v", err)
		}
		return s
	}
	var state persistedState
	if err = json.Unmarshal(data, &state); err != nil {
		logger.Warnf("Ignoring invalid persisted delta status: %v", err)
		return s
	}
	if state.Version == 1 {
		s.state.OS = state.OS
		if err = s.saveLocked(); err != nil {
			logger.Warnf("Failed to persist migrated delta status: %v", err)
		}
		return s
	}
	if state.Version != stateVersion {
		logger.Warnf("Ignoring persisted delta status with unsupported version %d", state.Version)
		return s
	}
	if state.Applications == nil {
		state.Applications = make(map[string]*applicationState)
	}
	for app, a := range state.Applications {
		if app == "" || a == nil || a.Lifecycle == 0 {
			delete(state.Applications, app)
			continue
		}
		if a.Lifecycle > state.NextLifecycle {
			state.NextLifecycle = a.Lifecycle
		}
	}
	histories := state.Histories
	state.Histories = nil
	for _, h := range histories {
		a := state.Applications[h.Context.Application]
		if !validContext(h.Context) || a == nil || a.Lifecycle != h.Context.Lifecycle {
			continue
		}
		index := slices.IndexFunc(state.Histories, func(existing applicationHistory) bool { return existing.Context == h.Context })
		if index >= 0 {
			h.Entries = append(state.Histories[index].Entries, h.Entries...)
			state.Histories = slices.Delete(state.Histories, index, index+1)
		}
		entries := h.Entries
		h.Entries = nil
		for _, entry := range entries {
			if !ValidDigest(entry.Digest) || !validOutcome(entry.Status.Outcome) {
				continue
			}
			i := slices.IndexFunc(h.Entries, func(e ApplicationResult) bool { return e.Digest == entry.Digest })
			if i >= 0 {
				h.Entries = slices.Delete(h.Entries, i, i+1)
			}
			entry.Status = cloneStatus(entry.Status)
			h.Entries = append(h.Entries, entry)
		}
		if len(h.Entries) > applicationHistoryCapacity {
			h.Entries = h.Entries[len(h.Entries)-applicationHistoryCapacity:]
		}
		if len(h.Entries) > 0 {
			state.Histories = append(state.Histories, h)
		}
	}
	if len(state.Histories) > applicationContextCapacity {
		state.Histories = state.Histories[len(state.Histories)-applicationContextCapacity:]
	}
	s.state = state
	return s
}

func ValidDigest(value string) bool {
	d, err := digest.Parse(value)
	return err == nil && d.Validate() == nil
}
func NormalizeReference(value string) string {
	named, err := reference.ParseNormalizedNamed(value)
	if err != nil {
		return ""
	}
	return reference.TagNameOnly(named).String()
}
func validContext(c ApplicationContext) bool {
	return c.Application != "" && c.Lifecycle > 0 && c.Reference != "" && NormalizeReference(c.Reference) == c.Reference && (c.Runtime == "podman" || c.Runtime == "cri") && c.Owner != "" && c.Destination != ""
}
func validOutcome(o v1beta1.DeviceDeltaApplyOutcomeType) bool {
	return o == v1beta1.DeviceDeltaApplyOutcomeApplied || o == v1beta1.DeviceDeltaApplyOutcomeFallback || o == v1beta1.DeviceDeltaApplyOutcomeNotUsed
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

// ReconcileApplicationSpecs tracks execution generations without discarding history.
// Absence from desired state never confirms runtime removal.
func (s *Store) ReconcileApplicationSpecs(specKeys map[string]string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	changed := false
	for app, current := range s.state.Applications {
		_, desired := specKeys[app]
		if current.Desired != desired {
			current.Desired = desired
			changed = true
		}
	}

	for app, key := range specKeys {
		if app == "" {
			continue
		}
		a := s.state.Applications[app]
		if a == nil {
			s.state.NextLifecycle++
			a = &applicationState{Lifecycle: s.state.NextLifecycle, Desired: true}
			s.state.Applications[app] = a
			changed = true
		}
		if a.SpecKey != key {
			a.SpecKey = key
			changed = true
		}
	}
	if changed {
		return s.saveLocked()
	}
	return nil
}

// Context resolves lifecycle ownership without consulting desired image identities.
func (s *Store) Context(application, ref, runtime, owner, destination string) (ApplicationContext, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.state.Applications[application]
	if a == nil {
		return ApplicationContext{}, false
	}
	c := ApplicationContext{Application: application, Lifecycle: a.Lifecycle, Reference: NormalizeReference(ref), Runtime: runtime, Owner: owner, Destination: destination}
	return c, validContext(c)
}
func (s *Store) SetApplicationObservations(observations map[string]ApplicationObservation) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.observations = make(map[string]ApplicationObservation, len(observations))
	for app, o := range observations {
		s.observations[app] = o
	}
}

// ObservationKey identifies an actual reported image pair without adding user API fields.
func ObservationKey(application, image, digest string) string {
	return Fingerprint(application, NormalizeReference(image), digest)
}
func (s *Store) ApplicationObservation(application, image, digest string) (ApplicationObservation, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.observations[ObservationKey(application, image, digest)]
	return o, ok
}

// RecordApplicationResult inserts only proven final prepared digests. Caller guards stale executions.
func (s *Store) RecordApplicationResult(c ApplicationContext, finalDigest string, result v1beta1.DeviceDeltaApplyStatus) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.state.Applications[c.Application]
	if !validContext(c) || !ValidDigest(finalDigest) || !validOutcome(result.Outcome) || a == nil || a.Lifecycle != c.Lifecycle {
		return nil
	}
	i := slices.IndexFunc(s.state.Histories, func(h applicationHistory) bool { return h.Context == c })
	h := applicationHistory{Context: c}
	if i >= 0 {
		h = s.state.Histories[i]
		s.state.Histories = slices.Delete(s.state.Histories, i, i+1)
	}
	j := slices.IndexFunc(h.Entries, func(e ApplicationResult) bool { return e.Digest == finalDigest })
	if j >= 0 {
		h.Entries = slices.Delete(h.Entries, j, j+1)
	}
	h.Entries = append(h.Entries, ApplicationResult{Digest: finalDigest, Status: cloneStatus(result)})
	if len(h.Entries) > applicationHistoryCapacity {
		h.Entries = slices.Delete(h.Entries, 0, 1)
	}
	s.state.Histories = append(s.state.Histories, h)
	if len(s.state.Histories) > applicationContextCapacity {
		s.state.Histories = slices.Delete(s.state.Histories, 0, 1)
	}
	return s.saveLocked()
}

// LookupApplicationResults touches hits in batch. Misses never insert or invalidate history.
func (s *Store) LookupApplicationResults(lookups []ApplicationLookup) ([]ApplicationLookupResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	results := make([]ApplicationLookupResult, len(lookups))
	if len(lookups) == 0 {
		return results, nil
	}
	before := make([]applicationHistory, len(s.state.Histories))
	for i, h := range s.state.Histories {
		before[i] = applicationHistory{Context: h.Context, Entries: slices.Clone(h.Entries)}
	}
	changed := false
	for n, lookup := range lookups {
		if !validContext(lookup.Context) || !ValidDigest(lookup.Digest) {
			continue
		}
		i := slices.IndexFunc(s.state.Histories, func(h applicationHistory) bool { return h.Context == lookup.Context })
		if i < 0 {
			continue
		}
		h := s.state.Histories[i]
		j := slices.IndexFunc(h.Entries, func(e ApplicationResult) bool { return e.Digest == lookup.Digest })
		if j < 0 {
			continue
		}
		entry := h.Entries[j]
		cloned := cloneStatus(entry.Status)
		results[n].Status = &cloned
		if j != len(h.Entries)-1 {
			h.Entries = append(slices.Delete(h.Entries, j, j+1), entry)
			changed = true
		}
		if i != len(s.state.Histories)-1 {
			s.state.Histories = append(slices.Delete(s.state.Histories, i, i+1), h)
			changed = true
		} else {
			s.state.Histories[i] = h
		}
	}
	if changed {
		changed = !slices.EqualFunc(before, s.state.Histories, func(a, b applicationHistory) bool {
			return a.Context == b.Context && slices.EqualFunc(a.Entries, b.Entries, func(x, y ApplicationResult) bool { return x.Digest == y.Digest })
		})
	}
	if changed {
		return results, s.saveLocked()
	}
	return results, nil
}

// ApplicationLifecycle returns the current persisted ownership generation.
func (s *Store) ApplicationLifecycle(application string) uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if a := s.state.Applications[application]; a != nil {
		return a.Lifecycle
	}
	return 0
}

// RetireApplicationContexts is called only after successful runtime lifecycle removal.
func (s *Store) RetireApplicationContexts(application string, lifecycle uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if lifecycle == 0 || s.state.Applications[application] == nil || s.state.Applications[application].Lifecycle != lifecycle || s.state.Applications[application].Desired {
		return nil
	}
	delete(s.state.Applications, application)
	for key, o := range s.observations {
		if o.Application == application {
			delete(s.observations, key)
		}
	}
	s.state.Histories = slices.DeleteFunc(s.state.Histories, func(h applicationHistory) bool { return h.Context.Application == application })
	return s.saveLocked()
}
func (s *Store) ApplicationSnapshot(application string) *ApplicationSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.state.Applications[application]
	if a == nil {
		return nil
	}
	result := &ApplicationSnapshot{Lifecycle: a.Lifecycle, Histories: make(map[ApplicationContext][]ApplicationResult)}
	for _, h := range s.state.Histories {
		if h.Context.Application != application {
			continue
		}
		entries := slices.Clone(h.Entries)
		for i := range entries {
			entries[i].Status = cloneStatus(entries[i].Status)
		}
		result.Histories[h.Context] = entries
	}
	return result
}
func (s *Store) saveLocked() error {
	if s.readWriter == nil {
		return nil
	}
	data, err := json.Marshal(s.state)
	if err != nil {
		return fmt.Errorf("marshal persisted delta status: %w", err)
	}
	if err = s.readWriter.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("create delta status directory: %w", err)
	}
	if err = s.readWriter.WriteFile(s.path, data, fileMode); err != nil {
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
