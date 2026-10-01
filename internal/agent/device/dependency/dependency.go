package dependency

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"iter"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/containers/image/v5/docker/reference"
	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/deltastatus"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/agent/device/resource"
	"github.com/flightctl/flightctl/internal/agent/device/status"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/internal/util/validation"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/poll"
)

const (
	maxQueueSize             = 10
	ociDeltaArtifactType     = "application/vnd.io.github.containers.oci-delta.v1"
	ociDeltaSourceAnnotation = "io.github.containers.delta.source"
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

func selectApplicationDeltaCandidate(targetImage string, delta *OCIDeltaTarget, index *client.OCIIndex) string {
	if delta == nil {
		return ""
	}
	if delta.Hint != "" {
		return delta.Hint
	}
	if delta.SourceDigest == "" || index == nil {
		return ""
	}
	repo := imageRepository(targetImage)
	if repo == "" {
		return ""
	}
	for _, ref := range index.Manifests {
		if ref.ArtifactType != ociDeltaArtifactType || ref.Digest == "" {
			continue
		}
		if normalizeDigest(ref.Annotations[ociDeltaSourceAnnotation]) == normalizeDigest(delta.SourceDigest) {
			return repo + "@" + ref.Digest
		}
	}
	return ""
}

func imageRepository(image string) string {
	matches := validation.OciImageReferenceRegexp.FindStringSubmatch(image)
	if len(matches) == 0 {
		return ""
	}
	return matches[1]
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
	ClientOptsFn ClientOptsFn // Called only when pulling is actually needed
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
	image string
	owner v1beta1.Username
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

	deltaDesired            *v1beta1.DeviceSpec
	deltaGeneration         uint64
	deltaApplyResults       map[string]map[imageRef]applicationDeltaApplyResult
	deltaStatusStore        *deltastatus.Store
	applicationNameResolver ApplicationNameResolver
	deltaAppSpecKeys        map[string]string
	deltaAppTargetKeys      map[string]map[string]string
	deltaTargetsScheduled   bool
}

type prefetchTask struct {
	clientOptsFn         ClientOptsFn
	ociType              OCIType
	targetDigest         string
	applicationTargetKey string
	delta                *OCIDeltaTarget
	deltaGeneration      uint64
	err                  error
	done                 bool
	cancelFn             context.CancelFunc
}

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
	appSpecKeys := make(map[string]string)
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
			}
		}
	}

	allTargets := make(OCIPullTargetsByUser)
	var requeueNeeded bool
	m.mu.Lock()
	if m.deltaDesired != desired || !sameStringMap(m.deltaAppSpecKeys, appSpecKeys) {
		m.deltaDesired = desired
		m.deltaGeneration++
		m.deltaApplyResults = make(map[string]map[imageRef]applicationDeltaApplyResult)
	}
	m.deltaAppSpecKeys = appSpecKeys
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
			image: target.Reference,
			owner: user,
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
		if err := m.deltaStatusStore.ReconcileApplicationTargets(appTargetKeys, !requeueNeeded); err != nil {
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
	m.deltaAppTargetKeys = appTargetKeys
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
			return fmt.Errorf("%w: insufficient disk storage space, please clear storage", errors.ErrCriticalResourceAlert)
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
		targetID := deltastatus.Fingerprint(string(ref.owner), ref.image)
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
		if snapshot == nil || snapshot.SpecKey != m.deltaAppSpecKeys[application] {
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
	m.mu.Unlock()

	// these operations are intentionally serial.  consider whether the
	// networking and disk I/O overhead of async pulls would be worth changing
	// before we do.
	retries := 0
	for {
		if ctx.Err() != nil {
			m.setResult(target, fmt.Errorf("pulling oci target %w: %w", errors.WithElement(target.image), ctx.Err()))
			m.log.Warnf("Context error pulling oci target: %s", target)
			return
		}
		if err := m.pull(ctx, target, task); err != nil {
			if errors.IsRetryable(err) {
				retries++
				m.log.Warnf("Retrying prefetch for %s (attempt %d): %v", target, retries+1, err)
				m.setError(target, fmt.Errorf("%w: %w", errors.WithElement(target.image), err))

				// cleanup file system from partial layer pulls
				if err := m.cleanupPartialLayers(ctx, target.owner); err != nil {
					m.log.Warnf("cleanup failed: %v", err)
				} else {
					m.log.Debug("cleanup completed successfully")
				}

				select {
				case <-time.After(poll.CalculateBackoffDelay(m.pollConfig, retries)):
					continue
				case <-ctx.Done():
					m.log.Warnf("Prefetch loop canceled while waiting to retry: %v", ctx.Err())
					return
				}
			}
			m.setResult(target, fmt.Errorf("pulling oci target %w: %w", errors.WithElement(target.image), err))
			return
		}
		// success
		m.setResult(target, nil)
		return
	}
}

func (m *prefetchManager) pull(ctx context.Context, target imageRef, task *prefetchTask) error {
	ociType := task.ociType

	podman, err := m.podmanFactory(target.owner)
	if err != nil {
		return fmt.Errorf("creating podman client: %w", err)
	}

	skopeo, err := m.skopeoFactory(target.owner)
	if err != nil {
		return fmt.Errorf("creating skopeo client: %w", err)
	}

	// Evaluate client options lazily at pull time
	opts := []client.ClientOption{client.Timeout(m.pullTimeout)}
	if task.clientOptsFn != nil {
		opts = append(opts, task.clientOptsFn()...)
	}

	switch ociType {
	case OCITypePodmanImage:
		err = m.pullApplicationImage(ctx, target, task, podman, skopeo, opts...)
	case OCITypeCRIImage:
		err = m.pullCRIImage(ctx, target, task, skopeo, opts...)
	case OCITypePodmanArtifact:
		_, err = podman.PullArtifact(ctx, target.image, opts...)
	case OCITypeHelmChart:
		err = m.cliClients.Helm().Resolve(ctx, target.image, opts...)
	case OCITypeAuto:
		m.log.Debugf("Auto-detecting OCI type for %s", target)
		manifest, inspectErr := skopeo.InspectManifest(ctx, target.image, opts...)
		if inspectErr != nil {
			return fmt.Errorf("inspecting manifest for auto-detection: %w", inspectErr)
		}

		detectedType, detectErr := detectOCIType(manifest)
		if detectErr != nil {
			return fmt.Errorf("detecting OCI type: %w", detectErr)
		}

		m.log.Infof("Detected OCI type for %s: %s", target, detectedType)

		switch detectedType {
		case OCITypePodmanImage:
			err = m.pullApplicationImage(ctx, target, task, podman, skopeo, opts...)
		case OCITypePodmanArtifact:
			_, err = podman.PullArtifact(ctx, target.image, opts...)
		default:
			return fmt.Errorf("unexpected detected OCI type: %s", detectedType)
		}
	default:
		return fmt.Errorf("invalid oci type %s", ociType)
	}
	return err
}

func (m *prefetchManager) pullCRIImage(ctx context.Context, target imageRef, task *prefetchTask, skopeo *client.Skopeo, opts ...client.ClientOption) error {
	cri := m.cliClients.CRI()
	if task.delta == nil {
		_, err := cri.Pull(ctx, target.image, opts...)
		return err
	}
	if m.ociDelta == nil {
		// CRI imports use the rootful client that targets the runtime's storage.
		// The per-user factory serves Podman-backed application storage only.
		_, err := cri.Pull(ctx, target.image, opts...)
		m.recordDeltaNotUsed(target, task)
		return err
	}
	deltaOpts := append([]client.ClientOption(nil), opts...)
	deltaOpts = append(deltaOpts, client.WithDefaultAuth())

	candidate := task.delta.Hint
	if candidate == "" && task.delta.SourceDigest != "" {
		index, err := skopeo.ListReferrers(ctx, target.image, deltaOpts...)
		if err != nil {
			m.log.Debugf("application delta referrers unavailable for %s: %v", target.image, err)
		} else {
			candidate = selectApplicationDeltaCandidate(target.image, task.delta, index)
		}
	}
	if candidate == "" {
		_, err := cri.Pull(ctx, target.image, opts...)
		m.recordDeltaNotUsed(target, task)
		return err
	}
	if err := validateApplicationDeltaCandidate(candidate); err != nil {
		return m.applicationCRIDeltaFallback(ctx, target, task, err, opts...)
	}
	if _, _, digestPinned, err := normalizedCRIImageReferences(target.image); err != nil {
		return m.applicationCRIDeltaFallback(ctx, target, task, err, opts...)
	} else if digestPinned {
		// The CRI reconstruction path may materialize a manifest with a digest
		// different from the immutable requested reference, so preserve the
		// reference's exact content by taking the full-pull path. Podman imports
		// against the full reference and then refreshes that same reference.
		return m.applicationCRIDeltaFallback(
			ctx,
			target,
			task,
			fmt.Errorf("delta reconstruction may change the manifest digest of digest-pinned image %s", target.image),
			opts...,
		)
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
	if err := skopeo.Copy(ctx, "docker://"+candidate, "oci-archive:"+deltaFile, deltaOpts...); err != nil {
		return m.applicationCRIDeltaFallback(ctx, target, task, err, opts...)
	}

	switch runtimeInfo.Name {
	case client.CRIRuntimeCRIO:
		if err := m.ociDelta.Import(ctx, deltaFile, target.image); err != nil {
			return m.applicationCRIDeltaFallback(ctx, target, task, err, opts...)
		}
	case client.CRIRuntimeContainerd:
		if err := m.applyContainerdImageDelta(ctx, target.image, candidate, deltaFile, tmpDir, runtimeInfo.Endpoint, task.delta.SourceDigest, skopeo, deltaOpts...); err != nil {
			return m.applicationCRIDeltaFallback(ctx, target, task, err, opts...)
		}
	}

	if !cri.ImageExists(ctx, target.image, opts...) {
		return m.applicationCRIDeltaFallback(ctx, target, task, fmt.Errorf("delta import did not make %s available in the CRI runtime", target.image), opts...)
	}

	// Pull the registry reference after importing the reconstructed image so
	// CRI records the registry's canonical manifest digest. The image blobs are
	// already in the runtime storage from delta reconstruction and can be reused
	// by digest.
	if _, err := cri.Pull(ctx, target.image, opts...); err != nil {
		return m.applicationCRIDeltaFallback(ctx, target, task,
			fmt.Errorf("refresh registry manifest after delta import: %w", err), opts...)
	}

	m.recordDeltaApplied(target, task)
	m.log.Infof("Applied OCI delta for application image %s from %s", target.image, candidate)
	m.log.Infof("Refreshed registry reference for application image %s after OCI delta import", target.image)
	return nil
}

func (m *prefetchManager) applyContainerdImageDelta(
	ctx context.Context,
	targetImage, deltaImage, deltaFile, tmpDir, endpoint, sourceDigest string,
	skopeo *client.Skopeo,
	opts ...client.ClientOption,
) error {
	if sourceDigest == "" {
		manifest, err := skopeo.InspectManifest(ctx, deltaImage, opts...)
		if err != nil {
			return fmt.Errorf("inspect application delta manifest: %w", err)
		}
		sourceDigest = manifest.Annotations[ociDeltaSourceAnnotation]
	}
	sourceDigest = normalizeDigest(sourceDigest)
	if sourceDigest == "" {
		return fmt.Errorf("application delta does not identify its source image digest")
	}

	repository, targetRef, _, err := normalizedCRIImageReferences(targetImage)
	if err != nil {
		return err
	}
	sourceRef := repository + "@" + sourceDigest

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

func (m *prefetchManager) applicationCRIDeltaFallback(ctx context.Context, target imageRef, task *prefetchTask, deltaErr error, opts ...client.ClientOption) error {
	m.recordDeltaFallback(target, task, deltaErr)
	if _, err := m.cliClients.CRI().Pull(ctx, target.image, opts...); err != nil {
		return fmt.Errorf("application delta failed: %w; full CRI image pull failed: %w", deltaErr, err)
	}
	return nil
}

func (m *prefetchManager) pullApplicationImage(ctx context.Context, target imageRef, task *prefetchTask, podman *client.Podman, skopeo *client.Skopeo, opts ...client.ClientOption) error {
	if task.delta == nil {
		_, err := podman.Pull(ctx, target.image, opts...)
		return err
	}
	if m.ociDelta == nil && m.ociDeltaFactory == nil {
		_, err := podman.Pull(ctx, target.image, opts...)
		m.recordDeltaNotUsed(target, task)
		return err
	}
	ociDelta, err := m.applicationOCIDelta(target.owner)
	if err != nil {
		return m.applicationDeltaFallback(ctx, target, task, podman, err, opts...)
	}
	deltaOpts := append([]client.ClientOption(nil), opts...)
	deltaOpts = append(deltaOpts, client.WithDefaultAuth())

	candidate := task.delta.Hint
	if candidate == "" && task.delta.SourceDigest != "" {
		index, err := skopeo.ListReferrers(ctx, target.image, deltaOpts...)
		if err != nil {
			m.log.Debugf("application delta referrers unavailable for %s: %v", target.image, err)
		} else {
			candidate = selectApplicationDeltaCandidate(target.image, task.delta, index)
		}
	}

	if candidate == "" {
		_, err := podman.Pull(ctx, target.image, opts...)
		m.recordDeltaNotUsed(target, task)
		return err
	}
	if err := validateApplicationDeltaCandidate(candidate); err != nil {
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
	if err := skopeo.Copy(ctx, "docker://"+candidate, "oci-archive:"+deltaFile, deltaOpts...); err != nil {
		return m.applicationDeltaFallback(ctx, target, task, podman, err, opts...)
	}
	if err := ociDelta.Import(ctx, deltaFile, target.image); err != nil {
		return m.applicationDeltaFallback(ctx, target, task, podman, err, opts...)
	}

	m.log.Infof("Applied OCI delta for application image %s from %s", target.image, candidate)

	// Pull the registry reference after importing the reconstructed image so
	// container storage records the registry's canonical manifest digest. The
	// blobs already present from delta reconstruction are reused by digest.
	if _, err := podman.Pull(ctx, target.image, opts...); err != nil {
		return m.applicationDeltaFallback(ctx, target, task, podman,
			fmt.Errorf("refresh registry manifest after delta import: %w", err), opts...)
	}
	m.recordDeltaApplied(target, task)
	m.log.Infof("Refreshed registry reference for application image %s after OCI delta import", target.image)
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
	if _, err := podman.Pull(ctx, target.image, opts...); err != nil {
		return fmt.Errorf("application delta failed: %w; full image pull failed: %w", deltaErr, err)
	}
	return nil
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

func validateApplicationDeltaCandidate(candidate string) error {
	if errs := validation.ValidateOciImageReference(&candidate, "application.deltaImage"); len(errs) > 0 {
		return fmt.Errorf("invalid application delta image reference %q: %w", candidate, errs[0])
	}
	return nil
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

func (m *prefetchManager) recordDeltaNotUsed(target imageRef, task *prefetchTask) {
	m.recordDeltaApplyResult(target, task, v1beta1.DeviceDeltaApplyOutcomeNotUsed, "")
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

func (m *prefetchManager) recordDeltaApplyResultLocked(target imageRef, task *prefetchTask, applications []string, outcome v1beta1.DeviceDeltaApplyOutcomeType, fallbackReason string) {
	if task == nil || task.delta == nil || len(applications) == 0 {
		return
	}

	current, exists := m.tasks[target]
	if !exists || current != task || task.deltaGeneration != m.deltaGeneration {
		return
	}
	if m.deltaApplyResults == nil {
		m.deltaApplyResults = make(map[string]map[imageRef]applicationDeltaApplyResult)
	}
	for _, application := range applications {
		targetID := deltastatus.Fingerprint(string(target.owner), target.image)
		persistedTargetKey := ""
		if m.deltaStatusStore != nil && m.deltaAppTargetKeys != nil {
			persistedTargetKey = m.deltaAppTargetKeys[application][targetID]
			if persistedTargetKey != "" && persistedTargetKey != task.applicationTargetKey {
				continue
			}
		}
		results := m.deltaApplyResults[application]
		if results == nil {
			results = make(map[imageRef]applicationDeltaApplyResult)
			m.deltaApplyResults[application] = results
		}
		if existing, exists := results[target]; outcome == v1beta1.DeviceDeltaApplyOutcomeNotUsed && exists {
			if existing.targetKey == task.applicationTargetKey {
				continue
			}
		}
		result := applicationDeltaApplyResult{
			outcome:        outcome,
			fallbackReason: fallbackReason,
			targetKey:      task.applicationTargetKey,
		}
		results[target] = result
		if m.deltaStatusStore != nil && persistedTargetKey == task.applicationTargetKey {
			status := v1beta1.DeviceDeltaApplyStatus{Outcome: outcome}
			if fallbackReason != "" {
				reason := fallbackReason
				status.FallbackReason = &reason
			}
			if err := m.deltaStatusStore.RecordApplicationResult(
				application,
				m.deltaAppSpecKeys[application],
				targetID,
				task.applicationTargetKey,
				status,
			); err != nil {
				m.log.Warnf("Failed to persist delta result for application %s image %s: %v", application, target.image, err)
			}
		}
	}
}

func (m *prefetchManager) setResult(target imageRef, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	task, ok := m.tasks[target]
	if !ok {
		m.log.Debugf("Task for %s no longer exists, skipping result update", target)
		return
	}
	task.err = err
	task.done = true
}

func (m *prefetchManager) setError(target imageRef, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	task, ok := m.tasks[target]
	if !ok {
		m.log.Debugf("Task for %s no longer exists, skipping error update", target)
		return
	}
	task.err = err
}

func (m *prefetchManager) Schedule(ctx context.Context, targets OCIPullTargetsByUser) error {
	for user, target := range targets.Iter() {
		ref := imageRef{image: target.Reference, owner: user}
		if err := m.schedule(ctx, ref, target.Type, target.Digest, target.ClientOptsFn, target.Delta); err != nil {
			return fmt.Errorf("prefetch schedule %w: %w", errors.WithElement(target.Reference), err)
		}
	}

	return nil
}

func (m *prefetchManager) schedule(ctx context.Context, target imageRef, ociType OCIType, targetDigest string, clientOptsFn ClientOptsFn, delta *OCIDeltaTarget) error {
	needsQueue, err := m.prepareTask(ctx, target, ociType, targetDigest, clientOptsFn, delta)
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

func (m *prefetchManager) prepareTask(ctx context.Context, target imageRef, ociType OCIType, targetDigest string, clientOptsFn ClientOptsFn, delta *OCIDeltaTarget) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if existing, exists := m.tasks[target]; exists {
		// Reuse a matching task across update generations. If it already
		// completed successfully, the target is present and no delta was used
		// for this generation unless a restored result already says otherwise.
		existing.deltaGeneration = m.deltaGeneration
		if existing.done && existing.err == nil && delta != nil {
			m.recordDeltaApplyResultLocked(
				target,
				existing,
				deltaApplications(delta),
				v1beta1.DeviceDeltaApplyOutcomeNotUsed,
				"",
			)
		}
		return false, nil
	}

	podman, err := m.podmanFactory(target.owner)
	if err != nil {
		return false, fmt.Errorf("creating podman client: %w", err)
	}

	var targetExists bool
	switch ociType {
	case OCITypePodmanImage:
		targetExists = podman.ImageExists(ctx, target.image)
	case OCITypeCRIImage:
		// CRI needs config options for existence check
		var opts []client.ClientOption
		if clientOptsFn != nil {
			opts = clientOptsFn()
		}
		targetExists = m.cliClients.CRI().ImageExists(ctx, target.image, opts...)
	case OCITypePodmanArtifact:
		targetExists = podman.ArtifactExists(ctx, target.image)
	case OCITypeAuto:
		targetExists = podman.ImageExists(ctx, target.image) || podman.ArtifactExists(ctx, target.image)
	case OCITypeHelmChart:
		resolved, err := m.cliClients.Helm().IsResolved(target.image)
		if err != nil {
			return false, fmt.Errorf("check helm chart resolved: %w", err)
		}
		targetExists = resolved
	default:
		return false, fmt.Errorf("invalid oci type %s", ociType)
	}

	if targetExists {
		m.log.Debugf("Scheduled prefetch target already exists: %s", target)
		task := &prefetchTask{
			ociType:              ociType,
			targetDigest:         targetDigest,
			applicationTargetKey: applicationImageTargetKeyFor(target, targetDigest, ociType),
			done:                 true,
			delta:                delta,
			deltaGeneration:      m.deltaGeneration,
		}
		m.tasks[target] = task
		if delta != nil {
			m.recordDeltaApplyResultLocked(
				target,
				task,
				deltaApplications(delta),
				v1beta1.DeviceDeltaApplyOutcomeNotUsed,
				"",
			)
		}
		return false, nil
	}

	task := &prefetchTask{
		ociType:              ociType,
		targetDigest:         targetDigest,
		applicationTargetKey: applicationImageTargetKeyFor(target, targetDigest, ociType),
		clientOptsFn:         clientOptsFn,
		delta:                delta,
		deltaGeneration:      m.deltaGeneration,
	}
	m.tasks[target] = task
	return true, nil
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
	return first.Hint == second.Hint &&
		first.SourceDigest == second.SourceDigest &&
		first.Application == second.Application &&
		slices.Equal(first.Applications, second.Applications)
}

func (m *prefetchManager) Cleanup() {
	m.mu.Lock()
	defer m.mu.Unlock()

	if len(m.tasks) == 0 {
		return
	}

	m.log.Debugf("Prefetch cleanup: canceling %d active tasks", len(m.tasks))

	for _, task := range m.tasks {
		if task.cancelFn != nil {
			task.cancelFn()
		}
	}

	m.collectors = nil
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

// Status adds application delta results collected by prefetch tasks.
func (m *prefetchManager) Status(ctx context.Context, deviceStatus *v1beta1.DeviceStatus, _ ...status.CollectorOpt) error {
	prefetchStatus := m.status(ctx)

	for i := range deviceStatus.Applications {
		deviceStatus.Applications[i].LastDelta = nil
		deltaStatus, exists := prefetchStatus.DeltaApplyStatuses[deviceStatus.Applications[i].Name]
		if !exists {
			continue
		}
		applicationDeltaStatus := deltaStatus
		deviceStatus.Applications[i].LastDelta = &applicationDeltaStatus
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
	map[string]map[imageRef]applicationDeltaApplyResult,
	map[string]struct{},
) {
	applicationResults := make(map[string]map[imageRef]applicationDeltaApplyResult)
	pendingApplications := make(map[string]struct{})
	if m.deltaAppTargetKeys == nil {
		for target, task := range m.tasks {
			if task.delta == nil {
				continue
			}
			for _, application := range deltaApplications(task.delta) {
				result, exists := m.deltaApplyResults[application][target]
				if !exists || (result.targetKey != "" && task.applicationTargetKey != "" && result.targetKey != task.applicationTargetKey) {
					pendingApplications[application] = struct{}{}
					continue
				}
				if applicationResults[application] == nil {
					applicationResults[application] = make(map[imageRef]applicationDeltaApplyResult)
				}
				applicationResults[application][target] = result
			}
		}
		return applicationResults, pendingApplications
	}

	for application, expectedTargets := range m.deltaAppTargetKeys {
		for targetID, targetKey := range expectedTargets {
			result, exists := m.currentApplicationDeltaResult(application, targetID, targetKey)
			if !exists {
				pendingApplications[application] = struct{}{}
				continue
			}
			if applicationResults[application] == nil {
				applicationResults[application] = make(map[imageRef]applicationDeltaApplyResult)
			}
			applicationResults[application][imageRef{image: targetID}] = result
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
) (map[imageRef]applicationDeltaApplyResult, bool) {
	expectedTargets, exists := m.deltaAppTargetKeys[application]
	if !exists || len(expectedTargets) == 0 {
		return nil, false
	}
	snapshot := m.deltaStatusStore.ApplicationSnapshot(application)
	if snapshot == nil || snapshot.SpecKey != specKey || !sameStringMap(snapshot.Targets, expectedTargets) {
		return nil, false
	}

	results := make(map[imageRef]applicationDeltaApplyResult, len(snapshot.Targets))
	for targetID, targetKey := range snapshot.Targets {
		result, hasResult := applicationDeltaResultFromSnapshot(snapshot, targetID, targetKey)
		if !hasResult {
			result, hasResult = m.currentApplicationDeltaResult(application, targetID, targetKey)
		}
		if !hasResult {
			return nil, false
		}
		results[imageRef{image: targetID}] = result
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
	for ref, result := range m.deltaApplyResults[application] {
		if deltastatus.Fingerprint(string(ref.owner), ref.image) != targetID {
			continue
		}
		if result.targetKey != targetKey {
			continue
		}
		task, exists := m.tasks[ref]
		if exists && task.applicationTargetKey != targetKey {
			continue
		}
		return result, true
	}
	return applicationDeltaApplyResult{}, false
}

func aggregateApplicationDeltaApplyResults(results map[imageRef]applicationDeltaApplyResult) *v1beta1.DeviceDeltaApplyStatus {
	var appliedCount, fallbackCount, notUsedCount int
	var fallbackReasons []string
	for _, result := range results {
		switch result.outcome {
		case v1beta1.DeviceDeltaApplyOutcomeApplied:
			appliedCount++
		case v1beta1.DeviceDeltaApplyOutcomeFallback:
			fallbackCount++
			if result.fallbackReason != "" {
				fallbackReasons = append(fallbackReasons, result.fallbackReason)
			}
		case v1beta1.DeviceDeltaApplyOutcomeNotUsed:
			notUsedCount++
		}
	}

	if appliedCount+fallbackCount+notUsedCount == 0 {
		return nil
	}

	var outcome v1beta1.DeviceDeltaApplyOutcomeType
	switch {
	case appliedCount > 0 && (fallbackCount > 0 || notUsedCount > 0):
		outcome = v1beta1.DeviceDeltaApplyOutcomePartial
	case appliedCount > 0:
		outcome = v1beta1.DeviceDeltaApplyOutcomeApplied
	case fallbackCount > 0:
		outcome = v1beta1.DeviceDeltaApplyOutcomeFallback
	default:
		outcome = v1beta1.DeviceDeltaApplyOutcomeNotUsed
	}

	deltaStatus := &v1beta1.DeviceDeltaApplyStatus{Outcome: outcome}
	if len(fallbackReasons) > 0 {
		slices.Sort(fallbackReasons)
		fallbackReason := fallbackReasons[0]
		deltaStatus.FallbackReason = &fallbackReason
	}
	return deltaStatus
}
