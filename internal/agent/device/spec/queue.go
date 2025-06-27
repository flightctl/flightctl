package spec

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/queues"
)

// requeueState represents the state of a queued template version.
type requeueState struct {
	// version is the state which is tracked for this template version.
	version int64
	// nextAvailable is the time when the template version is available to be retrieved from the queue.
	nextAvailable time.Time
	// tries is the number of times the template version has been requeued.
	tries int
}

// queueItem represents an item in the priority queue
type queueItem struct {
	Device  *v1alpha1.Device
	Version int64
}

type queueManager struct {
	queue          *queues.IndexedPriorityQueue[*queueItem, int64]
	failedVersions map[int64]struct{}
	requeueLookup  map[int64]*requeueState
	// maxRetries is the number of times a template version can be requeued before being removed.
	// A value of 0 means infinite retries.
	maxRetries int
	// delayThreshold is the number of times a template version can be requeued before enforcing a delay.
	// A value of 0 means this is disabled.
	delayThreshold int
	// delayDuration is the duration to wait before the item is
	// available to be retrieved form the queue.
	delayDuration time.Duration
	maxSize       int

	log *log.PrefixLogger
}

// newPriorityQueue returns a new priority queue.
func newPriorityQueue(
	maxSize int,
	maxRetries int,
	delayThreshold int,
	delayDuration time.Duration,
	log *log.PrefixLogger,
) PriorityQueue {
	extractor := func(item *queueItem) int64 {
		return item.Version
	}
	comparator := queues.Min[int64]
	underlying := queues.NewIndexedPriorityQueue[*queueItem, int64](
		comparator,
		extractor,
		queues.WithMaxSize[*queueItem, int64](maxSize),
	)

	return &queueManager{
		queue:          underlying,
		failedVersions: make(map[int64]struct{}),
		requeueLookup:  make(map[int64]*requeueState),
		maxRetries:     maxRetries,
		delayThreshold: delayThreshold,
		delayDuration:  delayDuration,
		maxSize:        maxSize,
		log:            log,
	}
}

func (m *queueManager) Add(ctx context.Context, device *v1alpha1.Device) {
	m.addInternal(ctx, device, nil)
}

func (m *queueManager) AddWithDelay(ctx context.Context, device *v1alpha1.Device, nextAvailable time.Time) {
	m.addInternal(ctx, device, &nextAvailable)
}

func (m *queueManager) addInternal(ctx context.Context, device *v1alpha1.Device, nextAvailable *time.Time) {
	if ctx.Err() != nil {
		return
	}

	if device.Spec == nil {
		m.log.Errorf("Skipping device with nil spec: %s", device.Version())
		return
	}

	version, err := stringToInt64(device.Version())
	if err != nil {
		m.log.Errorf("Failed to parse device version: %v", err)
		return
	}

	if _, failed := m.failedVersions[version]; failed {
		m.log.Debugf("Skipping adding failed template version: %d", version)
		return
	}

	m.pruneRequeueStatus()

	item := &queueItem{
		Device:  device,
		Version: version,
	}

	state := m.getOrCreateRequeueState(ctx, item)

	if nextAvailable != nil {
		state.nextAvailable = *nextAvailable
		m.log.Debugf("Added device version %d with delay until: %s", version, nextAvailable.Format(time.RFC3339))
	} else {
		if m.shouldEnforceDelay(state) {
			m.log.Debugf("Enforcing delay for version: %d", version)
		}
	}

	if m.hasExceededMaxRetries(state, version) {
		m.setFailed(version)
		return
	}

	m.queue.Add(item)
}

func (m *queueManager) Remove(version int64) {
	m.queue.Remove(version)
}

func (m *queueManager) Next(ctx context.Context) (*v1alpha1.Device, bool) {
	if ctx.Err() != nil {
		return nil, false
	}
	item, ok := m.queue.Pop()
	if !ok {
		return nil, false
	}
	version := item.Version

	m.log.Debugf("Evaluating template version: %d", version)
	now := time.Now()
	requeue := m.getOrCreateRequeueState(ctx, item)
	if now.Before(requeue.nextAvailable) {
		m.queue.Add(item)
		m.log.Debugf("Template version %d requeue is currently in backoff. Available after: %s", version, requeue.nextAvailable.Format(time.RFC3339))
		return nil, false
	}

	if item.Device != nil {
		m.log.Debugf("Retrieved template version from the queue: %d", version)
		requeue.nextAvailable = time.Time{}
		requeue.tries++
		return item.Device, true
	}

	m.log.Errorf("Dropping template version %d from queue: missing or invalid spec", version)
	return nil, false
}

func (m *queueManager) SetFailed(version int64) {
	m.setFailed(version)
}

func (m *queueManager) setFailed(version int64) {
	m.failedVersions[version] = struct{}{}
	delete(m.requeueLookup, version)
	m.queue.Remove(version)
}

func (m *queueManager) IsFailed(version int64) bool {
	_, ok := m.failedVersions[version]
	return ok
}

func (m *queueManager) getOrCreateRequeueState(ctx context.Context, item *queueItem) *requeueState {
	version := item.Version
	state, exists := m.requeueLookup[version]
	if !exists {
		m.log.Debugf("Initializing requeueState for version %d", version)
		state = &requeueState{
			version: version,
		}
		m.requeueLookup[version] = state
	}

	return state
}

func (m *queueManager) shouldEnforceDelay(state *requeueState) bool {
	if m.delayThreshold <= 0 || !m.queue.IsEmpty() || state.tries == 0 {
		return false
	}
	if state.tries >= m.delayThreshold && state.nextAvailable.IsZero() {
		state.nextAvailable = time.Now().Add(m.delayDuration)
		m.log.Debugf("Delay enforced until: %s", state.nextAvailable)
		return true
	}
	return false
}

func (m *queueManager) hasExceededMaxRetries(state *requeueState, version int64) bool {
	if m.maxRetries > 0 && state.tries >= m.maxRetries {
		m.log.Debugf("Max retries exceeded for version: %d", version)
		return true
	}
	return false
}

func (m *queueManager) pruneRequeueStatus() {
	maxRequeueSize := 5 * m.maxSize
	if m.maxSize == 0 {
		return
	}

	if len(m.requeueLookup) > maxRequeueSize {
		var minVersion int64
		var initialized bool

		for version, state := range m.requeueLookup {
			if !initialized || version < minVersion && state.tries > 0 {
				minVersion = version
				initialized = true
			}
		}
		if initialized {
			delete(m.requeueLookup, minVersion)
			m.log.Debugf("Evicted lowest template version: %d", minVersion)
		}
	}
}

func stringToInt64(s string) (int64, error) {
	if s == "" {
		return 0, nil
	}
	i, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("convert string to int64: %w", err)
	}
	if i < 0 {
		return 0, fmt.Errorf("version number cannot be negative: %d", i)
	}
	return i, nil
}
