package dependency

import (
	"context"
	"fmt"
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

type OCIType string

const (
	OCITypeImage    OCIType = "Image"
	OCITypeArtifact OCIType = "Artifact"
)

type OCIPullTarget struct {
	Type       OCIType
	Reference  string
	PullPolicy v1alpha1.ImagePullPolicy
	PullSecret *client.PullSecret
}

type PrefetchStatus struct {
	TotalImages   int
	PendingImages []string
}

var _ PrefetchManager = (*prefetchManager)(nil)

type PrefetchManager interface {
	// Schedule queues a pull task for the given image if not already pulled or scheduled.
	Schedule(ctx context.Context, image string, ociType OCIType, opts ...client.ClientOption) error
	// CheckReady verifies that all scheduled pulls are complete.
	CheckReady(ctx context.Context) error
	// EnsureScheduled launches pulls for any missing images not already queued.
	// Returns nil if all images are already present or completed, and a
	// retryable error if pending.
	EnsureScheduled(ctx context.Context, targets []OCIPullTarget) error
	// GetProgressMessage returns a human readable progress message.
	GetProgressMessage(ctx context.Context) string
	// IsReady reports whether the given image is already pulled and ready.
	IsReady(ctx context.Context, image string) bool
	// Cleanup cancels active pulls and drains the queue.
	Cleanup()
}

type prefetchManager struct {
	log          *log.PrefixLogger
	podmanClient *client.Podman
	timeout      time.Duration

	mu    sync.Mutex
	tasks map[string]*prefetchTask
	queue chan string
}

type prefetchTask struct {
	clientOps []client.ClientOption
	ociType   OCIType
	err       error
	done      bool
	cancelFn  context.CancelFunc
}

// NewPrefetchManager initializes a prefetch manager.
func NewPrefetchManager(
	log *log.PrefixLogger,
	podmanClient *client.Podman,
	timeout util.Duration,
) *prefetchManager {
	return &prefetchManager{
		log:          log,
		podmanClient: podmanClient,
		timeout:      time.Duration(timeout),
		tasks:        make(map[string]*prefetchTask),
		queue:        make(chan string, maxQueueSize),
	}
}

func (m *prefetchManager) Run(ctx context.Context) {
	m.log.Debug("Prefetch manager started")
	defer m.log.Debug("Prefetch manager stopped...")

	go m.worker(ctx)

	<-ctx.Done()
}

func (m *prefetchManager) Schedule(ctx context.Context, image string, ociType OCIType, opts ...client.ClientOption) error {
	m.mu.Lock()
	if _, exists := m.tasks[image]; exists {
		m.mu.Unlock()
		return nil
	}

	if m.podmanClient.ImageExists(ctx, image) {
		m.log.Debugf("%s already exists: %s", ociType, image)
		// mark done for unified management flow
		m.tasks[image] = &prefetchTask{
			ociType: ociType,
			done:    true,
			err:     nil,
		}
		m.mu.Unlock()
		return nil
	}

	// register task
	task := &prefetchTask{
		ociType: ociType,
	}
	m.tasks[image] = task
	m.mu.Unlock()

	timer := time.NewTimer(200 * time.Millisecond)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return fmt.Errorf("failed to enqueue image %s: %w", image, ctx.Err())
	case m.queue <- image:
		return nil
	case <-timer.C:
		// handle buffer full return retryable error
		return fmt.Errorf("%w: buffer full", errors.ErrImagePrefetchNotReady)
	}
}

func (m *prefetchManager) worker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			m.log.Infof("Prefetch worker exiting: %v", ctx.Err())
			return

		case image := <-m.queue:
			m.processImage(ctx, image)
		}
	}
}

func (m *prefetchManager) processImage(ctx context.Context, image string) {
	var task *prefetchTask
	m.log.Infof("Prefetching image: %s", image)

	// define the timeout for the queue item
	ctx, cancel := context.WithTimeout(ctx, m.timeout)

	m.mu.Lock()
	task = m.tasks[image]
	task.cancelFn = cancel
	m.mu.Unlock()

	defer cancel()

	for {
		if ctx.Err() != nil {
			m.setResult(image, fmt.Errorf("pulling image %s: %w", image, ctx.Err()))
			m.log.Warnf("Context error pulling image: %s", image)
			return
		}
		if err := m.pull(ctx, image, task); err != nil {
			if errors.IsRetryable(err) {
				time.Sleep(queueNextItemAfter)
				continue
			}
			m.setResult(image, fmt.Errorf("pulling image %s: %w", image, err))
			return
		}
		// success
		m.setResult(image, nil)
		return
	}
}

func (m *prefetchManager) pull(ctx context.Context, image string, task *prefetchTask) error {
	ociType := task.ociType
	switch ociType {
	case OCITypeImage:
		_, err := m.podmanClient.Pull(ctx, image, task.clientOps...)
		if err != nil {
			return err
		}
		return nil
	case OCITypeArtifact:
		_, err := m.podmanClient.PullArtifact(ctx, image, task.clientOps...)
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

func (m *prefetchManager) CheckReady(ctx context.Context) error {
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
				// this should fail fast and not be retried
				return fmt.Errorf("prefetch failed for image %s: %w", image, task.err)
			}
		}
	}

	if len(pending) > 0 {
		return fmt.Errorf("%w: %v", errors.ErrImagePrefetchNotReady, pending)
	}
	return nil
}

func (m *prefetchManager) EnsureScheduled(ctx context.Context, targets []OCIPullTarget) error {
	for _, target := range targets {
		opts := []client.ClientOption{
			client.Timeout(m.timeout),
		}
		if target.PullSecret != nil {
			opts = append(opts, client.WithPullSecret(target.PullSecret.Path))
		}
		if err := m.Schedule(ctx, target.Reference, target.Type, opts...); err != nil {
			return fmt.Errorf("prefetch schedule: %w", err)
		}
	}

	return m.CheckReady(ctx)
}

func (m *prefetchManager) IsReady(ctx context.Context, image string) bool {
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
	for _, task := range m.tasks {
		if task.cancelFn != nil {
			task.cancelFn()
		}
	}
	m.mu.Unlock()

	for {
		select {
		case <-m.queue:
			// discard
		default:
			return
		}
	}
}

func (m *prefetchManager) GetProgressMessage(ctx context.Context) string {
	status := m.status(ctx)
	completed := status.TotalImages - len(status.PendingImages)

	if len(status.PendingImages) == 0 {
		if status.TotalImages == 0 {
			return "No images to prefetch"
		}
		return fmt.Sprintf("All %d images ready", status.TotalImages)
	}

	if len(status.PendingImages) <= 3 {
		return fmt.Sprintf("%d/%d images complete, pending: %s", completed, status.TotalImages, strings.Join(status.PendingImages, ", "))
	}

	return fmt.Sprintf("%d/%d images complete, pending: %s and %d more", completed, status.TotalImages, strings.Join(status.PendingImages[:3], ", "), len(status.PendingImages)-3)
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

	return PrefetchStatus{
		TotalImages:   len(m.tasks),
		PendingImages: pendingImages,
	}
}
