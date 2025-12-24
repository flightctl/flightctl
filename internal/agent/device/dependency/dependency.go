package dependency

import (
	"context"
	"fmt"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/flightctl/flightctl/api/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/agent/device/resource"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/poll"
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
	OCITypeImage    OCIType = "Image"
	OCITypeArtifact OCIType = "Artifact"
	OCITypeAuto     OCIType = "Auto"
)

// detectOCIType analyzes an OCI manifest to determine if it's an image or artifact
func detectOCIType(manifest *client.OCIManifest) (OCIType, error) {
	if manifest == nil {
		return "", fmt.Errorf("manifest is nil")
	}

	if len(manifest.Manifests) > 0 {
		return OCITypeImage, nil
	}

	if manifest.ArtifactType != "" {
		return OCITypeArtifact, nil
	}

	if manifest.Config != nil {
		switch manifest.Config.MediaType {
		case ociImageConfigMediaType, dockerImageConfigMediaType:
			return OCITypeImage, nil
		case "":
			return "", fmt.Errorf("media type not set")
		default:
			// artifact types could be anything, so default to artifact
			// if there is any value here that isn't the default image type
			return OCITypeArtifact, nil
		}
	}

	return OCITypeImage, nil
}

// OCIPullTarget represents an OCI target to be prefetched
type OCIPullTarget struct {
	Type       OCIType
	Reference  string
	Digest     string
	PullPolicy v1beta1.ImagePullPolicy
	PullSecret *client.PullSecret
}

// OCICollection represents the result of collecting OCI targets
type OCICollection struct {
	Targets []OCIPullTarget
	Requeue bool // true if collection is incomplete and should be retried
}

// PrefetchStatus provides the current status of prefetch operations
type PrefetchStatus struct {
	TotalImages    int
	PendingImages  []string
	RetryingImages []string
}

var _ PrefetchManager = (*prefetchManager)(nil)

// PrefetchManager orchestrates OCI target collection and prefetching
type PrefetchManager interface {
	// RegisterOCICollector registers a function that can collect OCI targets from a device spec
	RegisterOCICollector(collector OCICollector)
	// BeforeUpdate collects and prefetches OCI targets from all registered collectors
	BeforeUpdate(ctx context.Context, current, desired *v1beta1.DeviceSpec) error
	// StatusMessage returns a human readable prefetch progress status message
	StatusMessage(ctx context.Context) string
	// Cleanup fires all cleanupFn cancels active pulls and drains the queue
	Cleanup()
}

// OCICollector interface for components that can collect OCI targets
type OCICollector interface {
	// CollectOCITargets collects OCI targets and indicates if requeue is needed
	CollectOCITargets(ctx context.Context, current, desired *v1beta1.DeviceSpec) (*OCICollection, error)
}

type prefetchManager struct {
	log             *log.PrefixLogger
	podmanClient    *client.Podman
	skopeoClient    *client.Skopeo
	readWriter      fileio.ReadWriter
	resourceManager resource.Manager
	// pullTimeout is the duration that each target will wait unless it
	// encounters an error
	pullTimeout time.Duration
	pollConfig  *poll.Config

	mu         sync.Mutex
	tasks      map[string]*prefetchTask
	queue      chan string
	collectors []OCICollector
	tmpDir     string // cached podman tmpdir
}

type prefetchTask struct {
	clientOps  []client.ClientOption
	ociType    OCIType
	err        error
	done       bool
	cancelFn   context.CancelFunc
	cleanupFns []func()
}

// NewPrefetchManager creates a new prefetch manager instance
func NewPrefetchManager(
	log *log.PrefixLogger,
	podmanClient *client.Podman,
	skopeoClient *client.Skopeo,
	readWriter fileio.ReadWriter,
	pullTimeout util.Duration,
	resourceManager resource.Manager,
	pollConfig poll.Config,
) *prefetchManager {
	return &prefetchManager{
		log:             log,
		podmanClient:    podmanClient,
		skopeoClient:    skopeoClient,
		readWriter:      readWriter,
		pullTimeout:     time.Duration(pullTimeout),
		pollConfig:      &pollConfig,
		resourceManager: resourceManager,
		tasks:           make(map[string]*prefetchTask),
		queue:           make(chan string, maxQueueSize),
	}
}

func (m *prefetchManager) Run(ctx context.Context) {
	m.log.Debug("Prefetch manager started")
	defer m.log.Debug("Prefetch manager stopped")

	if tmpDir, err := m.podmanClient.GetImageCopyTmpDir(ctx); err != nil {
		m.log.Warnf("failed to cache tmpdir: %v", err)
	} else {
		m.tmpDir = tmpDir
	}

	go m.worker(ctx)

	<-ctx.Done()
}

func (m *prefetchManager) worker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			m.log.Debugf("Prefetch worker exiting: %v", ctx.Err())
			return

		case image := <-m.queue:
			m.processTarget(ctx, image)
		}
	}
}

func (m *prefetchManager) RegisterOCICollector(collector OCICollector) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.collectors = append(m.collectors, collector)
}

// isTargetsChanged checks if targets changed using pre-built reference set
// caller must hold m.mu lock
func (m *prefetchManager) isTargetsChanged(seenTargets map[string]struct{}) bool {
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

func (m *prefetchManager) BeforeUpdate(ctx context.Context, current, desired *v1beta1.DeviceSpec) error {
	m.log.Debug("Collecting OCI targets from all dependency sources")

	var allTargets []OCIPullTarget
	var requeueNeeded bool
	m.mu.Lock()
	collectors := slices.Clone(m.collectors)
	m.mu.Unlock()

	for i, collector := range collectors {
		result, err := collector.CollectOCITargets(ctx, current, desired)
		if err != nil {
			return fmt.Errorf("prefetch collector %d failed: %w", i, err)
		}
		allTargets = append(allTargets, result.Targets...)
		if result.Requeue {
			requeueNeeded = true
		}
	}

	seenTargets := make(map[string]struct{})
	newTargets := make([]OCIPullTarget, 0)
	for _, target := range allTargets {
		if _, seen := seenTargets[target.Reference]; !seen {
			newTargets = append(newTargets, target)
			seenTargets[target.Reference] = struct{}{}
		}
	}

	m.log.Debugf("Collected %d unique OCI targets", len(seenTargets))

	// clean up stale prefetch tasks if targets have changed
	m.mu.Lock()
	if m.isTargetsChanged(seenTargets) {
		m.log.Debug("OCI targets changed, cleaning up stale prefetch tasks")
		m.cleanupStaleTasks(seenTargets)
	}
	m.mu.Unlock()

	if len(newTargets) > 0 {
		if m.resourceManager.IsCriticalAlert(resource.DiskMonitorType) {
			return fmt.Errorf("%w: insufficient disk storage space, please clear storage", errors.ErrCriticalResourceAlert)
		}
		m.log.Debugf("Scheduling %d new targets for prefetch", len(newTargets))
		if err := m.Schedule(ctx, newTargets); err != nil {
			return fmt.Errorf("scheduling prefetch targets: %w", err)
		}
	}

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
				return task.err
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

func (m *prefetchManager) processTarget(ctx context.Context, target string) {
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
			m.setResult(target, fmt.Errorf("pulling oci target %s: %w", target, ctx.Err()))
			m.log.Warnf("Context error pulling oci target: %s", target)
			return
		}
		if err := m.pull(ctx, target, task); err != nil {
			if errors.IsRetryable(err) {
				retries++
				m.log.Warnf("Retrying prefetch for %s (attempt %d): %v", target, retries+1, err)
				m.setError(target, err)

				// cleanup file system from partial layer pulls
				if err := m.cleanupPartialLayers(ctx); err != nil {
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
			m.setResult(target, fmt.Errorf("pulling oci target %s: %w", target, err))
			return
		}
		// success
		m.setResult(target, nil)
		return
	}
}

func (m *prefetchManager) pull(ctx context.Context, target string, task *prefetchTask) error {
	ociType := task.ociType

	var err error
	switch ociType {
	case OCITypeImage:
		_, err = m.podmanClient.Pull(ctx, target, task.clientOps...)
	case OCITypeArtifact:
		_, err = m.podmanClient.PullArtifact(ctx, target, task.clientOps...)
	case OCITypeAuto:
		m.log.Debugf("Auto-detecting OCI type for %s", target)
		manifest, err := m.skopeoClient.InspectManifest(ctx, target, task.clientOps...)
		if err != nil {
			return fmt.Errorf("inspecting manifest for auto-detection: %w", err)
		}

		detectedType, err := detectOCIType(manifest)
		if err != nil {
			return fmt.Errorf("detecting OCI type: %w", err)
		}

		m.log.Infof("Detected OCI type for %s: %s", target, detectedType)

		switch detectedType {
		case OCITypeImage:
			_, err = m.podmanClient.Pull(ctx, target, task.clientOps...)
		case OCITypeArtifact:
			_, err = m.podmanClient.PullArtifact(ctx, target, task.clientOps...)
		default:
			return fmt.Errorf("unexpected detected OCI type: %s", detectedType)
		}
		return err
	default:
		return fmt.Errorf("invalid oci type %s", ociType)
	}
	return err
}

func (m *prefetchManager) setResult(image string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	task, ok := m.tasks[image]
	if !ok {
		m.log.Debugf("Task for %s no longer exists, skipping result update", image)
		return
	}
	task.err = err
	task.done = true
}

func (m *prefetchManager) setError(target string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	task, ok := m.tasks[target]
	if !ok {
		m.log.Debugf("Task for %s no longer exists, skipping error update", target)
		return
	}
	task.err = err
}

func (m *prefetchManager) Schedule(ctx context.Context, targets []OCIPullTarget) error {
	for _, target := range targets {
		opts := []client.ClientOption{
			client.Timeout(m.pullTimeout),
		}
		var cleanupFns []func()
		if target.PullSecret != nil {
			opts = append(opts, client.WithPullSecret(target.PullSecret.Path))
			if target.PullSecret.Cleanup != nil {
				cleanupFns = append(cleanupFns, target.PullSecret.Cleanup)
			}
		}
		if err := m.schedule(ctx, target.Reference, target.Type, cleanupFns, opts...); err != nil {
			return fmt.Errorf("prefetch schedule: %w", err)
		}
	}

	return nil
}

func (m *prefetchManager) schedule(ctx context.Context, target string, ociType OCIType, cleanupFns []func(), opts ...client.ClientOption) error {
	m.mu.Lock()
	if _, exists := m.tasks[target]; exists {
		m.mu.Unlock()
		return nil
	}

	var targetExists bool
	switch ociType {
	case OCITypeImage:
		targetExists = m.podmanClient.ImageExists(ctx, target)
	case OCITypeArtifact:
		targetExists = m.podmanClient.ArtifactExists(ctx, target)
	case OCITypeAuto:
		// attempt to resolve whether the dependency already exists as an artifact or an image.
		// avoids making a network call to determine the actual type
		targetExists = m.podmanClient.ImageExists(ctx, target) || m.podmanClient.ArtifactExists(ctx, target)
	default:
		m.mu.Unlock()
		return fmt.Errorf("invalid oci type %s", ociType)
	}

	if targetExists {
		m.log.Debugf("Scheduled prefetch target already exists: %s", target)
		// mark done for unified management flow
		m.tasks[target] = &prefetchTask{
			ociType:    ociType,
			done:       true,
			err:        nil,
			cleanupFns: cleanupFns,
		}
		m.mu.Unlock()
		return nil
	}

	// register task
	task := &prefetchTask{
		ociType:    ociType,
		clientOps:  opts,
		cleanupFns: cleanupFns,
	}
	m.tasks[target] = task
	m.mu.Unlock()

	timer := time.NewTimer(250 * time.Millisecond)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		m.removeTask(target)
		return fmt.Errorf("failed to enqueue target %s: %w", target, ctx.Err())
	case m.queue <- target:
		return nil
	case <-timer.C:
		m.log.Warnf("Prefetch schedule failed for: %s: buffer full", target)
		m.removeTask(target)
		return fmt.Errorf("%w: buffer full", errors.ErrPrefetchNotReady)
	}
}

func (m *prefetchManager) removeTask(image string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.tasks, image)
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
func (m *prefetchManager) cleanupStaleTasks(seenTargets map[string]struct{}) {
	var removed int
	for ref, task := range m.tasks {
		if _, exists := seenTargets[ref]; !exists {
			if task.cancelFn != nil {
				task.cancelFn()
			}
			for _, cleanup := range task.cleanupFns {
				if cleanup != nil {
					cleanup()
				}
			}
			delete(m.tasks, ref)
			removed++
		}
	}

	if removed > 0 {
		m.log.Debugf("Cleaned up %d stale prefetch tasks", removed)
	}
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
		// fire cleanups
		for _, cleanup := range task.cleanupFns {
			if cleanup != nil {
				cleanup()
			}
		}
	}

	m.collectors = nil
	m.tasks = make(map[string]*prefetchTask)

	for {
		select {
		case <-m.queue:
			// discard
		default:
			return
		}
	}
}

func (m *prefetchManager) cleanupPartialLayers(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	m.mu.Lock()
	tmpDir := m.tmpDir
	m.mu.Unlock()

	if tmpDir == "" {
		var err error
		tmpDir, err = m.podmanClient.GetImageCopyTmpDir(ctx)
		if err != nil {
			m.log.Warnf("Failed to get image copy tmpdir: %v", err)
			return nil
		}
		m.tmpDir = tmpDir
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
			retryingImages = append(retryingImages, fmt.Sprintf("%s: %s", image, log.Truncate(errors.Reason(task.err), 100)))
		} else if !task.done {
			pendingImages = append(pendingImages, image)
		}
	}

	// sort for consistent ordering
	slices.Sort(pendingImages)
	slices.Sort(retryingImages)

	return PrefetchStatus{
		TotalImages:    len(m.tasks),
		PendingImages:  pendingImages,
		RetryingImages: retryingImages,
	}
}
