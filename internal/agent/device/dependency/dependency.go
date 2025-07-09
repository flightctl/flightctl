package dependency

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/pkg/log"
)

const (
	queueNextItemAfter = 5 * time.Second
	maxQueueSize       = 10
)

// OCIType represents the type of OCI target for prefetching
type OCIType string

const (
	OCITypeImage    OCIType = "Image"
	OCITypeArtifact OCIType = "Artifact"
)

// OCIPullTarget represents an OCI target to be prefetched
type OCIPullTarget struct {
	Type       OCIType
	Reference  string
	PullPolicy v1alpha1.ImagePullPolicy
	PullSecret *client.PullSecret
}

// PrefetchStatus provides the current status of prefetch operations
type PrefetchStatus struct {
	TotalImages   int
	PendingImages []string
}

var _ PrefetchManager = (*prefetchManager)(nil)

// PrefetchManager orchestrates OCI target collection and prefetching
type PrefetchManager interface {
	// RegisterOCICollector registers a function that can collect OCI targets from a device spec
	RegisterOCICollector(collector OCICollector)
	// BeforeUpdate collects and prefetches OCI targets from all registered collectors
	BeforeUpdate(ctx context.Context, current, desired *v1alpha1.DeviceSpec) error
	// StatusMessage returns a human readable prefetch progress status message
	StatusMessage(ctx context.Context) string
	// Cleanup fires all cleanupFn cancels active pulls and drains the queue
	Cleanup()
}

// OCICollector interface for components that can collect OCI targets
type OCICollector interface {
	// CollectOCITargets returns a function that collects and processes OCI targets
	CollectOCITargets(ctx context.Context, current, desired *v1alpha1.DeviceSpec) ([]OCIPullTarget, error)
}

type prefetchManager struct {
	log          *log.PrefixLogger
	podmanClient *client.Podman
	// pullTimeout is the duration that each target will wait unless it
	// encounters an error
	pullTimeout time.Duration

	mu         sync.Mutex
	tasks      map[string]*prefetchTask
	queue      chan string
	collectors []OCICollector
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
	pullTimeout util.Duration,
) *prefetchManager {
	return &prefetchManager{
		log:          log,
		podmanClient: podmanClient,
		pullTimeout:  time.Duration(pullTimeout),
		tasks:        make(map[string]*prefetchTask),
		queue:        make(chan string, maxQueueSize),
	}
}

func (m *prefetchManager) Run(ctx context.Context) {
	m.log.Debug("Prefetch manager started")
	defer m.log.Debug("Prefetch manager stopped")

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

func (m *prefetchManager) BeforeUpdate(ctx context.Context, current, desired *v1alpha1.DeviceSpec) error {
	m.log.Debug("Collecting OCI targets from all registered prefetch functions")

	// collect all OCI targets from registered functions
	var allTargets []OCIPullTarget
	m.mu.Lock()
	collectors := slices.Clone(m.collectors)
	m.mu.Unlock()

	for i, collector := range collectors {
		targets, err := collector.CollectOCITargets(ctx, current, desired)
		if err != nil {
			return fmt.Errorf("prefetch collector %d failed: %w", i, err)
		}
		allTargets = append(allTargets, targets...)
	}

	if len(allTargets) > 0 {
		m.log.Debugf("Scheduling %d total OCI targets for prefetching", len(allTargets))
		if err := m.Schedule(ctx, allTargets); err != nil {
			return fmt.Errorf("scheduling prefetch targets: %w", err)
		}
	}

	return m.checkReady(ctx)
}

func (m *prefetchManager) checkReady(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	var pending []string
	for image, task := range m.tasks {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !task.done {
			pending = append(pending, fmt.Sprintf("%s in progress", image))
			continue
		}
		if task.err != nil {
			if errors.IsRetryable(task.err) {
				pending = append(pending, fmt.Sprintf("%s retrying: %v", image, task.err))
			} else {
				return task.err
			}
		}
	}

	if len(pending) > 0 {
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

	for {
		if ctx.Err() != nil {
			m.setResult(target, fmt.Errorf("pulling oci target %s: %w", target, ctx.Err()))
			m.log.Warnf("Context error pulling oci target: %s", target)
			return
		}
		if err := m.pull(ctx, target, task); err != nil {
			if errors.IsRetryable(err) {
				m.log.Debugf("Retryable error during prefetch: %v; retrying after %s", err, queueNextItemAfter)
				select {
				case <-time.After(queueNextItemAfter):
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
	switch ociType {
	case OCITypeImage:
		_, err := m.podmanClient.Pull(ctx, target, task.clientOps...)
		if err != nil {
			return err
		}
		return nil
	case OCITypeArtifact:
		_, err := m.podmanClient.PullArtifact(ctx, target, task.clientOps...)
		if err != nil {
			return err
		}
		return nil
	default:
		return fmt.Errorf("invalid oci type %s", ociType)
	}
}

func (m *prefetchManager) setResult(image string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	task := m.tasks[image]
	task.err = err
	task.done = true
}

func (m *prefetchManager) Schedule(ctx context.Context, targets []OCIPullTarget) error {
	for _, target := range targets {
		opts := []client.ClientOption{
			client.Timeout(m.pullTimeout),
		}
		var cleanupFns []func()
		if target.PullSecret != nil {
			opts = append(opts, client.WithPullSecret(target.PullSecret.Path))
			cleanupFns = append(cleanupFns, target.PullSecret.Cleanup)
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

	if m.podmanClient.ImageExists(ctx, target) {
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

func (m *prefetchManager) isTargetReady(image string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	task, ok := m.tasks[image]
	if !ok {
		return false
	}
	return task.err == nil && task.done
}

func (m *prefetchManager) Cleanup() {
	m.log.Debug("Prefetch cleanup: canceling active task and draining queue")

	m.mu.Lock()
	defer m.mu.Unlock()

	for _, task := range m.tasks {
		if task.cancelFn != nil {
			task.cancelFn()
		}
		// fire cleanups
		for _, cleanup := range task.cleanupFns {
			cleanup()
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

func (m *prefetchManager) StatusMessage(ctx context.Context) string {
	status := m.status(ctx)
	pending := status.PendingImages
	total := status.TotalImages
	completed := total - len(pending)

	switch {
	case total == 0:
		return "No images to prefetch"
	case len(pending) == 0:
		return fmt.Sprintf("All %d images ready", total)
	case len(pending) <= 3:
		return fmt.Sprintf("%d/%d images complete, pending: %s", completed, total, strings.Join(pending, ", "))
	default:
		return fmt.Sprintf(
			"%d/%d images complete, pending: %s and %d more",
			completed, total, strings.Join(pending[:3], ", "), len(pending)-3,
		)
	}
}

func (m *prefetchManager) status(ctx context.Context) PrefetchStatus {
	m.mu.Lock()
	defer m.mu.Unlock()

	var pendingImages []string
	for image, task := range m.tasks {
		if ctx.Err() != nil {
			return PrefetchStatus{}
		}
		if !task.done {
			pendingImages = append(pendingImages, image)
		} else if errors.IsRetryable(task.err) {
			pendingImages = append(pendingImages, image)
		}
	}

	// sort for consistent ordering
	slices.Sort(pendingImages)

	return PrefetchStatus{
		TotalImages:   len(m.tasks),
		PendingImages: pendingImages,
	}
}
