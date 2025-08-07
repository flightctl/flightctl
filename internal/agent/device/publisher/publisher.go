package publisher

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

type Subscription = *ring_buffer.RingBuffer[*v1alpha1.Device]

func NewSubscription() Subscription {
	return ring_buffer.NewRingBuffer[*v1alpha1.Device](3)
}

type Publisher interface {
	Run(ctx context.Context, wg *sync.WaitGroup)
	Subscribe() Subscription
	SetClient(client.Management)
}

type publisher struct {
	managementClient client.Management
	deviceName       string
	subscribers      []Subscription
	lastKnownVersion string
	interval         time.Duration
	stopped          atomic.Bool
	log              *log.PrefixLogger
	backoff          wait.Backoff
	mu               sync.Mutex
}

func New(deviceName string,
	interval time.Duration,
	backoff wait.Backoff,
	log *log.PrefixLogger) Publisher {
	return &publisher{
		deviceName: deviceName,
		interval:   interval,
		backoff:    backoff,
		log:        log,
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

func (n *publisher) Subscribe() Subscription {
	n.mu.Lock()
	defer n.mu.Unlock()
	sub := NewSubscription()
	n.subscribers = append(n.subscribers, sub)
	if n.stopped.Load() {
		sub.Stop()
	}
	return sub
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

	startTime := time.Now()
	err := wait.ExponentialBackoff(n.backoff, func() (bool, error) {
		return n.getRenderedFromManagementAPIWithRetry(ctx, n.lastKnownVersion, newDesired)
	})

	// log slow calls
	duration := time.Since(startTime)
	if duration > time.Minute {
		n.log.Debugf("Dialing management API took: %v", duration)
	}
	if err != nil {
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

	// notify all subscribers of the new device spec
	for _, sub := range n.subscribers {
		if err := sub.Push(newDesired); err != nil {
			n.log.Errorf("Failed to notify subscriber: %v", err)
		}
	}
}

func (n *publisher) Run(ctx context.Context, wg *sync.WaitGroup) {
	defer n.stop()
	defer wg.Done()
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
	for _, sub := range n.subscribers {
		sub.Stop()
	}
}
