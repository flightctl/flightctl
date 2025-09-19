package spec

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/ring_buffer"
	"k8s.io/apimachinery/pkg/util/wait"
)

const longPollTimeout = 4 * time.Minute

// watcher wraps a ring buffer to implement the Watcher interface
type watcher struct {
	buffer *ring_buffer.RingBuffer[*v1alpha1.Device]
}

func newWatcher() *watcher {
	return &watcher{
		buffer: ring_buffer.NewRingBuffer[*v1alpha1.Device](3),
	}
}

func (w *watcher) Pop() (*v1alpha1.Device, error) {
	return w.buffer.Pop()
}

func (w *watcher) TryPop() (*v1alpha1.Device, bool, error) {
	return w.buffer.TryPop()
}

type Publisher interface {
	Run(ctx context.Context)
	Watch() Watcher
	SetClient(client.Management)
}

type publisher struct {
	managementClient      client.Management
	deviceName            string
	watchers              []*watcher
	lastKnownVersion      string
	interval              time.Duration
	stopped               atomic.Bool
	log                   *log.PrefixLogger
	backoff               wait.Backoff
	deviceNotFoundHandler func() error
	mu                    sync.Mutex
}

func newPublisher(deviceName string,
	interval time.Duration,
	backoff wait.Backoff,
	lastKnownVersion string,
	deviceNotFoundHandler func() error,
	log *log.PrefixLogger) Publisher {
	return &publisher{
		deviceName:            deviceName,
		interval:              interval,
		backoff:               backoff,
		lastKnownVersion:      lastKnownVersion,
		deviceNotFoundHandler: deviceNotFoundHandler,
		log:                   log,
	}
}

func (n *publisher) getRenderedFromManagementAPIWithRetry(
	ctx context.Context,
	renderedVersion string,
	rendered *v1alpha1.Device,
) (bool, error) {
	params := &v1alpha1.GetRenderedDeviceParams{}
	if renderedVersion != "" {
		params.KnownRenderedVersion = &renderedVersion
	}

	resp, statusCode, err := n.managementClient.GetRenderedDevice(ctx, n.deviceName, params)
	if err != nil {
		return false, fmt.Errorf("%w: %w", errors.ErrGettingDeviceSpec, err)
	}

	switch statusCode {
	case http.StatusOK:
		if resp == nil {
			// 200 OK but response is nil
			return false, errors.ErrNilResponse
		}
		*rendered = *resp
		return true, nil

	case http.StatusNoContent, http.StatusConflict:
		// instead of treating it as an error indicate that no new content is available
		return true, errors.ErrNoContent

	default:
		// unexpected status codes
		return false, fmt.Errorf("%w: unexpected status code %d", errors.ErrGettingDeviceSpec, statusCode)
	}
}

func (n *publisher) Watch() Watcher {
	n.mu.Lock()
	defer n.mu.Unlock()
	w := newWatcher()
	n.watchers = append(n.watchers, w)
	if n.stopped.Load() {
		w.buffer.Stop()
	}
	return w
}

func (n *publisher) SetClient(client client.Management) {
	n.managementClient = client
}

func (n *publisher) pollAndPublish(ctx context.Context) {
	if n.stopped.Load() {
		n.log.Debug("Publisher is stopped, skipping poll")
		return
	}

	newDesired := &v1alpha1.Device{}

	var cancel context.CancelFunc
	startTime := time.Now()
	ctx, cancel = context.WithTimeout(ctx, longPollTimeout)
	defer cancel()
	err := wait.ExponentialBackoff(n.backoff, func() (bool, error) {
		return n.getRenderedFromManagementAPIWithRetry(ctx, n.lastKnownVersion, newDesired)
	})

	// log slow calls
	duration := time.Since(startTime)
	if duration >= longPollTimeout {
		n.log.Debugf("Dialing management API took: %v", duration)
	}
	if err != nil {
		// Check for device not found error - handle certificate wiping and restart
		if errors.Is(err, client.ErrDeviceNotFound) {
			n.log.Warn("Device not found on management server")
			if n.deviceNotFoundHandler != nil {
				if handlerErr := n.deviceNotFoundHandler(); handlerErr != nil {
					n.log.Warnf("Failed to handle device not found: %v", handlerErr)
					return
				}
				n.log.Info("Successfully handled device not found - certificate wiped and agent restarted")
			}
			return
		}

		if errors.Is(err, errors.ErrNoContent) || errors.IsTimeoutError(err) {
			n.log.Debug("No new template version from management service")
			return
		}
		n.log.Errorf("Received non-retryable error from management service: %v", err)
		return
	}

	n.mu.Lock()
	defer n.mu.Unlock()

	n.lastKnownVersion = newDesired.Version()

	// notify all watchers of the new device spec
	for _, w := range n.watchers {
		if err := w.buffer.Push(newDesired); err != nil {
			n.log.Errorf("Failed to notify watcher: %v", err)
		}
	}
}

func (n *publisher) Run(ctx context.Context) {
	defer n.stop()
	n.log.Debug("Starting publisher")
	ticker := time.NewTicker(n.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			n.log.Debug("Publisher context done")
			return
		case <-ticker.C:
			n.pollAndPublish(ctx)
		}
	}
}

func (n *publisher) stop() {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.stopped.Store(true)
	for _, w := range n.watchers {
		w.buffer.Stop()
	}
}
