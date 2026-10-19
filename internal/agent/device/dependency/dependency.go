package dependency

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"iter"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/containers/image/v5/docker/reference"
	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/delta"
	"github.com/flightctl/flightctl/internal/agent/device/deltastatus"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/agent/device/resource"
	"github.com/flightctl/flightctl/internal/agent/device/status"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/poll"
	"github.com/opencontainers/go-digest"
)

const (
	maxQueueSize = 10
)

const (
	ociImageConfigMediaType    = "application/vnd.oci.image.config.v1+json"
	dockerImageConfigMediaType = "application/vnd.docker.container.image.v1+json"
)

// OCIType represents the type of OCI target for prefetching
type OCIType string

const (
	OCITypePodmanImage    OCIType = "PodmanImage"
	OCITypeCRIImage       OCIType = "CRIImage"
	OCITypePodmanArtifact OCIType = "PodmanArtifact"
	OCITypeAuto           OCIType = "Auto"
	OCITypeHelmChart      OCIType = "HelmChart"
)

// detectOCIType analyzes an OCI manifest to determine if it's an image or artifact
// Returns Podman-specific types as the default runtime
func detectOCIType(manifest *client.OCIManifest) (OCIType, error) {
	if manifest == nil {
		return "", fmt.Errorf("manifest is nil")
	}

	if len(manifest.Manifests) > 0 {
		return OCITypePodmanImage, nil
	}

	if manifest.ArtifactType != "" {
		return OCITypePodmanArtifact, nil
	}

	if manifest.Config != nil {
		switch manifest.Config.MediaType {
		case ociImageConfigMediaType, dockerImageConfigMediaType:
			return OCITypePodmanImage, nil
		case "":
			return "", fmt.Errorf("media type not set")
		default:
			// artifact types could be anything, so default to artifact
			// if there is any value here that isn't the default image type
			return OCITypePodmanArtifact, nil
		}
	}

	return OCITypePodmanImage, nil
}

// ClientOptsFn is a function that lazily resolves client options.
// This allows deferring config resolution until a pull is actually needed,
// avoiding disk I/O in steady state when all images are already present.
type ClientOptsFn func() []client.ClientOption

// OCIDeltaTarget describes an optional delta transition for an OCI target.
type OCIDeltaTarget struct {
	// Package marks non-workload packages, such as Compose bundles and Helm
	// charts, whose normal preparation contributes neutral NotRequired.
	Package      bool
	Hint         string
	SourceDigest string
	Application  string   // application supplying Hint and SourceDigest
	Applications []string // applications sharing this image target
}

func mergeOCIDeltaTargets(first, second *OCIDeltaTarget) *OCIDeltaTarget {
	if first == nil {
		return cloneOCIDeltaTarget(second)
	}
	if second == nil {
		return cloneOCIDeltaTarget(first)
	}

	selected := first
	if deltaTargetLess(second, first) {
		selected = second
	}
	merged := *selected
	merged.Applications = append(deltaApplications(first), deltaApplications(second)...)
	merged.Package = first.Package && second.Package
	slices.Sort(merged.Applications)
	merged.Applications = slices.Compact(merged.Applications)
	return &merged
}

func cloneOCIDeltaTarget(target *OCIDeltaTarget) *OCIDeltaTarget {
	if target == nil {
		return nil
	}
	cloned := *target
	cloned.Applications = deltaApplications(target)
	return &cloned
}

func deltaApplications(target *OCIDeltaTarget) []string {
	if target == nil {
		return nil
	}
	applications := slices.Clone(target.Applications)
	if target.Application != "" {
		applications = append(applications, target.Application)
	}
	slices.Sort(applications)
	applications = slices.Compact(applications)
	return applications
}

func hasApplicationDeltaContext(target *OCIDeltaTarget) bool {
	return target != nil && (target.Hint != "" || target.SourceDigest != "")
}

// Shared image refs use one prefetch task. Prefer explicit hints, then choose
// by hint, source digest, and application name to make candidate selection stable.
func deltaTargetLess(candidate, current *OCIDeltaTarget) bool {
	if (candidate.Hint != "") != (current.Hint != "") {
		return candidate.Hint != ""
	}
	if candidate.Hint != current.Hint {
		return candidate.Hint < current.Hint
	}
	if candidate.SourceDigest != current.SourceDigest {
		return candidate.SourceDigest < current.SourceDigest
	}
	return candidate.Application < current.Application
}

func normalizeDigest(digest string) string {
	digest = strings.TrimSpace(digest)
	if digest == "" || strings.Contains(digest, ":") {
		return digest
	}
	return "sha256:" + digest
}

// OCIPullTarget represents an OCI target to be prefetched
type OCIPullTarget struct {
	Type         OCIType
	Reference    string
	Digest       string
	PullPolicy   v1beta1.ImagePullPolicy
	ClientOptsFn ClientOptsFn // Resolved when pulling the target or inspecting its registry digest
	Delta        *OCIDeltaTarget
}

// A set of OCIPullTargets grouped by the user that will use the targets (blank Username is root).
type OCIPullTargetsByUser map[v1beta1.Username][]OCIPullTarget

func (o OCIPullTargetsByUser) Add(user v1beta1.Username, targets ...OCIPullTarget) OCIPullTargetsByUser {
	if o == nil {
		o = make(map[v1beta1.Username][]OCIPullTarget, len(targets))
	}
	o[user] = append(o[user], targets...)
	return o
}

// MergeWith o2 in-place.
func (o OCIPullTargetsByUser) MergeWith(o2 OCIPullTargetsByUser) OCIPullTargetsByUser {
	if o == nil {
		o = make(OCIPullTargetsByUser, len(o2))
	}
	for u, v := range o2 {
		o[u] = append(o[u], v...)
	}
	return o
}

func (o OCIPullTargetsByUser) Iter() iter.Seq2[v1beta1.Username, OCIPullTarget] {
	return func(yield func(k v1beta1.Username, v OCIPullTarget) bool) {
		for user, targets := range o {
			for _, t := range targets {
				if !yield(user, t) {
					return
				}
			}
		}
	}
}

// OCICollection represents the result of collecting OCI targets
type OCICollection struct {
	Targets OCIPullTargetsByUser
	Requeue bool // true if collection is incomplete and should be retried
}

// PrefetchStatus provides the current status of prefetch operations
type PrefetchStatus struct {
	TotalImages        int
	PendingImages      []string
	RetryingImages     []string
	DeltaApplyStatuses map[string]v1beta1.DeviceDeltaApplyStatus
}

type applicationDeltaApplyResult struct {
	outcome        v1beta1.DeviceDeltaApplyOutcomeType
	fallbackReason string
	targetKey      string
}

type applicationImageInspection struct {
	currentDigests  []string
	runtimeImageRef string
	podmanMetadata  *client.PodmanImageMetadata
}

type applicationDeltaPersistence struct {
	application        string
	target             imageRef
	targetID           string
	task               *prefetchTask
	persistedTargetKey string
	status             v1beta1.DeviceDeltaApplyStatus
}

var _ PrefetchManager = (*prefetchManager)(nil)
var _ status.Exporter = (*prefetchManager)(nil)

// PrefetchManager orchestrates OCI target collection and prefetching
type PrefetchManager interface {
	// RegisterOCICollector registers a function that can collect OCI targets from a device spec
	RegisterOCICollector(collector OCICollector)
	// BeforeUpdate collects and prefetches OCI targets from all registered collectors
	BeforeUpdate(ctx context.Context, current, desired *v1beta1.DeviceSpec, opts ...OCICollectOpt) error
	// StatusMessage returns a human readable prefetch progress status message
	StatusMessage(ctx context.Context) string
	// Cleanup fires all cleanupFn cancels active pulls and drains the queue
	Cleanup()
}

// OCICollectOpt configures OCI target collection behavior.
type OCICollectOpt func(*ociCollectOpts)

type ociCollectOpts struct {
	osUpdatePending bool
}

// WithOSUpdatePending indicates an OS update is pending (not yet booted).
func WithOSUpdatePending(pending bool) OCICollectOpt {
	return func(o *ociCollectOpts) {
		o.osUpdatePending = pending
	}
}

// ApplyOCICollectOpts applies the given options and returns the configured options struct.
func ApplyOCICollectOpts(opts ...OCICollectOpt) ociCollectOpts {
	var o ociCollectOpts
	for _, opt := range opts {
		opt(&o)
	}
	return o
}

// OSUpdatePending returns true if an OS update is pending.
func (o ociCollectOpts) OSUpdatePending() bool {
	return o.osUpdatePending
}

// OCICollector interface for components that can collect OCI targets
type OCICollector interface {
	// CollectOCITargets collects OCI targets and indicates if requeue is needed
	CollectOCITargets(ctx context.Context, current, desired *v1beta1.DeviceSpec, opts ...OCICollectOpt) (*OCICollection, error)
}

type imageRef struct {
	image   string
	owner   v1beta1.Username
	ociType OCIType
}

func (i imageRef) targetID() string {
	return deltastatus.Fingerprint(string(i.owner), i.image, string(i.ociType))
}

func (i imageRef) String() string {
	if i.owner != "" {
		return fmt.Sprintf("%s (owned by %s)", i.image, i.owner)
	}
	return i.image
}

type prefetchManager struct {
	log               *log.PrefixLogger
	podmanFactory     client.PodmanFactory
	skopeoFactory     client.SkopeoFactory
	cliClients        client.CLIClients
	ociDelta          *client.OCIDelta
	ociDeltaFactory   client.OCIDeltaFactory
	readWriter        fileio.ReadWriter
	readWriterFactory fileio.ReadWriterFactory
	resourceManager   resource.Manager
	// pullTimeout is the duration that each target will wait unless it
	// encounters an error
	pullTimeout time.Duration
	pollConfig  *poll.Config

	mu         sync.Mutex
	tasks      map[imageRef]*prefetchTask
	queue      chan imageRef
	collectors []OCICollector

	deltaDesiredFingerprint string
	deltaGeneration         uint64
	deltaApplyResults       map[string]map[imageRef]applicationDeltaApplyResult
	deltaStatusStore        *deltastatus.Store
	applicationNameResolver ApplicationNameResolver
	deltaAppSpecKeys        map[string]string
	deltaAppLifetimes       map[string]string
	deltaAppTargetKeys      map[string]map[string]string
	deltaTargetRefs         map[string]imageRef
	deltaTargetsScheduled   bool
}

type prefetchTask struct {
	operationTask        *prefetchTask // Registered task whose immutable operation this snapshot describes.
	clientOptsFn         ClientOptsFn
	ociType              OCIType
	resolvedOCIType      OCIType
	pullPolicy           v1beta1.ImagePullPolicy
	targetDigest         string
	resolvedDigest       string
	applicationTargetKey string
	applicationAttempts  map[string]deltastatus.ApplicationAttempt
	targetPresent        bool
	delta                *OCIDeltaTarget
	deltaGeneration      uint64
	err                  error
	done                 bool
	cancelFn             context.CancelFunc
}

// snapshotOperationLocked freezes all recording identity before external work.
// A registered task may be reused by reconciliation, but its operation cannot
// inherit a later generation, target key, or list of applications.
func snapshotOperationLocked(task *prefetchTask) *prefetchTask {
	if task.operationTask != nil {
		return task
	}
	snapshot := *task
	snapshot.operationTask = task
	snapshot.applicationAttempts = maps.Clone(task.applicationAttempts)
	if task.delta != nil {
		delta := *task.delta
		delta.Applications = slices.Clone(task.delta.Applications)
		snapshot.delta = &delta
	}
	return &snapshot
}

func (m *prefetchManager) snapshotOperation(task *prefetchTask) *prefetchTask {
	m.mu.Lock()
	defer m.mu.Unlock()
	return snapshotOperationLocked(task)
}

func (m *prefetchManager) currentOperationLocked(target imageRef, task *prefetchTask) bool {
	if task == nil {
		return false
	}
	registered := task
	if task.operationTask != nil {
		registered = task.operationTask
	}
	return m.tasks[target] == registered && task.deltaGeneration == m.deltaGeneration && registered.deltaGeneration == task.deltaGeneration && registered.applicationTargetKey == task.applicationTargetKey
}

type digestCheckResult int

const (
	digestUnknown digestCheckResult = iota
	digestMatched
	digestMismatched
)

// PrefetchManagerOption configures optional prefetch integrations.
type PrefetchManagerOption func(*prefetchManager)

// ApplicationNameResolver resolves the same stable name used by application
// OCI collectors and application status reporting.
type ApplicationNameResolver func(*v1beta1.ApplicationProviderSpec) (string, error)

// WithOCIDelta enables application image delta reconstruction during prefetch.
func WithOCIDelta(ociDelta *client.OCIDelta) PrefetchManagerOption {
	return func(m *prefetchManager) { m.ociDelta = ociDelta }
}

// WithOCIDeltaFactory creates OCI delta clients that use each application's
// Podman storage owner.
func WithOCIDeltaFactory(factory client.OCIDeltaFactory) PrefetchManagerOption {
	return func(m *prefetchManager) { m.ociDeltaFactory = factory }
}

// WithReadWriterFactory provides per-user temporary workspaces for application
// delta artifacts.
func WithReadWriterFactory(factory fileio.ReadWriterFactory) PrefetchManagerOption {
	return func(m *prefetchManager) { m.readWriterFactory = factory }
}

// WithDeltaStatusStore persists the last result for each application image target.
func WithDeltaStatusStore(store *deltastatus.Store) PrefetchManagerOption {
	return func(m *prefetchManager) { m.deltaStatusStore = store }
}

// WithApplicationNameResolver resolves names for application specs that omit
// an explicit name, keeping persisted delta results aligned with app status.
func WithApplicationNameResolver(resolve ApplicationNameResolver) PrefetchManagerOption {
	return func(m *prefetchManager) { m.applicationNameResolver = resolve }
}

// NewPrefetchManager creates a new prefetch manager instance
// TODO: Consider extending cliClients to include podman and skopeo in the future
func NewPrefetchManager(
	log *log.PrefixLogger,
	podmanFactory client.PodmanFactory,
	skopeoFactory client.SkopeoFactory,
	cliClients client.CLIClients,
	readWriter fileio.ReadWriter,
	pullTimeout util.Duration,
	resourceManager resource.Manager,
	pollConfig poll.Config,
	opts ...PrefetchManagerOption,
) *prefetchManager {
	m := &prefetchManager{
		log:               log,
		podmanFactory:     podmanFactory,
		skopeoFactory:     skopeoFactory,
		cliClients:        cliClients,
		readWriter:        readWriter,
		pullTimeout:       time.Duration(pullTimeout),
		pollConfig:        &pollConfig,
		resourceManager:   resourceManager,
		tasks:             make(map[imageRef]*prefetchTask),
		queue:             make(chan imageRef, maxQueueSize),
		deltaApplyResults: make(map[string]map[imageRef]applicationDeltaApplyResult),
	}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

func (m *prefetchManager) Run(ctx context.Context) {
	m.log.Debug("Prefetch manager started")
	defer m.log.Debug("Prefetch manager stopped")

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		m.worker(ctx)
	}()

	wg.Wait()
}

func (m *prefetchManager) worker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			m.log.Debugf("Prefetch worker exiting: %v", ctx.Err())
			return

		case ref := <-m.queue:
			m.processTarget(ctx, ref)
		}
	}
}

func (m *prefetchManager) RegisterOCICollector(collector OCICollector) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if slices.Contains(m.collectors, collector) {
		return
	}
	m.collectors = append(m.collectors, collector)
}

// isTargetsChanged checks if targets changed using pre-built reference set
// caller must hold m.mu lock
func (m *prefetchManager) isTargetsChanged(seenTargets map[imageRef]struct{}) bool {
	if len(m.tasks) == 0 {
		return len(seenTargets) > 0
	}

	if len(seenTargets) != len(m.tasks) {
		return true
	}

	for existingRef := range m.tasks {
		if _, exists := seenTargets[existingRef]; !exists {
			return true
		}
	}

	return false
}

func (m *prefetchManager) BeforeUpdate(ctx context.Context, current, desired *v1beta1.DeviceSpec, opts ...OCICollectOpt) error {
	m.log.Debug("Collecting OCI targets from all dependency sources")
	desiredFingerprint := ""
	var applications *[]v1beta1.ApplicationProviderSpec
	if desired != nil {
		applications = desired.Applications
	}
	if desiredJSON, err := json.Marshal(applications); err != nil {
		m.log.Warnf("Failed to fingerprint desired device spec for delta prefetch generation: %v", err)
	} else {
		// Desired specs are read again for each reconciliation, so pointer identity
		// would make every retry look like a new delta generation.
		desiredFingerprint = deltastatus.Fingerprint(string(desiredJSON))
	}
	appSpecKeys := make(map[string]string)
	appLifetimes := make(map[string]string)
	canPersistDeltaStatus := m.deltaStatusStore != nil
	if m.deltaStatusStore != nil {
		var err error
		appSpecKeys, err = applicationDeltaSpecKeys(desired, m.applicationNameResolver)
		if err != nil {
			m.log.Warnf("Failed to fingerprint application delta targets: %v", err)
			appSpecKeys = make(map[string]string)
			canPersistDeltaStatus = false
		}
		if canPersistDeltaStatus {
			if err := m.deltaStatusStore.ReconcileApplicationSpecs(appSpecKeys); err != nil {
				m.log.Warnf("Failed to reconcile persisted application delta specs: %v", err)
				canPersistDeltaStatus = false
			} else {
				for application := range appSpecKeys {
					if lifetime := m.deltaStatusStore.ApplicationLifetime(application); lifetime != "" {
						appLifetimes[application] = lifetime
					}
				}
			}
		}
	}

	allTargets := make(OCIPullTargetsByUser)
	var requeueNeeded bool
	m.mu.Lock()
	desiredChanged := desiredFingerprint != "" && m.deltaDesiredFingerprint != desiredFingerprint
	if desiredChanged || !sameStringMap(m.deltaAppSpecKeys, appSpecKeys) {
		if desiredFingerprint != "" {
			m.deltaDesiredFingerprint = desiredFingerprint
		}
		m.deltaGeneration++
		m.deltaApplyResults = make(map[string]map[imageRef]applicationDeltaApplyResult)
	}
	m.deltaAppSpecKeys = appSpecKeys
	m.deltaAppLifetimes = appLifetimes
	m.deltaTargetsScheduled = false
	collectors := slices.Clone(m.collectors)
	m.mu.Unlock()

	for i, collector := range collectors {
		result, err := collector.CollectOCITargets(ctx, current, desired, opts...)
		if err != nil {
			return fmt.Errorf("%w %d failed: %w", errors.ErrPrefetchCollector, i, err)
		}
		allTargets = allTargets.MergeWith(result.Targets)
		if result.Requeue {
			requeueNeeded = true
		}
	}

	targetsByRef := make(map[imageRef]OCIPullTarget)
	for user, target := range allTargets.Iter() {
		ref := imageRef{
			image:   target.Reference,
			owner:   user,
			ociType: target.Type,
		}
		if existing, seen := targetsByRef[ref]; seen {
			existing.Delta = mergeOCIDeltaTargets(existing.Delta, target.Delta)
			targetsByRef[ref] = existing
			continue
		}
		targetsByRef[ref] = target
	}
	appTargetKeys, targetRefs := applicationTargetIdentities(targetsByRef)
	if canPersistDeltaStatus {
		workloadTargets := maps.Clone(targetsByRef)
		maps.DeleteFunc(workloadTargets, func(_ imageRef, target OCIPullTarget) bool {
			return target.Delta != nil && target.Delta.Package
		})
		workloadKeys, _ := applicationTargetIdentities(workloadTargets)
		if err := m.deltaStatusStore.ReconcileApplicationTargets(workloadKeys, !requeueNeeded); err != nil {
			m.log.Warnf("Failed to reconcile persisted application delta targets: %v", err)
		}
	}

	seenTargets := make(map[imageRef]struct{}, len(targetsByRef))
	var newTargets OCIPullTargetsByUser
	for ref, target := range targetsByRef {
		newTargets = newTargets.Add(ref.owner, target)
		seenTargets[ref] = struct{}{}
	}

	// Restore completed image results before scheduling. If the image is already
	// present, the synthetic NotUsed result from prepareTask must not erase the
	// result persisted for this same target.
	var restoredResults map[string]map[imageRef]applicationDeltaApplyResult
	if canPersistDeltaStatus {
		restoredResults = m.restoreApplicationDeltaResults(appTargetKeys, targetRefs)
	}
	m.mu.Lock()
	// Keep both indexes on the same collection so status can resolve results
	// directly without scanning image references and recomputing fingerprints.
	m.deltaAppTargetKeys = appTargetKeys
	m.deltaTargetRefs = targetRefs
	m.deltaApplyResults = mergeApplicationDeltaResults(m.deltaApplyResults, restoredResults)
	m.mu.Unlock()

	m.log.Debugf("Collected %d unique OCI targets", len(seenTargets))

	// clean up stale prefetch tasks if targets have changed
	m.mu.Lock()
	if m.isTargetsChanged(seenTargets) || m.hasChangedTasks(targetsByRef) {
		m.log.Debug("OCI targets changed, cleaning up stale prefetch tasks")
	}
	m.cleanupStaleTasks(seenTargets)
	m.cleanupChangedTasks(targetsByRef)
	m.mu.Unlock()

	if len(newTargets) > 0 {
		if m.resourceManager.IsCriticalAlert(resource.DiskMonitorType) {
			return fmt.Errorf("%w: %w: insufficient disk storage space, please clear storage",
				errors.WithElement("Disk"), errors.ErrCriticalResourceAlert)
		}
		m.log.Debugf("Scheduling %d new targets for prefetch", len(newTargets))
		if err := m.Schedule(ctx, newTargets); err != nil {
			return fmt.Errorf("%w: %w", errors.ErrSchedulingPrefetchTargets, err)
		}
	}

	// Hold application delta status until every target from this collection has
	// been registered. Prefetch workers can finish targets while Schedule is
	// still adding later targets, so exposing results before it returns can
	// report a misleading Applied outcome for only a subset of the application.
	m.mu.Lock()
	m.deltaTargetsScheduled = !requeueNeeded
	m.mu.Unlock()

	if err := m.checkReady(ctx); err != nil {
		return err
	}

	// collector requested a requeue, return retryable error to trigger another iteration
	if requeueNeeded {
		m.log.Debug("Requeue requested by collector, will retry after current targets are fetched")
		return errors.ErrPrefetchNotReady
	}

	return nil
}

func applicationDeltaSpecKeys(desired *v1beta1.DeviceSpec, resolveName ApplicationNameResolver) (map[string]string, error) {
	keys := make(map[string]string)
	if desired == nil || desired.Applications == nil {
		return keys, nil
	}
	for i, application := range *desired.Applications {
		var name string
		var err error
		if resolveName != nil {
			name, err = resolveName(&application)
		} else {
			var explicitName *string
			explicitName, err = application.GetName()
			if explicitName != nil {
				name = *explicitName
			}
		}
		if err != nil {
			return nil, fmt.Errorf("resolve application %d name: %w", i, err)
		}
		if name == "" {
			return nil, fmt.Errorf("application %d has no name", i)
		}
		data, err := application.MarshalJSON()
		if err != nil {
			return nil, fmt.Errorf("marshal application %s: %w", name, err)
		}
		var normalizedFields map[string]json.RawMessage
		if err := json.Unmarshal(data, &normalizedFields); err != nil {
			return nil, fmt.Errorf("decode application %s: %w", name, err)
		}
		for _, key := range []string{"desiredState", "restartGeneration", "deltaImage", "deltaImages"} {
			delete(normalizedFields, key)
		}
		normalized, err := json.Marshal(normalizedFields)
		if err != nil {
			return nil, fmt.Errorf("normalize application %s: %w", name, err)
		}
		keys[name] = deltastatus.Fingerprint(string(normalized))
	}
	return keys, nil
}

func applicationTargetIdentities(targets map[imageRef]OCIPullTarget) (map[string]map[string]string, map[string]imageRef) {
	applicationTargets := make(map[string]map[string]string)
	targetRefs := make(map[string]imageRef)
	for ref, target := range targets {
		applications := deltaApplications(target.Delta)
		if len(applications) == 0 {
			continue
		}
		targetID := ref.targetID()
		targetKey := applicationImageTargetKey(ref, target)
		targetRefs[targetID] = ref
		for _, application := range applications {
			if applicationTargets[application] == nil {
				applicationTargets[application] = make(map[string]string)
			}
			applicationTargets[application][targetID] = targetKey
		}
	}
	return applicationTargets, targetRefs
}

func applicationImageTargetKey(ref imageRef, target OCIPullTarget) string {
	return applicationImageTargetKeyFor(ref, target.Digest, target.Type)
}

func applicationImageTargetKeyFor(ref imageRef, digest string, ociType OCIType) string {
	return deltastatus.Fingerprint(string(ref.owner), ref.image, digest, string(ociType))
}

func (m *prefetchManager) applicationAttemptsLocked(target imageRef, targetKey string, delta *OCIDeltaTarget) map[string]deltastatus.ApplicationAttempt {
	applications := deltaApplications(delta)
	if len(applications) == 0 {
		return nil
	}
	attempts := make(map[string]deltastatus.ApplicationAttempt, len(applications))
	for _, application := range applications {
		attempts[application] = deltastatus.ApplicationAttempt{Application: application,
			Lifetime: m.deltaAppLifetimes[application], SpecKey: m.deltaAppSpecKeys[application],
			TargetID: target.targetID(), TargetKey: targetKey}
	}
	return attempts
}

func (m *prefetchManager) restoreApplicationDeltaResults(
	applicationTargets map[string]map[string]string,
	targetRefs map[string]imageRef,
) map[string]map[imageRef]applicationDeltaApplyResult {
	results := make(map[string]map[imageRef]applicationDeltaApplyResult)
	if m.deltaStatusStore == nil {
		return results
	}
	for application, targets := range applicationTargets {
		snapshot := m.deltaStatusStore.ApplicationSnapshot(application)
		if snapshot == nil || snapshot.SpecKey != m.deltaAppSpecKeys[application] || snapshot.Lifetime != m.deltaAppLifetimes[application] {
			continue
		}
		for targetID, targetKey := range targets {
			if snapshot.Targets[targetID] != targetKey {
				continue
			}
			persisted, exists := snapshot.Results[targetID]
			if !exists || persisted.TargetKey != targetKey {
				continue
			}
			target, exists := targetRefs[targetID]
			if !exists {
				continue
			}
			if results[application] == nil {
				results[application] = make(map[imageRef]applicationDeltaApplyResult)
			}
			fallbackReason := ""
			if persisted.Status.FallbackReason != nil {
				fallbackReason = *persisted.Status.FallbackReason
			}
			results[application][target] = applicationDeltaApplyResult{
				outcome:        persisted.Status.Outcome,
				fallbackReason: fallbackReason,
				targetKey:      targetKey,
			}
		}
	}
	return results
}

func mergeApplicationDeltaResults(
	current, restored map[string]map[imageRef]applicationDeltaApplyResult,
) map[string]map[imageRef]applicationDeltaApplyResult {
	if current == nil {
		current = make(map[string]map[imageRef]applicationDeltaApplyResult)
	}
	for application, restoredResults := range restored {
		currentResults := current[application]
		if currentResults == nil {
			currentResults = make(map[imageRef]applicationDeltaApplyResult)
			current[application] = currentResults
		}
		for target, result := range restoredResults {
			if _, exists := currentResults[target]; exists {
				continue
			}
			currentResults[target] = result
		}
	}
	return current
}

func sameStringMap(first, second map[string]string) bool {
	if len(first) != len(second) {
		return false
	}
	for key, value := range first {
		if second[key] != value {
			return false
		}
	}
	return true
}

func (m *prefetchManager) checkReady(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	var pending []string
	for image, task := range m.tasks {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if task.err != nil {
			if errors.IsRetryable(task.err) {
				pending = append(pending, fmt.Sprintf("%s retrying: %v", image, task.err))
			} else {
				return fmt.Errorf("%w: %w", errors.WithElement(image.image), task.err)
			}
			continue
		}
		if !task.done {
			pending = append(pending, fmt.Sprintf("%s in progress", image))
			continue
		}
	}

	if len(pending) > 0 {
		// ensure retry
		return fmt.Errorf("%w: %v", errors.ErrPrefetchNotReady, pending)
	}
	return nil
}

func (m *prefetchManager) processTarget(ctx context.Context, target imageRef) {
	var task *prefetchTask
	m.log.Infof("Prefetching OCI target: %s", target)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	m.mu.Lock()
	task, ok := m.tasks[target]
	if !ok {
		// handle race against cleanup
		m.mu.Unlock()
		return
	}
	task.cancelFn = cancel
	task = snapshotOperationLocked(task)
	m.mu.Unlock()

	// these operations are intentionally serial.  consider whether the
	// networking and disk I/O overhead of async pulls would be worth changing
	// before we do.
	retries := 0
	for {
		if ctx.Err() != nil {
			m.setResult(target, fmt.Errorf("pulling oci target %w: %w", errors.WithElement(target.image), ctx.Err()), task)
			m.log.Warnf("Context error pulling oci target: %s", target)
			return
		}
		if err := m.pull(ctx, target, task); err != nil {
			if errors.IsRetryable(err) {
				retries++
				m.setError(target, fmt.Errorf("%w: %w", errors.WithElement(target.image), err), task)

				// cleanup file system from partial layer pulls
				if err := m.cleanupPartialLayers(ctx, target.owner); err != nil {
					m.log.Warnf("cleanup failed: %v", err)
				} else {
					m.log.Debug("cleanup completed successfully")
				}

				// A pull client may already have exhausted its own retry budget.
				// Do not retry that exhausted error again from the prefetch loop.
				// Also enforce the prefetch retry budget for retryable errors that
				// are returned without a client-side retry limit.
				if errors.Is(err, poll.ErrMaxSteps) ||
					(m.pollConfig != nil && m.pollConfig.MaxSteps > 0 && retries > m.pollConfig.MaxSteps) {
					m.log.Warnf("Prefetch retry limit reached for %s after %d attempts: %v", target, retries, err)
					m.setResult(target, fmt.Errorf(
						"%w: pulling oci target %w after retry limit (%d): %w",
						errors.ErrNoRetry, errors.WithElement(target.image), retries, err,
					), task)
					return
				}

				m.log.Warnf("Retrying prefetch for %s (attempt %d): %v", target, retries+1, err)
				select {
				case <-time.After(poll.CalculateBackoffDelay(m.pollConfig, retries)):
					continue
				case <-ctx.Done():
					m.log.Warnf("Prefetch loop canceled while waiting to retry: %v", ctx.Err())
					return
				}
			}
			m.setResult(target, fmt.Errorf("pulling oci target %w: %w", errors.WithElement(target.image), err), task)
			return
		}
		// success
		m.setResult(target, nil, task)
		return
	}
}

func (m *prefetchManager) pull(ctx context.Context, target imageRef, task *prefetchTask) error {
	task = m.snapshotOperation(task)
	ociType := task.ociType
	if task.resolvedOCIType == OCITypePodmanArtifact {
		ociType = OCITypePodmanArtifact
	}

	// Evaluate client options lazily at pull time
	opts := []client.ClientOption{client.Timeout(m.pullTimeout)}
	if task.clientOptsFn != nil {
		opts = append(opts, task.clientOptsFn()...)
	}
	podman, skopeo, err := m.preparePullClients(target, task)
	if err != nil {
		return err
	}
	return m.pullByOCIType(ctx, target, task, ociType, podman, skopeo, opts...)
}

func (m *prefetchManager) preparePullClients(
	target imageRef,
	task *prefetchTask,
) (*client.Podman, *client.Skopeo, error) {
	ociType := task.ociType
	var podman *client.Podman
	var skopeo *client.Skopeo
	var err error
	needsPodman := ociType == OCITypePodmanImage || ociType == OCITypePodmanArtifact || ociType == OCITypeAuto
	needsSkopeo := ociType == OCITypeAuto ||
		(ociType == OCITypePodmanImage && task.delta != nil && !task.delta.Package &&
			(hasApplicationDeltaContext(task.delta) || m.ociDelta != nil || m.ociDeltaFactory != nil)) ||
		(ociType == OCITypeCRIImage && task.delta != nil &&
			(hasApplicationDeltaContext(task.delta) || m.ociDelta != nil))

	if needsPodman && podman == nil {
		podman, err = m.podmanFactory(target.owner)
		if err != nil {
			return nil, nil, fmt.Errorf("creating podman client: %w", err)
		}
	}

	if needsSkopeo && skopeo == nil {
		skopeo, err = m.skopeoFactory(target.owner)
		if err != nil {
			return nil, nil, fmt.Errorf("creating skopeo client: %w", err)
		}
	}
	return podman, skopeo, nil
}

func (m *prefetchManager) pullByOCIType(
	ctx context.Context,
	target imageRef,
	task *prefetchTask,
	ociType OCIType,
	podman *client.Podman,
	skopeo *client.Skopeo,
	opts ...client.ClientOption,
) error {
	switch ociType {
	case OCITypePodmanImage:
		return m.pullApplicationImage(ctx, target, task, podman, skopeo, opts...)
	case OCITypeCRIImage:
		return m.pullCRIImage(ctx, target, task, skopeo, opts...)
	case OCITypePodmanArtifact:
		return m.pullApplicationArtifact(ctx, target, task, podman, opts...)
	case OCITypeHelmChart:
		if err := m.cliClients.Helm().Resolve(ctx, target.image, opts...); err != nil {
			return err
		}
		if task.delta != nil && task.delta.Package {
			m.recordDeltaApplyResult(target, task, v1beta1.DeviceDeltaApplyOutcomeNotRequired, "")
		}
		return nil
	case OCITypeAuto:
		return m.pullAutoDetectedOCIType(ctx, target, task, podman, skopeo, opts...)
	default:
		return fmt.Errorf("invalid oci type %s", ociType)
	}
}

func (m *prefetchManager) pullAutoDetectedOCIType(
	ctx context.Context,
	target imageRef,
	task *prefetchTask,
	podman *client.Podman,
	skopeo *client.Skopeo,
	opts ...client.ClientOption,
) error {
	m.log.Debugf("Auto-detecting OCI type for %s", target)
	manifest, err := skopeo.InspectManifest(ctx, target.image, opts...)
	if err != nil {
		return fmt.Errorf("inspecting manifest for auto-detection: %w", err)
	}
	detectedType, err := detectOCIType(manifest)
	if err != nil {
		return fmt.Errorf("detecting OCI type: %w", err)
	}
	m.log.Infof("Detected OCI type for %s: %s", target, detectedType)
	task.resolvedOCIType = detectedType

	switch detectedType {
	case OCITypePodmanImage:
		return m.pullApplicationImage(ctx, target, task, podman, skopeo, opts...)
	case OCITypePodmanArtifact:
		return m.pullApplicationArtifact(ctx, target, task, podman, opts...)
	default:
		return fmt.Errorf("unexpected detected OCI type: %s", detectedType)
	}
}

func (m *prefetchManager) pullCRIImage(ctx context.Context, target imageRef, task *prefetchTask, skopeo *client.Skopeo, opts ...client.ClientOption) error {
	task = m.snapshotOperation(task)
	cri := m.cliClients.CRI()
	if task.delta == nil {
		_, err := cri.Pull(ctx, target.image, opts...)
		return err
	}
	if m.ociDelta == nil {
		if task.delta.Hint != "" {
			return m.applicationCRIDeltaFallback(ctx, target, task, fmt.Errorf("OCI delta client is unavailable"), opts...)
		}
		imageRef, err := cri.Pull(ctx, target.image, opts...)
		if err == nil {
			m.recordObservedDeltaResult(ctx, target, task, nil, imageRef, v1beta1.DeviceDeltaApplyOutcomeNotUsed, "", opts...)
		}
		return err
	}
	deltaOpts := append([]client.ClientOption(nil), opts...)
	deltaOpts = append(deltaOpts, client.WithDefaultAuth())
	if task.delta.Hint == "" && task.delta.SourceDigest != "" {
		m.checkUncachedApplicationTask(ctx, target, OCITypeCRIImage, task, nil, skopeo, deltaOpts...)
	}
	candidate, sourceRef, err := m.discoverCRIDelta(ctx, target.image, task.delta.Hint, cri, skopeo, deltaOpts...)
	if err != nil {
		return m.applicationCRIDeltaFallback(ctx, target, task, err, opts...)
	}
	if candidate.Reference == "" {
		imageRef, err := cri.Pull(ctx, target.image, opts...)
		if err == nil {
			m.recordObservedDeltaResult(ctx, target, task, nil, imageRef, v1beta1.DeviceDeltaApplyOutcomeNotUsed, "", opts...)
		}
		return err
	}

	runtimeInfo, err := cri.RuntimeInfo(ctx, opts...)
	if err != nil {
		return m.applicationCRIDeltaFallback(ctx, target, task, err, opts...)
	}
	if runtimeInfo.Name != client.CRIRuntimeCRIO && runtimeInfo.Name != client.CRIRuntimeContainerd {
		return m.applicationCRIDeltaFallback(
			ctx,
			target,
			task,
			fmt.Errorf("delta import is unsupported for CRI runtime %q", runtimeInfo.Name),
			opts...,
		)
	}

	tmpDir, err := m.readWriter.MkdirTemp("application-delta")
	if err != nil {
		return m.applicationCRIDeltaFallback(ctx, target, task, fmt.Errorf("create application delta temporary directory: %w", err), opts...)
	}
	defer m.removeApplicationDeltaTempDir(m.readWriter, tmpDir)

	deltaFile := filepath.Join(tmpDir, "delta.oci")
	if err := skopeo.Copy(ctx, "docker://"+candidate.Reference, "oci-archive:"+deltaFile, deltaOpts...); err != nil {
		return m.applicationCRIDeltaFallback(ctx, target, task, err, opts...)
	}

	switch runtimeInfo.Name {
	case client.CRIRuntimeCRIO:
		if err := m.ociDelta.Import(ctx, deltaFile, target.image); err != nil {
			return m.applicationCRIDeltaFallback(ctx, target, task, err, opts...)
		}
	case client.CRIRuntimeContainerd:
		if err := m.applyContainerdImageDelta(ctx, target.image, deltaFile, tmpDir, runtimeInfo.Endpoint, sourceRef, opts...); err != nil {
			return m.applicationCRIDeltaFallback(ctx, target, task, err, opts...)
		}
	}

	if !cri.ImageExists(ctx, target.image, opts...) {
		return m.applicationCRIDeltaFallback(ctx, target, task, fmt.Errorf("delta import did not make %s available in the CRI runtime", target.image), opts...)
	}

	if err := m.activateCRIRefreshedImage(ctx, cri, skopeo, target, *runtimeInfo, candidate, deltaOpts, opts...); err != nil {
		return m.applicationCRIDeltaFallback(ctx, target, task, err, opts...)
	}

	m.recordObservedDeltaResult(ctx, target, task, nil, target.image, v1beta1.DeviceDeltaApplyOutcomeApplied, "", opts...)
	m.log.Infof("Applied OCI delta for application image %s from %s", target.image, candidate.Reference)
	m.log.Infof("Refreshed registry reference for application image %s after OCI delta import", target.image)
	return nil
}

func (m *prefetchManager) activateCRIRefreshedImage(
	ctx context.Context,
	cri *client.CRI,
	skopeo *client.Skopeo,
	target imageRef,
	runtimeInfo client.CRIRuntimeInfo,
	candidate delta.Candidate,
	deltaOpts []client.ClientOption,
	opts ...client.ClientOption,
) error {
	refreshImage, err := deltaRefreshReference(target.image, candidate.TargetDigest)
	if err != nil {
		return err
	}
	if _, err := cri.Pull(ctx, refreshImage, opts...); err != nil {
		return fmt.Errorf("refresh registry manifest after delta import: %w", err)
	}
	canonicalMetadata, err := cri.ImageMetadata(ctx, refreshImage, opts...)
	if err != nil {
		return fmt.Errorf("inspect refreshed image for %s: %w", target.image, err)
	}
	digestPinned, err := imageReferenceIsDigestPinned(target.image)
	if err != nil {
		return fmt.Errorf("activate refreshed image for %s: %w", target.image, err)
	}
	switch runtimeInfo.Name {
	case client.CRIRuntimeCRIO:
		if digestPinned {
			break
		}
		if err := cri.RemoveImage(ctx, target.image, opts...); err != nil {
			return fmt.Errorf("remove reconstructed image for %s: %w", target.image, err)
		}
		if err := skopeo.CopyPreservingDigests(
			ctx, "docker://"+refreshImage, "containers-storage:"+target.image, deltaOpts...,
		); err != nil {
			return fmt.Errorf("activate refreshed image for %s: %w", target.image, err)
		}
	case client.CRIRuntimeContainerd:
		if digestPinned {
			break
		}
		if err := cri.TagContainerdImage(ctx, runtimeInfo.Endpoint, refreshImage, target.image, opts...); err != nil {
			return fmt.Errorf("activate refreshed image for %s: %w", target.image, err)
		}
	}
	boundMetadata, err := cri.ImageMetadata(ctx, target.image, opts...)
	if err != nil {
		return fmt.Errorf("verify activated image for %s: %w", target.image, err)
	}
	repository, _, _, err := normalizedCRIImageReferences(target.image)
	if err != nil || !criImageMetadataMatches(boundMetadata, canonicalMetadata.ID, repository, candidate.TargetDigest) {
		return fmt.Errorf("verify activated image for %s: bound image does not match canonical image", target.image)
	}
	return nil
}

func criImageMetadataMatches(metadata client.CRIImageMetadata, canonicalImageID, repository, targetDigest string) bool {
	if metadata.ID != canonicalImageID {
		return false
	}
	for _, repoDigest := range metadata.RepoDigests {
		named, err := reference.ParseNormalizedNamed(repoDigest)
		if err != nil || named.Name() != repository {
			continue
		}
		if pinned, ok := named.(reference.Digested); ok && pinned.Digest().String() == targetDigest {
			return true
		}
	}
	return false
}

func (m *prefetchManager) applyContainerdImageDelta(
	ctx context.Context,
	targetImage, deltaFile, tmpDir, endpoint, sourceRef string,
	opts ...client.ClientOption,
) error {
	repository, targetRef, _, err := normalizedCRIImageReferences(targetImage)
	if err != nil {
		return err
	}

	sourceRootfs := filepath.Join(tmpDir, "source")
	if err := m.readWriter.MkdirAll(sourceRootfs, fileio.DefaultDirectoryPermissions); err != nil {
		return fmt.Errorf("create containerd source mountpoint: %w", err)
	}
	cri := m.cliClients.CRI()
	if err := cri.MountContainerdImage(ctx, endpoint, sourceRef, sourceRootfs, opts...); err != nil {
		return fmt.Errorf("mount containerd source image %s: %w", sourceRef, err)
	}
	defer func() {
		if err := cri.UnmountContainerdImage(context.WithoutCancel(ctx), endpoint, sourceRootfs); err != nil {
			m.log.Warnf("unmount containerd source image %s: %v", sourceRef, err)
		}
	}()

	targetArchive := filepath.Join(tmpDir, "target.oci-archive")
	if err := m.ociDelta.ApplyFromDirectory(ctx, sourceRootfs, deltaFile, "oci-archive:"+targetArchive); err != nil {
		return err
	}

	importBase := repository + "/flightctl-delta-" + strings.ToLower(filepath.Base(tmpDir))
	if err := cri.ImportContainerdImage(ctx, endpoint, targetArchive, importBase, targetRef, opts...); err != nil {
		return err
	}
	return nil
}

func normalizedCRIImageReferences(image string) (string, string, bool, error) {
	named, err := reference.ParseNormalizedNamed(image)
	if err != nil {
		return "", "", false, fmt.Errorf("parse CRI image reference %q: %w", image, err)
	}
	target := reference.TagNameOnly(named)
	_, digestPinned := named.(reference.Digested)
	return reference.TrimNamed(target).String(), target.String(), digestPinned, nil
}

func deltaRefreshReference(targetImage, targetDigest string) (string, error) {
	if targetDigest == "" {
		return "", fmt.Errorf("delta target digest is missing")
	}
	parsed, err := reference.Parse(targetImage)
	if err != nil {
		return "", fmt.Errorf("parse target image reference %q: %w", targetImage, err)
	}
	named, ok := parsed.(reference.Named)
	if !ok {
		return "", fmt.Errorf("target image reference %q has no repository name", targetImage)
	}
	return reference.TrimNamed(named).String() + "@" + targetDigest, nil
}

func imageReferenceIsDigestPinned(image string) (bool, error) {
	parsed, err := reference.Parse(image)
	if err != nil {
		return false, fmt.Errorf("parse image reference %q: %w", image, err)
	}
	_, digestPinned := parsed.(reference.Digested)
	return digestPinned, nil
}

func (m *prefetchManager) applicationCRIDeltaFallback(ctx context.Context, target imageRef, task *prefetchTask, deltaErr error, opts ...client.ClientOption) error {
	m.recordDeltaFallback(target, task, deltaErr)
	imageRef, err := m.cliClients.CRI().Pull(ctx, target.image, opts...)
	if err != nil {
		return fmt.Errorf("application delta failed: %w; full CRI image pull failed: %w", deltaErr, err)
	}
	m.recordObservedDeltaResult(ctx, target, task, nil, imageRef, v1beta1.DeviceDeltaApplyOutcomeFallback, deltaErr.Error(), opts...)
	return nil
}

func (m *prefetchManager) pullApplicationImage(ctx context.Context, target imageRef, task *prefetchTask, podman *client.Podman, skopeo *client.Skopeo, opts ...client.ClientOption) error {
	task = m.snapshotOperation(task)
	if task.delta == nil || task.delta.Package {
		imageID, err := podman.Pull(ctx, target.image, opts...)
		if err == nil && task.delta != nil {
			m.recordObservedDeltaResult(ctx, target, task, podman, imageID, v1beta1.DeviceDeltaApplyOutcomeNotRequired, "", opts...)
		}
		return err
	}
	if m.ociDelta == nil && m.ociDeltaFactory == nil {
		if task.delta.Hint != "" {
			return m.applicationDeltaFallback(ctx, target, task, podman, fmt.Errorf("OCI delta client is unavailable"), opts...)
		}
		imageID, err := podman.Pull(ctx, target.image, opts...)
		if err == nil {
			m.recordObservedDeltaResult(ctx, target, task, podman, imageID, v1beta1.DeviceDeltaApplyOutcomeNotUsed, "", opts...)
		}
		return err
	}
	deltaOpts := append([]client.ClientOption(nil), opts...)
	deltaOpts = append(deltaOpts, client.WithDefaultAuth())
	if task.delta.Hint == "" && task.delta.SourceDigest != "" {
		m.checkUncachedApplicationTask(ctx, target, OCITypePodmanImage, task, podman, skopeo, deltaOpts...)
	}
	candidate, err := m.discoverPodmanDelta(ctx, target.image, task.delta.Hint, podman, skopeo, deltaOpts...)
	if err != nil {
		return m.applicationDeltaFallback(ctx, target, task, podman, err, opts...)
	}
	if candidate.Reference == "" {
		imageID, err := podman.Pull(ctx, target.image, opts...)
		if err == nil {
			m.recordObservedDeltaResult(ctx, target, task, podman, imageID, v1beta1.DeviceDeltaApplyOutcomeNotUsed, "", opts...)
		}
		return err
	}

	ociDelta, err := m.applicationOCIDelta(target.owner)
	if err != nil {
		return m.applicationDeltaFallback(ctx, target, task, podman, err, opts...)
	}

	tmpReadWriter, err := m.applicationDeltaReadWriter(target.owner)
	if err != nil {
		return m.applicationDeltaFallback(ctx, target, task, podman, err, opts...)
	}
	tmpDir, err := tmpReadWriter.MkdirTemp("application-delta")
	if err != nil {
		return m.applicationDeltaFallback(ctx, target, task, podman, fmt.Errorf("create application delta temporary directory: %w", err), opts...)
	}
	defer m.removeApplicationDeltaTempDir(tmpReadWriter, tmpDir)

	deltaFile := filepath.Join(tmpDir, "delta.oci")
	if err := skopeo.Copy(ctx, "docker://"+candidate.Reference, "oci-archive:"+deltaFile, deltaOpts...); err != nil {
		return m.applicationDeltaFallback(ctx, target, task, podman, err, opts...)
	}
	if err := ociDelta.Import(ctx, deltaFile, target.image); err != nil {
		return m.applicationDeltaFallback(ctx, target, task, podman, err, opts...)
	}
	// Pull the registry reference after importing the reconstructed image so
	// container storage records the registry's canonical manifest digest. The
	// blobs already present from delta reconstruction are reused by digest.
	refreshImage, err := deltaRefreshReference(target.image, candidate.TargetDigest)
	if err != nil {
		return m.applicationDeltaFallback(ctx, target, task, podman, err, opts...)
	}
	imageID, err := podman.Pull(ctx, refreshImage, opts...)
	if err != nil {
		return m.applicationDeltaFallback(ctx, target, task, podman,
			fmt.Errorf("refresh registry manifest after delta import: %w", err), opts...)
	}
	if imageID == "" {
		return m.applicationDeltaFallback(ctx, target, task, podman,
			fmt.Errorf("refresh registry manifest after delta import: empty image ID"), opts...)
	}
	digestPinned, err := imageReferenceIsDigestPinned(target.image)
	if err != nil {
		return m.applicationDeltaFallback(ctx, target, task, podman,
			fmt.Errorf("activate refreshed image for %s: %w", target.image, err), opts...)
	}
	if !digestPinned {
		if err := podman.Tag(ctx, imageID, target.image); err != nil {
			return m.applicationDeltaFallback(ctx, target, task, podman,
				fmt.Errorf("activate refreshed image for %s: %w", target.image, err), opts...)
		}
	}
	boundImageID, err := podman.ImageID(ctx, target.image)
	if err != nil {
		return m.applicationDeltaFallback(ctx, target, task, podman,
			fmt.Errorf("verify activated image for %s: %w", target.image, err), opts...)
	}
	if boundImageID != imageID {
		return m.applicationDeltaFallback(ctx, target, task, podman,
			fmt.Errorf("verify activated image for %s: bound image ID %q does not match canonical image ID %q", target.image, boundImageID, imageID), opts...)
	}
	m.recordObservedDeltaResult(ctx, target, task, podman, imageID, v1beta1.DeviceDeltaApplyOutcomeApplied, "", opts...)
	m.log.Infof("Applied OCI delta for application image %s from %s", target.image, candidate.Reference)
	m.log.Debugf("Refreshed registry reference for application image %s after OCI delta import", target.image)
	return nil
}

func (m *prefetchManager) applicationOCIDelta(owner v1beta1.Username) (*client.OCIDelta, error) {
	if m.ociDeltaFactory != nil {
		ociDelta, err := m.ociDeltaFactory(owner)
		if err != nil {
			return nil, fmt.Errorf("create OCI delta client for user %s: %w", owner, err)
		}
		if ociDelta == nil {
			return nil, fmt.Errorf("create OCI delta client for user %s: factory returned nil", owner)
		}
		return ociDelta, nil
	}
	if applicationTargetUsesAgentStorage(owner) && m.ociDelta != nil {
		return m.ociDelta, nil
	}
	return nil, fmt.Errorf("OCI delta factory is not configured for RunAs user %s", owner)
}

func (m *prefetchManager) applicationDeltaFallback(ctx context.Context, target imageRef, task *prefetchTask, podman *client.Podman, deltaErr error, opts ...client.ClientOption) error {
	m.recordDeltaFallback(target, task, deltaErr)
	imageID, err := podman.Pull(ctx, target.image, opts...)
	if err != nil {
		return fmt.Errorf("application delta failed: %w; full image pull failed: %w", deltaErr, err)
	}
	m.recordObservedDeltaResult(ctx, target, task, podman, imageID, v1beta1.DeviceDeltaApplyOutcomeFallback, deltaErr.Error(), opts...)
	return nil
}

func (m *prefetchManager) pullApplicationArtifact(ctx context.Context, target imageRef, task *prefetchTask, podman *client.Podman, opts ...client.ClientOption) error {
	task = m.snapshotOperation(task)
	task.resolvedOCIType = OCITypePodmanArtifact
	if task.delta != nil && !task.delta.Package && task.delta.Hint != "" {
		return m.applicationArtifactDeltaFallback(
			ctx,
			target,
			task,
			podman,
			fmt.Errorf("application delta is not supported for OCI artifacts"),
			opts...,
		)
	}
	outcome := v1beta1.DeviceDeltaApplyOutcomeNotUsed
	packageTarget := task.delta != nil && task.delta.Package
	if packageTarget {
		outcome = v1beta1.DeviceDeltaApplyOutcomeNotRequired
	}
	if !packageTarget || task.pullPolicy == v1beta1.PullAlways || !podman.ArtifactExists(ctx, target.image) {
		if _, err := podman.PullArtifact(ctx, target.image, opts...); err != nil {
			return err
		}
	}
	digestValue, err := podman.ArtifactDigest(ctx, target.image)
	if err != nil || !validObservedDigest(digestValue) {
		m.recordArtifactDeltaResult(target, task, outcome, "", "")
		return nil
	}
	m.recordArtifactDeltaResult(target, task, outcome, "", digestValue)
	return nil
}

func (m *prefetchManager) applicationArtifactDeltaFallback(ctx context.Context, target imageRef, task *prefetchTask, podman *client.Podman, deltaErr error, opts ...client.ClientOption) error {
	m.recordDeltaFallback(target, task, deltaErr)
	// A failed image-delta hint still follows the full-artifact path. Respect
	// the pull policy when the artifact is already available locally.
	present := task.pullPolicy != v1beta1.PullAlways && podman.ArtifactExists(ctx, target.image)
	if !present {
		if task.pullPolicy == v1beta1.PullNever {
			return fmt.Errorf("application delta failed: %w; artifact %s is absent and pull policy is Never", deltaErr, target.image)
		}
		if _, err := podman.PullArtifact(ctx, target.image, opts...); err != nil {
			return fmt.Errorf("application delta failed: %w; full artifact pull failed: %w", deltaErr, err)
		}
	}
	digestValue, err := podman.ArtifactDigest(ctx, target.image)
	if err != nil || !validObservedDigest(digestValue) {
		return nil
	}
	m.recordArtifactDeltaResult(target, task, v1beta1.DeviceDeltaApplyOutcomeFallback, deltaErr.Error(), digestValue)
	return nil
}

func validObservedDigest(value string) bool {
	_, err := digest.Parse(value)
	return err == nil
}

func (m *prefetchManager) recordArtifactDeltaResult(target imageRef, task *prefetchTask, outcome v1beta1.DeviceDeltaApplyOutcomeType, reason, observedDigest string) {
	if task == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.recordDeltaApplyResultLocked(target, task, deltaApplications(task.delta), outcome, reason, observedDigest)
}

func applicationTargetUsesAgentStorage(owner v1beta1.Username) bool {
	// The agent runs as root; an empty RunAs uses that same process user.
	return owner.IsCurrentProcessUser() || owner.IsRootUser()
}

func (m *prefetchManager) applicationDeltaReadWriter(owner v1beta1.Username) (fileio.ReadWriter, error) {
	if m.readWriterFactory != nil {
		readWriter, err := m.readWriterFactory(owner)
		if err != nil {
			return nil, fmt.Errorf("create application delta workspace for user %s: %w", owner, err)
		}
		if readWriter == nil {
			return nil, fmt.Errorf("create application delta workspace for user %s: factory returned nil", owner)
		}
		return readWriter, nil
	}
	if applicationTargetUsesAgentStorage(owner) {
		return m.readWriter, nil
	}
	return nil, fmt.Errorf("read-writer factory is required to transfer application delta to user %s storage", owner)
}

func (m *prefetchManager) removeApplicationDeltaTempDir(readWriter fileio.ReadWriter, path string) {
	if err := readWriter.RemoveAll(path); err != nil {
		m.log.Warnf("remove application delta temporary directory %s: %v", path, err)
	}
}

// canRecordObservedResultLocked avoids runtime verification for unregistered or
// superseded attempts. The accepted write validates these identities again.
func (m *prefetchManager) canRecordObservedResultLocked(target imageRef, task *prefetchTask) bool {
	if m.deltaStatusStore == nil || task == nil || task.delta == nil || task.delta.Package || !m.currentOperationLocked(target, task) {
		return false
	}
	for _, application := range deltaApplications(task.delta) {
		if task.applicationTargetKey != "" && task.applicationAttempts[application].Lifetime != "" &&
			m.deltaAppLifetimes[application] == task.applicationAttempts[application].Lifetime &&
			m.deltaAppSpecKeys[application] != "" && m.deltaAppTargetKeys[application][target.targetID()] == task.applicationTargetKey {
			return true
		}
	}
	return false
}

func (m *prefetchManager) recordObservedDeltaResult(ctx context.Context, target imageRef, task *prefetchTask, podman *client.Podman, runtimeImageRef string, outcome v1beta1.DeviceDeltaApplyOutcomeType, reason string, opts ...client.ClientOption) {
	observed := ""
	m.mu.Lock()
	canRecord := m.canRecordObservedResultLocked(target, task)
	m.mu.Unlock()
	if canRecord {
		if task.ociType == OCITypeCRIImage {
			observed = m.observedCRIImageDigest(ctx, target, runtimeImageRef, opts...)
		} else if podman != nil && runtimeImageRef != "" {
			value, err := podman.ImageDigest(ctx, runtimeImageRef)
			if err == nil {
				observed = value
			}
		}
	}
	if _, err := digest.Parse(observed); err != nil {
		observed = ""
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.recordDeltaApplyResultLocked(target, task, deltaApplications(task.delta), outcome, reason, observed)
}

// observedCachedImageDigests verifies identities of a held image without
// following a mutable reference after its runtime identity has been captured.
func (m *prefetchManager) observedCachedImageDigests(ctx context.Context, target imageRef, ociType OCIType, podman *client.Podman, inspection applicationImageInspection, eligible bool, opts ...client.ClientOption) []string {
	if !eligible {
		return nil
	}
	var observed []string
	if ociType == OCITypeCRIImage {
		runtimeImageRef := inspection.runtimeImageRef
		if runtimeImageRef == "" && m.cliClients != nil && m.cliClients.CRI() != nil {
			metadata, err := m.cliClients.CRI().ImageMetadata(ctx, target.image, opts...)
			if err == nil {
				runtimeImageRef = metadata.ID
			}
		}
		observed = append(observed, m.observedCRIImageDigest(ctx, target, runtimeImageRef, opts...))
	} else if podman != nil {
		metadata := inspection.podmanMetadata
		if metadata == nil {
			inspected, err := podman.ImageMetadata(ctx, target.image)
			if err == nil {
				metadata = &inspected
			}
		}
		if metadata != nil && metadata.ID != "" {
			observed = append(observed, metadata.Digest)
			// Podman may report the platform manifest here while create events
			// retain an index digest. Record each locally verified identity for
			// cache reuse, without transferring Applied between digest forms.
			named, err := reference.ParseNormalizedNamed(target.image)
			if err == nil {
				for _, value := range metadata.RepoDigests {
					candidate, err := reference.ParseNormalizedNamed(value)
					if err != nil || candidate.Name() != named.Name() {
						continue
					}
					if pinned, ok := candidate.(reference.Digested); ok {
						observed = append(observed, pinned.Digest().String())
					}
				}
			}
		}
	}
	verified := make([]string, 0, len(observed))
	for _, value := range observed {
		if validObservedDigest(value) && !slices.Contains(verified, value) {
			verified = append(verified, value)
		}
	}
	return verified
}

// observedCRIImageDigest verifies a selected platform manifest against the same
// local runtime image's repository metadata. RepoDigests ordering is not evidence.
func (m *prefetchManager) observedCRIImageDigest(ctx context.Context, target imageRef, runtimeImageRef string, opts ...client.ClientOption) string {
	if runtimeImageRef == "" || m.cliClients == nil || m.cliClients.CRI() == nil || m.skopeoFactory == nil {
		return ""
	}
	cri := m.cliClients.CRI()
	skopeo, err := m.skopeoFactory(target.owner)
	if err != nil || skopeo == nil {
		return ""
	}
	repository, _, _, err := normalizedCRIImageReferences(target.image)
	if err != nil {
		return ""
	}
	repoDigests, err := cri.ImageRepoDigests(ctx, runtimeImageRef, opts...)
	if err != nil {
		return ""
	}
	local := make(map[string]bool)
	for _, value := range repoDigests {
		named, err := reference.ParseNormalizedNamed(value)
		if err != nil || named.Name() != repository {
			continue
		}
		if pinned, ok := named.(reference.Digested); ok {
			local[pinned.Digest().String()] = true
		}
	}
	observed := ""
	for candidate := range local {
		platform, err := skopeo.InspectPlatformDigest(ctx, repository+"@"+candidate, opts...)
		if err != nil || !local[platform] {
			continue
		}
		if observed != "" && observed != platform {
			return ""
		}
		observed = platform
	}
	return observed
}

func (m *prefetchManager) recordDeltaFallback(target imageRef, task *prefetchTask, deltaErr error) {
	if task == nil || task.delta == nil || deltaErr == nil {
		return
	}
	m.recordDeltaApplyResult(target, task, v1beta1.DeviceDeltaApplyOutcomeFallback, deltaErr.Error())
}

func (m *prefetchManager) recordDeltaApplied(target imageRef, task *prefetchTask) {
	m.recordDeltaApplyResult(target, task, v1beta1.DeviceDeltaApplyOutcomeApplied, "")
}

func (m *prefetchManager) recordDeltaApplyResult(target imageRef, task *prefetchTask, outcome v1beta1.DeviceDeltaApplyOutcomeType, fallbackReason string) {
	if task == nil || task.delta == nil {
		return
	}
	applications := deltaApplications(task.delta)

	m.mu.Lock()
	defer m.mu.Unlock()
	m.recordDeltaApplyResultLocked(target, task, applications, outcome, fallbackReason)
}

func (m *prefetchManager) recordDeltaApplyResultLocked(target imageRef, task *prefetchTask, applications []string, outcome v1beta1.DeviceDeltaApplyOutcomeType, fallbackReason string, observedDigest ...string) {
	if task == nil || task.delta == nil || len(applications) == 0 {
		return
	}

	_, exists := m.tasks[target]
	if !exists || !m.currentOperationLocked(target, task) {
		m.log.Infof(
			"Ignoring stale application delta result: image=%s outcome=%s taskGeneration=%d currentGeneration=%d taskStillRegistered=%t",
			target.image, outcome, task.deltaGeneration, m.deltaGeneration, exists && m.currentOperationLocked(target, task),
		)
		return
	}
	if m.deltaApplyResults == nil {
		m.deltaApplyResults = make(map[string]map[imageRef]applicationDeltaApplyResult)
	}
	for _, application := range applications {
		targetID := target.targetID()
		persistedTargetKey, targetMatches := m.deltaApplicationTargetKeyForResult(application, targetID, task.applicationTargetKey)
		if !targetMatches {
			m.log.Debugf(
				"Ignoring application delta result for stale target identity: application=%s image=%s outcome=%s",
				application, target.image, outcome,
			)
			continue
		}
		persistence := applicationDeltaPersistence{
			application:        application,
			target:             target,
			targetID:           targetID,
			task:               task,
			persistedTargetKey: persistedTargetKey,
			status:             applicationDeltaApplyStatus(outcome, fallbackReason),
		}
		m.persistObservedDeltaApplyResultLocked(persistence, observedDigest)

		results := m.deltaApplyResults[application]
		if results == nil {
			results = make(map[imageRef]applicationDeltaApplyResult)
			m.deltaApplyResults[application] = results
		}
		if existing, exists := results[target]; exists && existing.targetKey == task.applicationTargetKey {
			// Keep the recorded result for an unchanged image target. A verified
			// NotRequired can upgrade an earlier NotUsed, but must not erase the
			// outcome that describes how this target was originally applied.
			if outcome == v1beta1.DeviceDeltaApplyOutcomeNotUsed ||
				(outcome == v1beta1.DeviceDeltaApplyOutcomeNotRequired && existing.outcome != v1beta1.DeviceDeltaApplyOutcomeNotUsed) {
				m.log.Debugf(
					"Preserving application delta result: application=%s image=%s existingOutcome=%s attemptedOutcome=%s",
					application, target.image, existing.outcome, outcome,
				)
				continue
			}
		}
		results[target] = applicationDeltaApplyResult{
			outcome:        outcome,
			fallbackReason: fallbackReason,
			targetKey:      task.applicationTargetKey,
		}
		logResult := m.log.Debugf
		if outcome == v1beta1.DeviceDeltaApplyOutcomeFallback {
			logResult = m.log.Infof
		}
		logResult(
			"Recorded application delta result: application=%s image=%s outcome=%s ociType=%s hintPresent=%t sourceDigest=%q targetDigest=%q fallbackReason=%q",
			application, target.image, outcome, task.ociType, task.delta.Hint != "", task.delta.SourceDigest, task.targetDigest, fallbackReason,
		)
		m.persistDeltaApplyResultLocked(persistence)
	}
}

func (m *prefetchManager) deltaApplicationTargetKeyForResult(application, targetID, expectedKey string) (string, bool) {
	if m.deltaStatusStore == nil || m.deltaAppTargetKeys == nil {
		return "", true
	}
	persistedTargetKey := m.deltaAppTargetKeys[application][targetID]
	return persistedTargetKey, persistedTargetKey == "" || persistedTargetKey == expectedKey
}

func (m *prefetchManager) persistObservedDeltaApplyResultLocked(result applicationDeltaPersistence, observedDigest []string) {
	attempt := result.task.applicationAttempts[result.application]
	if m.deltaStatusStore == nil || result.task.delta.Package || result.persistedTargetKey != result.task.applicationTargetKey || attempt.Lifetime == "" || len(observedDigest) == 0 || observedDigest[0] == "" {
		return
	}
	imageType := result.task.resolvedOCIType
	if imageType == "" {
		imageType = result.task.ociType
	}
	if imageType == OCITypeAuto {
		imageType = OCITypePodmanImage
	}
	image := deltastatus.ApplicationImage{
		Image:   result.target.image,
		Owner:   string(result.target.owner.WithDefault(v1beta1.RootUsername)),
		OCIType: string(imageType),
		Digest:  observedDigest[0],
	}
	if err := m.deltaStatusStore.RecordApplicationImageResultForAttempt(
		attempt, image, result.status,
	); err != nil {
		m.log.Warnf("Failed to persist observed delta result for application %s image %s: %v", result.application, result.target.image, err)
	}
}

func (m *prefetchManager) persistDeltaApplyResultLocked(result applicationDeltaPersistence) {
	attempt := result.task.applicationAttempts[result.application]
	if m.deltaStatusStore == nil || result.task.delta.Package || result.persistedTargetKey != result.task.applicationTargetKey || attempt.Lifetime == "" {
		return
	}
	if err := m.deltaStatusStore.RecordApplicationResultForAttempt(
		attempt, result.status,
	); err != nil {
		m.log.Warnf("Failed to persist delta result for application %s image %s: %v", result.application, result.target.image, err)
	}
}

func applicationDeltaApplyStatus(outcome v1beta1.DeviceDeltaApplyOutcomeType, fallbackReason string) v1beta1.DeviceDeltaApplyStatus {
	status := v1beta1.DeviceDeltaApplyStatus{Outcome: outcome}
	if fallbackReason != "" {
		reason := fallbackReason
		status.FallbackReason = &reason
	}
	return status
}

func (m *prefetchManager) setResult(target imageRef, err error, operation *prefetchTask) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.currentOperationLocked(target, operation) {
		return
	}
	task, ok := m.tasks[target]
	if !ok {
		m.log.Infof("Ignoring prefetch completion for unregistered target: image=%s errorPresent=%t", target.image, err != nil)
		return
	}
	task.err = err
	task.done = true
	m.log.Infof("Completed prefetch task: image=%s errorPresent=%t", target.image, err != nil)
}

func (m *prefetchManager) setError(target imageRef, err error, operation *prefetchTask) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.currentOperationLocked(target, operation) {
		return
	}
	task, ok := m.tasks[target]
	if !ok {
		m.log.Debugf("Task for %s no longer exists, skipping error update", target)
		return
	}
	task.err = err
}

func (m *prefetchManager) Schedule(ctx context.Context, targets OCIPullTargetsByUser) error {
	for user, target := range targets.Iter() {
		ref := imageRef{image: target.Reference, owner: user, ociType: target.Type}
		if err := m.schedule(ctx, ref, target.Type, target.Digest, target.ClientOptsFn, target.Delta, target.PullPolicy); err != nil {
			return fmt.Errorf("prefetch schedule %w: %w", errors.WithElement(target.Reference), err)
		}
	}

	return nil
}

func (m *prefetchManager) schedule(ctx context.Context, target imageRef, ociType OCIType, targetDigest string, clientOptsFn ClientOptsFn, delta *OCIDeltaTarget, pullPolicy v1beta1.ImagePullPolicy) error {
	needsQueue, err := m.prepareTask(ctx, target, ociType, targetDigest, clientOptsFn, delta, pullPolicy)
	if err != nil {
		return err
	}
	if !needsQueue {
		return nil
	}

	timer := time.NewTimer(250 * time.Millisecond)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		m.removeTask(target)
		return fmt.Errorf("failed to enqueue target %w: %w", errors.WithElement(target.image), ctx.Err())
	case m.queue <- target:
		return nil
	case <-timer.C:
		m.log.Warnf("Prefetch schedule failed for: %s: buffer full", target)
		m.removeTask(target)
		return fmt.Errorf("%w %w: buffer full", errors.ErrPrefetchNotReady, errors.WithElement(target.image))
	}
}

func (m *prefetchManager) prepareTask(ctx context.Context, target imageRef, ociType OCIType, targetDigest string, clientOptsFn ClientOptsFn, delta *OCIDeltaTarget, pullPolicies ...v1beta1.ImagePullPolicy) (bool, error) {
	var pullPolicy v1beta1.ImagePullPolicy
	if len(pullPolicies) > 0 {
		pullPolicy = pullPolicies[0]
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	// Superseded in-flight work is cancelled and queued under a new identity.
	if existing := m.tasks[target]; existing != nil && !existing.done && existing.deltaGeneration != m.deltaGeneration {
		if existing.cancelFn == nil {
			// The queued entry will read this registration when it is processed.
			existing.deltaGeneration = m.deltaGeneration
			existing.operationTask = nil
			existing.ociType = ociType
			existing.pullPolicy = pullPolicy
			existing.targetDigest = targetDigest
			existing.resolvedDigest = ""
			existing.applicationTargetKey = applicationImageTargetKeyFor(target, targetDigest, ociType)
			existing.applicationAttempts = m.applicationAttemptsLocked(target, applicationImageTargetKeyFor(target, targetDigest, ociType), delta)
			existing.delta = cloneOCIDeltaTarget(delta)
			existing.clientOptsFn = clientOptsFn
			return false, nil
		}
		existing.cancelFn()
		delete(m.tasks, target)
	}

	if existing, exists := m.tasks[target]; exists {
		if existing.done && delta != nil && delta.Package && existing.deltaGeneration != m.deltaGeneration {
			// Recheck local availability and publish the neutral package outcome
			// for this generation without resolving a mutable tag remotely.
			delete(m.tasks, target)
		} else {
			return m.prepareExistingTaskLocked(ctx, target, existing, clientOptsFn, delta)
		}
	}

	podman, err := m.podmanFactory(target.owner)
	if err != nil {
		return false, fmt.Errorf("creating podman client: %w", err)
	}

	targetExists, targetArtifact, resolvedOCIType, err := m.resolveTargetAvailability(ctx, target, ociType, clientOptsFn, podman)
	if err != nil {
		return false, err
	}

	if m.canUseExistingTarget(targetExists, targetArtifact, delta) {
		return m.preparePresentTargetLocked(ctx, target, ociType, resolvedOCIType, targetDigest, clientOptsFn, delta, pullPolicy, podman)
	}

	task := &prefetchTask{
		ociType:              ociType,
		targetPresent:        targetExists && targetArtifact,
		resolvedOCIType:      resolvedOCIType,
		pullPolicy:           pullPolicy,
		targetDigest:         targetDigest,
		applicationTargetKey: applicationImageTargetKeyFor(target, targetDigest, ociType),
		applicationAttempts:  m.applicationAttemptsLocked(target, applicationImageTargetKeyFor(target, targetDigest, ociType), delta),
		clientOptsFn:         clientOptsFn,
		delta:                delta,
		deltaGeneration:      m.deltaGeneration,
	}
	m.tasks[target] = task
	return true, nil
}

func (m *prefetchManager) prepareExistingTaskLocked(
	ctx context.Context,
	target imageRef,
	existing *prefetchTask,
	clientOptsFn ClientOptsFn,
	delta *OCIDeltaTarget,
) (bool, error) {
	// BeforeUpdate runs cleanupChangedTasks before Schedule, so a reused task
	// still has the current digest, OCI type, delta, and target key.
	generationChanged := existing.deltaGeneration != m.deltaGeneration
	if generationChanged && existing.done {
		// A completed task can be reused for a new update attempt, but the prior
		// immutable operation must keep its captured application tokens.
		existing.operationTask = nil
		existing.applicationAttempts = m.applicationAttemptsLocked(target, existing.applicationTargetKey, delta)
	}
	existing.deltaGeneration = m.deltaGeneration
	if !existing.done || existing.err != nil || delta == nil {
		return false, nil
	}
	if existing.resolvedOCIType == OCITypePodmanArtifact && delta.Hint != "" {
		existing.delta = cloneOCIDeltaTarget(delta)
		existing.applicationAttempts = m.applicationAttemptsLocked(target, existing.applicationTargetKey, delta)
		if generationChanged {
			existing.clientOptsFn = clientOptsFn
			existing.done = false
			return true, nil
		}
		return false, nil
	}
	if !supportsApplicationImageDigestCheck(existing.ociType) {
		m.recordDeltaApplyResultLocked(target, existing, deltaApplications(delta), v1beta1.DeviceDeltaApplyOutcomeNotUsed, "")
		return false, nil
	}
	if generationChanged {
		existing.clientOptsFn = clientOptsFn
		existing.targetPresent = false
		existing.delta = delta
		existing.done = false
		existing.err = nil
		existing.resolvedDigest = ""
	}
	return m.checkCachedApplicationTask(ctx, target, existing), nil
}

func (m *prefetchManager) resolveTargetAvailability(
	ctx context.Context,
	target imageRef,
	ociType OCIType,
	clientOptsFn ClientOptsFn,
	podman *client.Podman,
) (bool, bool, OCIType, error) {
	var targetExists, targetArtifact bool
	switch ociType {
	case OCITypePodmanImage:
		targetExists = podman.ImageExists(ctx, target.image)
	case OCITypeCRIImage:
		var opts []client.ClientOption
		if clientOptsFn != nil {
			opts = clientOptsFn()
		}
		targetExists = m.cliClients.CRI().ImageExists(ctx, target.image, opts...)
	case OCITypePodmanArtifact:
		targetExists = podman.ArtifactExists(ctx, target.image)
		targetArtifact = targetExists
	case OCITypeAuto:
		targetExists = podman.ImageExists(ctx, target.image)
		if !targetExists {
			targetArtifact = podman.ArtifactExists(ctx, target.image)
			targetExists = targetArtifact
		}
	case OCITypeHelmChart:
		resolved, err := m.cliClients.Helm().IsResolved(target.image)
		if err != nil {
			return false, false, "", fmt.Errorf("check helm chart resolved: %w", err)
		}
		targetExists = resolved
	default:
		return false, false, "", fmt.Errorf("invalid oci type %s", ociType)
	}
	resolvedOCIType := ociType
	if ociType == OCITypeAuto && targetExists {
		resolvedOCIType = OCITypePodmanImage
		if targetArtifact {
			resolvedOCIType = OCITypePodmanArtifact
		}
	}
	return targetExists, targetArtifact, resolvedOCIType, nil
}

func (m *prefetchManager) canUseExistingTarget(
	targetExists bool,
	targetArtifact bool,
	delta *OCIDeltaTarget,
) bool {
	hintedArtifact := targetArtifact && delta != nil && !delta.Package && delta.Hint != ""
	return targetExists && !hintedArtifact
}

func (m *prefetchManager) preparePresentTargetLocked(
	ctx context.Context,
	target imageRef,
	ociType OCIType,
	resolvedOCIType OCIType,
	targetDigest string,
	clientOptsFn ClientOptsFn,
	delta *OCIDeltaTarget,
	pullPolicy v1beta1.ImagePullPolicy,
	podman *client.Podman,
) (bool, error) {
	m.log.Debugf("Scheduled prefetch target already exists: %s", target)
	targetPresent := delta != nil && supportsApplicationImageDigestCheck(resolvedOCIType)
	task := &prefetchTask{
		ociType:              ociType,
		resolvedOCIType:      resolvedOCIType,
		pullPolicy:           pullPolicy,
		targetDigest:         targetDigest,
		applicationTargetKey: applicationImageTargetKeyFor(target, targetDigest, ociType),
		applicationAttempts:  m.applicationAttemptsLocked(target, applicationImageTargetKeyFor(target, targetDigest, ociType), delta),
		clientOptsFn:         clientOptsFn,
		targetPresent:        targetPresent,
		done:                 !targetPresent,
		delta:                delta,
		deltaGeneration:      m.deltaGeneration,
	}
	m.tasks[target] = task
	if delta != nil && delta.Package {
		task.done = true
		m.recordDeltaApplyResultLocked(target, task, deltaApplications(delta), v1beta1.DeviceDeltaApplyOutcomeNotRequired, "")
		return false, nil
	}
	if targetPresent {
		return m.checkCachedApplicationTask(ctx, target, task), nil
	}
	if delta != nil {
		m.recordPresentTargetAsNotUsed(ctx, target, task, podman)
	}
	return false, nil
}

func (m *prefetchManager) recordPresentTargetAsNotUsed(ctx context.Context, target imageRef, task *prefetchTask, podman *client.Podman) {
	applications := deltaApplications(task.delta)
	if task.resolvedOCIType == OCITypePodmanArtifact {
		artifactDigest, err := podman.ArtifactDigest(ctx, target.image)
		if err == nil && validObservedDigest(artifactDigest) {
			m.recordDeltaApplyResultLocked(target, task, applications, v1beta1.DeviceDeltaApplyOutcomeNotUsed, "", artifactDigest)
			return
		}
	}
	m.recordDeltaApplyResultLocked(target, task, applications, v1beta1.DeviceDeltaApplyOutcomeNotUsed, "")
}

// checkUncachedApplicationTask invalidates restored outcomes when a known source
// digest confirms that the requested tag has changed, even if the image is absent.
func (m *prefetchManager) checkUncachedApplicationTask(
	ctx context.Context,
	target imageRef,
	ociType OCIType,
	task *prefetchTask,
	podman *client.Podman,
	skopeo *client.Skopeo,
	opts ...client.ClientOption,
) digestCheckResult {
	result, _ := m.applicationImageDigestMatchesTarget(ctx, target, ociType, task.delta, false, podman, skopeo, &task.resolvedDigest, opts...)
	if result != digestMismatched || task.delta.SourceDigest == "" || task.resolvedDigest == "" {
		return result
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.currentOperationLocked(target, task) {
		m.invalidateApplicationDeltaResultLocked(target, task)
	}
	return result
}

// mu must be held on entry; may be temporarily released during runtime inspection; mu is held on return.
func (m *prefetchManager) checkCachedApplicationTask(ctx context.Context, target imageRef, task *prefetchTask) bool {
	var observedDigests []string
	present := false
	for attempt := 0; attempt < 3; attempt++ {
		if ctx.Err() != nil {
			return true
		}
		snapshot := *task
		canRecord := m.canRecordObservedResultLocked(target, task)
		observedDigests = nil
		present = false
		func() {
			m.mu.Unlock()
			defer m.mu.Lock()
			var opts []client.ClientOption
			if snapshot.clientOptsFn != nil {
				opts = snapshot.clientOptsFn()
			}
			var podman *client.Podman
			if m.podmanFactory != nil {
				podman, _ = m.podmanFactory(target.owner)
			}
			if snapshot.ociType == OCITypeCRIImage {
				if m.cliClients != nil && m.cliClients.CRI() != nil {
					present = m.cliClients.CRI().ImageExists(ctx, target.image, opts...)
				}
			} else if podman != nil {
				present = podman.ImageExists(ctx, target.image) || podman.ArtifactExists(ctx, target.image)
			}
			if present {
				// PullIfNotPresent treats the local runtime image as the cache hit.
				// Resolve its immutable local identity for status, but don't follow a
				// mutable tag to the registry just to decide whether to pull it.
				observedDigests = m.observedCachedImageDigests(ctx, target, snapshot.resolvedOCIType, podman, applicationImageInspection{}, canRecord, opts...)
			}
		}()
		if m.tasks[target] != task || task.delta != snapshot.delta || task.done != snapshot.done ||
			task.err != nil || task.ociType != snapshot.ociType || task.targetDigest != snapshot.targetDigest ||
			task.targetPresent != snapshot.targetPresent || task.applicationTargetKey != snapshot.applicationTargetKey {
			return false
		}
		if task.deltaGeneration != snapshot.deltaGeneration {
			continue
		}
		break
	}
	if !present {
		task.targetPresent = false
		task.done = false
		return true
	}
	task.targetPresent = true
	if task.done && len(observedDigests) == 0 {
		return false
	}
	task.done = true
	// A cache hit satisfies PullIfNotPresent without resolving a mutable tag.
	// Associate NotRequired with the verified local identity, checking the
	// desired digest as well when one was supplied.
	desiredDigest := task.targetDigest
	if desiredDigest == "" {
		_, desiredDigest, _ = strings.Cut(target.image, "@")
	}
	if desiredDigest == "" || slices.Contains(observedDigests, desiredDigest) {
		for _, observedDigest := range observedDigests {
			m.recordDeltaApplyResultLocked(target, task, deltaApplications(task.delta), v1beta1.DeviceDeltaApplyOutcomeNotRequired, "", observedDigest)
		}
	}
	return false
}

// mu must be held. A confirmed content change supersedes results for the same tag.
func (m *prefetchManager) invalidateApplicationDeltaResultLocked(target imageRef, task *prefetchTask) {
	for _, application := range deltaApplications(task.delta) {
		if existing, exists := m.deltaApplyResults[application][target]; exists && existing.targetKey == task.applicationTargetKey {
			delete(m.deltaApplyResults[application], target)
		}
		if m.deltaStatusStore != nil {
			if err := m.deltaStatusStore.ClearApplicationResultForAttempt(task.applicationAttempts[application]); err != nil {
				m.log.Warnf("Failed to invalidate delta result for application %s image %s: %v", application, target.image, err)
			}
		}
	}
}

func supportsApplicationImageDigestCheck(ociType OCIType) bool {
	switch ociType {
	case OCITypePodmanImage, OCITypeCRIImage, OCITypeAuto:
		return true
	default:
		return false
	}
}

func (m *prefetchManager) applicationImageDigestMatchesTarget(
	ctx context.Context,
	target imageRef,
	ociType OCIType,
	delta *OCIDeltaTarget,
	targetPresent bool,
	podman *client.Podman,
	skopeo *client.Skopeo,
	resolvedDigest *string,
	opts ...client.ClientOption,
) (digestCheckResult, applicationImageInspection) {
	application := ""
	hintPresent := false
	sourceDigest := ""
	if delta != nil {
		application = strings.Join(deltaApplications(delta), ",")
		hintPresent = delta.Hint != ""
		sourceDigest = delta.SourceDigest
	}
	var imageInspection applicationImageInspection
	logCheck := func(result digestCheckResult, comparisonDigests []string, desiredDigest, reason string) (digestCheckResult, applicationImageInspection) {
		m.log.Debugf(
			"Application delta digest check: application=%s image=%s ociType=%s targetPresent=%t hintPresent=%t sourceDigest=%q comparisonDigests=%v targetDigest=%q matches=%t reason=%q",
			application, target.image, ociType, targetPresent, hintPresent, sourceDigest, comparisonDigests, desiredDigest, result == digestMatched, reason,
		)
		return result, imageInspection
	}
	if delta == nil || !supportsApplicationImageDigestCheck(ociType) {
		return logCheck(digestUnknown, nil, "", "delta metadata missing or OCI type does not support digest checks")
	}
	if !targetPresent && (delta.Hint != "" || delta.SourceDigest == "") {
		return logCheck(digestMismatched, nil, "", "target is uncached and its hint/source digest cannot verify a cache hit")
	}
	if targetPresent && ociType == OCITypeAuto && (podman == nil || !(podman.ImageExists(ctx, target.image) || podman.ArtifactExists(ctx, target.image))) {
		return logCheck(digestMismatched, nil, "", "cached image is absent from Podman storage")
	}
	if skopeo == nil && ociType == OCITypeAuto {
		return logCheck(digestUnknown, nil, "", "Skopeo client unavailable")
	}

	timeout := m.pullTimeout
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	inspectOpts := append([]client.ClientOption(nil), opts...)
	inspectOpts = append(inspectOpts, client.WithDefaultAuth())

	imageType, ok := m.applicationDeltaCheckImageType(
		ctx, target, ociType, targetPresent, podman, skopeo, inspectOpts...,
	)
	if !ok {
		return logCheck(digestUnknown, nil, "", "could not determine image type")
	}

	imageInspection, ok = m.applicationDeltaCheckCurrentDigests(
		ctx, target, imageType, targetPresent, podman, opts...,
	)
	if !ok {
		return logCheck(digestMismatched, nil, "", "could not inspect local image digest")
	}
	if !targetPresent && len(imageInspection.currentDigests) == 0 && delta.SourceDigest != "" {
		imageInspection.currentDigests = append(imageInspection.currentDigests, delta.SourceDigest)
	}
	if len(imageInspection.currentDigests) == 0 {
		return logCheck(digestUnknown, imageInspection.currentDigests, "", "no current or source digest available")
	}
	if skopeo == nil {
		return logCheck(digestUnknown, imageInspection.currentDigests, "", "Skopeo client unavailable")
	}

	targetDigest := *resolvedDigest
	if targetDigest == "" {
		var err error
		targetDigest, err = skopeo.InspectDigest(ctx, target.image, inspectOpts...)
		if err != nil {
			m.log.Debugf("Could not inspect requested digest for %s while checking delta necessity: %v", target, err)
			return logCheck(digestUnknown, imageInspection.currentDigests, "", fmt.Sprintf("could not inspect requested digest: %v", err))
		}
	}
	targetDigest = applicationImageDigest(targetDigest)
	*resolvedDigest = targetDigest
	if targetDigest == "" {
		m.log.Debugf("Could not inspect requested digest for %s while checking delta necessity: empty digest", target)
		return logCheck(digestUnknown, imageInspection.currentDigests, "", "requested digest inspection returned an empty digest")
	}
	matches := applicationImageDigestsMatch(imageInspection.currentDigests, targetDigest)
	reason := "digest mismatch"
	if matches {
		reason = "digest match"
	}
	result := digestMismatched
	if matches {
		result = digestMatched
	}
	return logCheck(result, imageInspection.currentDigests, targetDigest, reason)
}

func (m *prefetchManager) applicationDeltaCheckImageType(
	ctx context.Context,
	target imageRef,
	ociType OCIType,
	targetPresent bool,
	podman *client.Podman,
	skopeo *client.Skopeo,
	opts ...client.ClientOption,
) (OCIType, bool) {
	imageType := ociType
	if imageType == OCITypeAuto {
		manifest, err := skopeo.InspectManifest(ctx, target.image, opts...)
		if err != nil {
			m.log.Debugf("Could not inspect target type for %s while checking delta necessity: %v", target, err)
			return "", false
		}
		imageType, err = detectOCIType(manifest)
		if err != nil {
			m.log.Debugf("Could not detect OCI type for %s while checking delta necessity: %v", target, err)
			return "", false
		}
		if imageType != OCITypePodmanImage {
			m.log.Debugf("Target %s resolved to OCI type %s while checking delta necessity; expected PodmanImage", target, imageType)
			return "", false
		}
	}
	return imageType, true
}

func (m *prefetchManager) applicationDeltaCheckCurrentDigests(
	ctx context.Context,
	target imageRef,
	imageType OCIType,
	targetPresent bool,
	podman *client.Podman,
	opts ...client.ClientOption,
) (applicationImageInspection, bool) {
	if !targetPresent {
		return applicationImageInspection{}, true
	}
	switch imageType {
	case OCITypePodmanImage:
		if podman == nil {
			return applicationImageInspection{}, false
		}
		metadata, err := podman.ImageMetadata(ctx, target.image)
		if err != nil {
			m.log.Debugf("Could not inspect local digests for %s while checking delta necessity: %v", target, err)
			return applicationImageInspection{}, false
		}
		imageInspection := applicationImageInspection{currentDigests: metadata.Digests(), runtimeImageRef: metadata.ID, podmanMetadata: &metadata}
		if len(imageInspection.currentDigests) == 0 {
			m.log.Debugf("Local digest inspection returned no digests for %s while checking delta necessity", target)
			return imageInspection, true
		}
		return imageInspection, true
	case OCITypeCRIImage:
		if m.cliClients == nil || m.cliClients.CRI() == nil {
			return applicationImageInspection{}, false
		}
		metadata, err := m.cliClients.CRI().ImageMetadata(ctx, target.image, opts...)
		if err != nil {
			m.log.Debugf("Could not inspect local CRI digests for %s while checking delta necessity: %v", target, err)
			return applicationImageInspection{}, false
		}
		return applicationImageInspection{currentDigests: metadata.RepoDigests, runtimeImageRef: metadata.ID}, true
	default:
		return applicationImageInspection{}, false
	}
}

func applicationImageDigestsMatch(currentDigests []string, targetDigest string) bool {
	for _, currentDigest := range currentDigests {
		if currentDigest := applicationImageDigest(currentDigest); currentDigest != "" && currentDigest == targetDigest {
			return true
		}
	}
	return false
}

func applicationImageDigest(digestOrReference string) string {
	if index := strings.LastIndex(digestOrReference, "@"); index >= 0 {
		digestOrReference = digestOrReference[index+1:]
	}
	return normalizeDigest(digestOrReference)
}

func (m *prefetchManager) removeTask(target imageRef) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.tasks, target)
}

func (m *prefetchManager) IsReady(ctx context.Context) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	if ctx.Err() != nil {
		return false
	}

	for _, task := range m.tasks {
		if !task.done || task.err != nil {
			return false
		}
	}

	return true
}

// cleanupStaleTasks removes tasks not in the provided target set
// caller must hold m.mu lock
func (m *prefetchManager) hasChangedTasks(targets map[imageRef]OCIPullTarget) bool {
	for ref, task := range m.tasks {
		target, exists := targets[ref]
		if !exists || !samePrefetchTarget(task, target) {
			return true
		}
	}
	return false
}

func (m *prefetchManager) cleanupStaleTasks(seenTargets map[imageRef]struct{}) {
	var removed int
	for ref, task := range m.tasks {
		if _, exists := seenTargets[ref]; exists {
			continue
		}
		if task.cancelFn != nil {
			task.cancelFn()
		}
		delete(m.tasks, ref)
		removed++
	}

	if removed > 0 {
		m.log.Debugf("Cleaned up %d stale prefetch tasks", removed)
	}
}

func (m *prefetchManager) cleanupChangedTasks(targets map[imageRef]OCIPullTarget) {
	for ref, task := range m.tasks {
		target, exists := targets[ref]
		if !exists || samePrefetchTarget(task, target) {
			continue
		}
		if task.cancelFn != nil {
			task.cancelFn()
		}
		delete(m.tasks, ref)
	}
}

func samePrefetchTarget(task *prefetchTask, target OCIPullTarget) bool {
	return task != nil && task.ociType == target.Type && task.targetDigest == target.Digest && sameDeltaTarget(task.delta, target.Delta)
}

func sameDeltaTarget(first, second *OCIDeltaTarget) bool {
	if first == nil || second == nil {
		return first == second
	}
	return first.Package == second.Package && first.Hint == second.Hint &&
		first.SourceDigest == second.SourceDigest &&
		first.Application == second.Application &&
		slices.Equal(first.Applications, second.Applications)
}

func (m *prefetchManager) Cleanup() {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.collectors = nil
	if len(m.tasks) == 0 {
		return
	}

	m.log.Debugf("Prefetch cleanup: canceling %d active tasks", len(m.tasks))

	for _, task := range m.tasks {
		if task.cancelFn != nil {
			task.cancelFn()
		}
	}

	m.tasks = make(map[imageRef]*prefetchTask)

	for {
		select {
		case <-m.queue:
			// discard
		default:
			return
		}
	}
}

func (m *prefetchManager) cleanupPartialLayers(ctx context.Context, user v1beta1.Username) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	podman, err := m.podmanFactory(user)
	if err != nil {
		return fmt.Errorf("creating podman client: %w", err)
	}

	tmpDir, err := podman.GetImageCopyTmpDir(ctx)
	if err != nil {
		m.log.Warnf("Failed to get image copy tmpdir: %v", err)
		return nil
	}

	if tmpDir == "" {
		m.log.Warn("Image copy tmpdir is empty, skipping cleanup")
		return nil
	}

	entries, err := m.readWriter.ReadDir(tmpDir)
	if err != nil {
		return fmt.Errorf("reading tmp directory %s: %w", tmpDir, err)
	}

	const prefix = "container_images_storage"
	var dirs []fs.DirEntry

	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), prefix) {
			dirs = append(dirs, entry)
		}
	}

	if len(dirs) == 0 {
		return nil
	}

	var removed int
	for _, entry := range dirs {
		dirPath := filepath.Join(tmpDir, entry.Name())
		if err := m.readWriter.RemoveAll(dirPath); err != nil {
			m.log.Warnf("Failed to remove %s: %v", entry.Name(), err)
			continue
		}
		removed++
	}

	if removed > 0 {
		m.log.Infof("Cleaned up %d dangling OCI layer directories", removed)
	}

	return nil
}

func (m *prefetchManager) StatusMessage(ctx context.Context) string {
	status := m.status(ctx)
	pending := status.PendingImages
	retrying := status.RetryingImages
	total := status.TotalImages
	completed := total - len(pending) - len(retrying)

	switch {
	case total == 0:
		return "No images to prefetch"
	case completed == total:
		return fmt.Sprintf("All %d images ready", total)
	case len(retrying) > 0:
		displayRetrying := retrying
		if len(retrying) > 3 {
			displayRetrying = retrying[:3]
		}
		retryingStr := strings.Join(displayRetrying, ", ")
		remaining := len(retrying) - len(displayRetrying) + len(pending)
		if remaining > 0 {
			return fmt.Sprintf("%d/%d images complete, retrying: %s, and %d more pending",
				completed, total, retryingStr, remaining)
		}
		return fmt.Sprintf("%d/%d images complete, retrying: %s", completed, total, retryingStr)
	case len(pending) <= 3:
		return fmt.Sprintf("%d/%d images complete, pending: %s", completed, total, strings.Join(pending, ", "))
	default:
		return fmt.Sprintf("%d/%d images complete, pending: %s and %d more",
			completed, total, strings.Join(pending[:3], ", "), len(pending)-3)
	}
}

// Status preserves image-associated results selected by the application collector.
// Desired prefetch outcomes are scheduling metadata, not evidence for emitted images.
func (m *prefetchManager) Status(_ context.Context, deviceStatus *v1beta1.DeviceStatus, _ ...status.CollectorOpt) error {
	if m.deltaStatusStore == nil {
		for i := range deviceStatus.Applications {
			deviceStatus.Applications[i].LastDelta = nil
		}
	}
	return nil
}

func (m *prefetchManager) status(ctx context.Context) PrefetchStatus {
	m.mu.Lock()
	defer m.mu.Unlock()

	var pendingImages []string
	var retryingImages []string
	for image, task := range m.tasks {
		if ctx.Err() != nil {
			return PrefetchStatus{}
		}
		if errors.IsRetryable(task.err) {
			retryingImages = append(retryingImages, fmt.Sprintf("%s: %s", image.image, log.Truncate(errors.Reason(task.err), 100)))
		} else if !task.done {
			pendingImages = append(pendingImages, image.image)
		}
	}

	// sort for consistent ordering
	slices.Sort(pendingImages)
	slices.Sort(retryingImages)

	prefetchStatus := PrefetchStatus{
		TotalImages:    len(m.tasks),
		PendingImages:  pendingImages,
		RetryingImages: retryingImages,
	}
	if !m.deltaTargetsScheduled {
		return prefetchStatus
	}
	prefetchStatus.DeltaApplyStatuses = m.applicationDeltaApplyStatuses()
	return prefetchStatus
}

func (m *prefetchManager) applicationDeltaApplyStatuses() map[string]v1beta1.DeviceDeltaApplyStatus {
	if m.deltaStatusStore != nil {
		return m.persistedApplicationDeltaApplyStatuses()
	}

	applicationResults, pendingApplications := m.collectApplicationDeltaApplyResults()
	statuses := make(map[string]v1beta1.DeviceDeltaApplyStatus, len(applicationResults))
	for application, results := range applicationResults {
		if _, pending := pendingApplications[application]; pending {
			continue
		}
		if deltaStatus := aggregateApplicationDeltaApplyResults(results); deltaStatus != nil {
			statuses[application] = *deltaStatus
		}
	}
	return statuses
}

func (m *prefetchManager) collectApplicationDeltaApplyResults() (
	map[string]map[string]applicationDeltaApplyResult,
	map[string]struct{},
) {
	applicationResults := make(map[string]map[string]applicationDeltaApplyResult)
	pendingApplications := make(map[string]struct{})
	for application, expectedTargets := range m.deltaAppTargetKeys {
		for targetID, targetKey := range expectedTargets {
			result, exists := m.currentApplicationDeltaResult(application, targetID, targetKey)
			if !exists {
				pendingApplications[application] = struct{}{}
				continue
			}
			if applicationResults[application] == nil {
				applicationResults[application] = make(map[string]applicationDeltaApplyResult)
			}
			applicationResults[application][targetID] = result
		}
	}
	return applicationResults, pendingApplications
}

func (m *prefetchManager) persistedApplicationDeltaApplyStatuses() map[string]v1beta1.DeviceDeltaApplyStatus {
	statuses := make(map[string]v1beta1.DeviceDeltaApplyStatus, len(m.deltaAppSpecKeys))
	for application, specKey := range m.deltaAppSpecKeys {
		results, complete := m.persistedApplicationDeltaResults(application, specKey)
		if !complete {
			continue
		}
		if deltaStatus := aggregateApplicationDeltaApplyResults(results); deltaStatus != nil {
			statuses[application] = *deltaStatus
		}
	}
	return statuses
}

func (m *prefetchManager) persistedApplicationDeltaResults(
	application, specKey string,
) (map[string]applicationDeltaApplyResult, bool) {
	expectedTargets, exists := m.deltaAppTargetKeys[application]
	if !exists || len(expectedTargets) == 0 {
		return nil, false
	}
	// Packages have neutral scheduling results but never enter workload history.
	workloadTargets := maps.Clone(expectedTargets)
	for targetID := range workloadTargets {
		if task := m.tasks[m.deltaTargetRefs[targetID]]; task != nil && task.delta != nil && task.delta.Package {
			delete(workloadTargets, targetID)
		}
	}
	snapshot := m.deltaStatusStore.ApplicationSnapshot(application)
	if snapshot == nil || snapshot.SpecKey != specKey || snapshot.Lifetime != m.deltaAppLifetimes[application] || !sameStringMap(snapshot.Targets, workloadTargets) {
		return nil, false
	}

	results := make(map[string]applicationDeltaApplyResult, len(expectedTargets))
	for targetID, targetKey := range expectedTargets {
		result, hasResult := applicationDeltaResultFromSnapshot(snapshot, targetID, targetKey)
		if !hasResult {
			result, hasResult = m.currentApplicationDeltaResult(application, targetID, targetKey)
		}
		if !hasResult {
			return nil, false
		}
		results[targetID] = result
	}
	return results, true
}

func applicationDeltaResultFromSnapshot(
	snapshot *deltastatus.ApplicationSnapshot,
	targetID, targetKey string,
) (applicationDeltaApplyResult, bool) {
	persisted, exists := snapshot.Results[targetID]
	if !exists || persisted.TargetKey != targetKey {
		return applicationDeltaApplyResult{}, false
	}
	fallbackReason := ""
	if persisted.Status.FallbackReason != nil {
		fallbackReason = *persisted.Status.FallbackReason
	}
	return applicationDeltaApplyResult{
		outcome:        persisted.Status.Outcome,
		fallbackReason: fallbackReason,
		targetKey:      targetKey,
	}, true
}

func (m *prefetchManager) currentApplicationDeltaResult(
	application, targetID, targetKey string,
) (applicationDeltaApplyResult, bool) {
	ref, exists := m.deltaTargetRefs[targetID]
	if !exists {
		return applicationDeltaApplyResult{}, false
	}
	result, exists := m.deltaApplyResults[application][ref]
	if !exists || result.targetKey != targetKey {
		return applicationDeltaApplyResult{}, false
	}
	if task, exists := m.tasks[ref]; exists && task.applicationTargetKey != targetKey {
		return applicationDeltaApplyResult{}, false
	}
	return result, true
}

func aggregateApplicationDeltaApplyResults(results map[string]applicationDeltaApplyResult) *v1beta1.DeviceDeltaApplyStatus {
	statuses := make([]v1beta1.DeviceDeltaApplyStatus, 0, len(results))
	for _, result := range results {
		statuses = append(statuses, applicationDeltaApplyStatus(result.outcome, result.fallbackReason))
	}
	return deltastatus.AggregateApplicationResults(statuses)
}
