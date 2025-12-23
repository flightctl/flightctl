package spec

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/flightctl/flightctl/api/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/poll"
	"github.com/flightctl/flightctl/pkg/ring_buffer"
)

const (
	longPollTimeout = 4 * time.Minute
	// minPollDelay is the minimum delay between polls to prevent hot-looping
	minPollDelay = 5 * time.Second
)

// watcher wraps a ring buffer to implement the Watcher interface
type watcher struct {
	buffer *ring_buffer.RingBuffer[*v1beta1.Device]
}

func newWatcher() *watcher {
	return &watcher{
		buffer: ring_buffer.NewRingBuffer[*v1beta1.Device](3),
	}
}

func (w *watcher) Pop() (*v1beta1.Device, error) {
	return w.buffer.Pop()
}

func (w *watcher) TryPop() (*v1beta1.Device, bool, error) {
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
	stopped               atomic.Bool
	log                   *log.PrefixLogger
	pollConfig            poll.Config
	deviceNotFoundHandler func() error
	minDelay              time.Duration
	mu                    sync.Mutex
}

func newPublisher(deviceName string,
	pollConfig poll.Config,
	lastKnownVersion string,
	deviceNotFoundHandler func() error,
	log *log.PrefixLogger) Publisher {
	return &publisher{
		deviceName:            deviceName,
		pollConfig:            pollConfig,
		lastKnownVersion:      lastKnownVersion,
		deviceNotFoundHandler: deviceNotFoundHandler,
		log:                   log,
	}
}

func (n *publisher) getRenderedFromManagementAPIWithRetry(
	ctx context.Context,
	renderedVersion string,
	rendered *v1beta1.Device,
) (bool, error) {
	params := &v1beta1.GetRenderedDeviceParams{}
	if renderedVersion != "" {
		params.KnownRenderedVersion = &renderedVersion
	}

	resp, statusCode, err := n.managementClient.GetRenderedDevice(ctx, n.deviceName, params)
	if err != nil {
		n.log.Debugf("Failed to get rendered device spec: %v", err)
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
		// no new content available, spec unchanged
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

	n.log.Debugf("Polling management service for new rendered device spec: last known version: %s", n.lastKnownVersion)

	newDesired := &v1beta1.Device{}

	var cancel context.CancelFunc
	startTime := time.Now()
	ctx, cancel = context.WithTimeout(ctx, longPollTimeout)
	defer cancel()
	err := poll.BackoffWithContext(ctx, n.pollConfig, func(ctx context.Context) (bool, error) {
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

	newVersion := newDesired.Version()
	n.log.Debugf("Received rendered device with version: '%s'", newVersion)

	// Parse versions with defaults
	newVersionInt := int64(0)
	if newVersion != "" {
		if parsed, err := strconv.ParseInt(newVersion, 10, 64); err == nil {
			newVersionInt = parsed
		}
	}

	lastVersionInt := int64(0)
	if n.lastKnownVersion != "" {
		if parsed, err := strconv.ParseInt(n.lastKnownVersion, 10, 64); err == nil {
			lastVersionInt = parsed
		}
	}

	// Update if new version is greater, or if either version is empty/invalid
	if newVersionInt > lastVersionInt {
		n.log.Infof("New spec version received: %s -> %s", n.lastKnownVersion, newVersion)
		n.lastKnownVersion = newVersion
	} else {
		n.log.Warnf("Received spec version %s is not greater than last known version %s, skipping...", newVersion, n.lastKnownVersion)
	}

	// notify all watchers of the new device spec
	for _, w := range n.watchers {
		if err := w.buffer.Push(newDesired); err != nil {
			n.log.Errorf("Failed to notify watcher: %v", err)
		}
	}
}

func (n *publisher) Run(ctx context.Context) {
	defer n.stop()
	n.log.Debug("Starting publisher with continuous long-polling")

	minDelay := n.minDelay
	if minDelay == 0 {
		minDelay = minPollDelay
	}

	for {
		if ctx.Err() != nil {
			n.log.Debug("Publisher context done")
			return
		}

		startTime := time.Now()
		n.pollAndPublish(ctx)

		elapsed := time.Since(startTime)
		if elapsed < minDelay {
			delay := minDelay - elapsed
			n.log.Debugf("Poll completed quickly, waiting %v before next poll", delay)
			select {
			case <-ctx.Done():
				n.log.Debug("Publisher context done during delay")
				return
			case <-time.After(delay):
			}
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
