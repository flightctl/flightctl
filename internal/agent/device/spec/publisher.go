package spec

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/poll"
	"github.com/flightctl/flightctl/pkg/ring_buffer"
)

const (
	longPollTimeout = 4 * time.Minute
	// defaultMinPollDelay paces successful / no-content polls so a fast response
	// does not immediately hammer /rendered. Error backoff uses a separate config.
	defaultMinPollDelay       = 5 * time.Second
	defaultErrorBackoffFactor = 2.0
	defaultMaxErrorPollDelay  = 5 * time.Minute
	defaultErrorJitterFactor  = 0.2
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

// LastStatusInvalidator is called when the server returns ConflictPaused so the next status sync pushes device details.
// Implemented by status.Manager.
type LastStatusInvalidator interface {
	InvalidateLastStatus()
}

type Publisher interface {
	Run(ctx context.Context)
	Watch() Watcher
	SetClient(client.Management)
	SetOnConflictPausedInvalidator(LastStatusInvalidator)
	ResetVersion(version string)
}

type publisher struct {
	managementClient            client.Management
	deviceName                  string
	watchers                    []*watcher
	lastKnownVersion            string
	stopped                     atomic.Bool
	log                         *log.PrefixLogger
	pollConfig                  poll.Config
	errorBackoff                poll.Config
	deviceNotFoundHandler       func() error
	onConflictPausedInvalidator LastStatusInvalidator
	mu                          sync.Mutex
}

func defaultErrorBackoff() poll.Config {
	return poll.Config{
		BaseDelay:    defaultMinPollDelay,
		Factor:       defaultErrorBackoffFactor,
		MaxDelay:     defaultMaxErrorPollDelay,
		JitterFactor: defaultErrorJitterFactor,
	}
}

func newPublisher(deviceName string,
	pollConfig poll.Config,
	errorBackoff poll.Config,
	lastKnownVersion string,
	deviceNotFoundHandler func() error,
	log *log.PrefixLogger) Publisher {
	return &publisher{
		deviceName:            deviceName,
		pollConfig:            pollConfig,
		errorBackoff:          errorBackoff,
		lastKnownVersion:      lastKnownVersion,
		deviceNotFoundHandler: deviceNotFoundHandler,
		log:                   log,
	}
}

func (n *publisher) ResetVersion(version string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.lastKnownVersion = version
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

func (n *publisher) SetOnConflictPausedInvalidator(invalidator LastStatusInvalidator) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.onConflictPausedInvalidator = invalidator
}

// pollAndPublish fetches the rendered spec once. Returns a non-nil error for
// failures that should trigger exponential backoff before the next poll.
// 204/timeout and successful 200 return nil (normal pacing).
func (n *publisher) pollAndPublish(ctx context.Context) error {
	if n.stopped.Load() {
		n.log.Debug("Publisher is stopped, skipping poll")
		return nil
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

	duration := time.Since(startTime)
	if duration >= longPollTimeout {
		n.log.Debugf("Dialing management API took: %v", duration)
	}
	if err != nil {
		if errors.Is(err, client.ErrDeviceNotFound) {
			n.log.Warn("Device not found on management server")
			if n.deviceNotFoundHandler == nil {
				return err
			}
			if handlerErr := n.deviceNotFoundHandler(); handlerErr != nil {
				n.log.Warnf("Failed to handle device not found: %v", handlerErr)
				return handlerErr
			}
			n.log.Info("Successfully handled device not found - certificate wiped and agent restarted")
			return err
		}

		if errors.Is(err, errors.ErrNoContent) || errors.IsTimeoutError(err) {
			n.log.Debug("No new template version from management service")
			return nil
		}
		n.log.Errorf("Received non-retryable error from management service: %v", err)
		return err
	}

	n.mu.Lock()
	defer n.mu.Unlock()

	if newDesired.Metadata.Annotations != nil {
		if v, ok := (*newDesired.Metadata.Annotations)[v1beta1.DeviceAnnotationConflictPaused]; ok && v == "true" && n.onConflictPausedInvalidator != nil {
			n.onConflictPausedInvalidator.InvalidateLastStatus()
		}
	}

	newVersion := newDesired.Version()
	n.log.Debugf("Received rendered device with version: '%s'", newVersion)

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

	if newVersionInt > lastVersionInt {
		n.log.Infof("New spec version received: %s -> %s", n.lastKnownVersion, newVersion)
		n.lastKnownVersion = newVersion
	} else {
		n.log.Debugf("Received rendered device with unchanged version %s (last known: %s)", newVersion, n.lastKnownVersion)
	}

	for _, w := range n.watchers {
		if err := w.buffer.Push(newDesired); err != nil {
			n.log.Errorf("Failed to notify watcher: %v", err)
		}
	}
	return nil
}

func (n *publisher) Run(ctx context.Context) {
	defer n.stop()
	n.log.Debug("Starting publisher with continuous long-polling")

	errorTries := 0
	backoffCfg := n.errorBackoff

	for {
		if ctx.Err() != nil {
			n.log.Debug("Publisher context done")
			return
		}

		startTime := time.Now()
		err := n.pollAndPublish(ctx)
		elapsed := time.Since(startTime)

		var delay time.Duration
		if err != nil {
			errorTries++
			delay = poll.CalculateBackoffDelay(&backoffCfg, errorTries)
			n.log.Debugf("Poll failed, backing off %v before next poll (attempt %d)", delay, errorTries)
		} else {
			errorTries = 0
			if elapsed < defaultMinPollDelay {
				delay = defaultMinPollDelay - elapsed
				n.log.Debugf("Poll completed quickly, waiting %v before next poll", delay)
			}
		}
		if delay <= 0 {
			continue
		}

		select {
		case <-ctx.Done():
			n.log.Debug("Publisher context done during delay")
			return
		case <-time.After(delay):
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
