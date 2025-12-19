package spec

import (
	"container/heap"
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/flightctl/flightctl/api/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/policy"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/poll"
)

// requeueState represents the state of a queued template version.
type requeueState struct {
	// version is the state which is tracked for this template version.
	version int64
	// nextAvailable is the time when the template version is available to be retrieved from the queue.
	nextAvailable time.Time
	// tries is the number of times the template version has been requeued.
	tries int
	// downloadPolicySatisfied indicates if the download policy is satisfied.
	// this state only needs to be met once.
	downloadPolicySatisfied bool
	// updatePolicySatisfied indicates if the update policy is satisfied. this
	// state only needs to be met once.
	updatePolicySatisfied bool
}

type queueManager struct {
	queue          *queue
	policyManager  policy.Manager
	failedVersions map[int64]struct{}
	requeueLookup  map[int64]*requeueState
	specCache      *cache
	// maxRetries is the number of times a template version can be requeued before being removed.
	// A value of 0 means infinite retries.
	maxRetries int
	// pollConfig contains the backoff configuration for retries
	pollConfig poll.Config

	log *log.PrefixLogger
}

// newQueueManager returns a new queue manager.
func newQueueManager(
	maxSize int,
	maxRetries int,
	pollConfig poll.Config,
	policyManager policy.Manager,
	specCache *cache,
	log *log.PrefixLogger,
) PriorityQueue {
	return &queueManager{
		queue:          newQueue(log, maxSize),
		policyManager:  policyManager,
		specCache:      specCache,
		failedVersions: make(map[int64]struct{}),
		requeueLookup:  make(map[int64]*requeueState),
		maxRetries:     maxRetries,
		pollConfig:     pollConfig,
		log:            log,
	}
}

func (m *queueManager) Add(ctx context.Context, device *v1beta1.Device) {
	if ctx.Err() != nil {
		return
	}

	if device.Spec == nil {
		m.log.Errorf("Skipping device with nil spec: %s", device.Version())
		return
	}

	item, err := newItem(device)
	if err != nil {
		m.log.Errorf("Failed to create queue item: %v", err)
		return
	}
	proposedVersion := item.Version
	if _, failed := m.failedVersions[proposedVersion]; failed {
		m.log.Debugf("Skipping adding failed template version: %d", proposedVersion)
		return
	}
	currentRenderedVersion, err := stringToInt64(m.specCache.current.renderedVersion)
	if err != nil {
		m.log.Errorf("Failed to parse device version: %s error: %v", device.Version(), err)
		return
	}
	if proposedVersion < currentRenderedVersion {
		m.log.Errorf("Skipping adding template version less than current rendered version: %d < %d", proposedVersion, currentRenderedVersion)
		return
	}

	m.pruneRequeueStatus()

	state := m.getOrCreateRequeueState(ctx, proposedVersion)
	if m.shouldEnforceDelay(state) {
		m.log.Debugf("Enforcing resync delay for version: %d", proposedVersion)
	}

	if m.hasExceededMaxRetries(state, proposedVersion) {
		m.setFailed(proposedVersion)
		return
	}

	m.queue.Add(item)
}

func (m *queueManager) Remove(version int64) {
	m.queue.Remove(version)
}

func (m *queueManager) Next(ctx context.Context) (*v1beta1.Device, bool) {
	if ctx.Err() != nil {
		return nil, false
	}
	item, ok := m.queue.Pop()
	if !ok {
		return nil, false
	}
	version := item.Version

	m.log.Tracef("Evaluating template version: %d", version)
	now := time.Now()
	requeue := m.getOrCreateRequeueState(ctx, version)
	if now.Before(requeue.nextAvailable) {
		m.queue.Add(item)
		m.log.Tracef("Template version %d requeue is currently in backoff. Available after: %s", version, requeue.nextAvailable.Format(time.RFC3339Nano))
		return nil, false
	}

	// currently it's useful to allow specs to be consumed if the download policy is satisfied
	// even if the updatePolicy isn't
	if !requeue.downloadPolicySatisfied && !requeue.updatePolicySatisfied {
		m.log.Debugf("Template version %d policies are not satisfied skipping...", version)
		m.queue.Add(item)
		return nil, false
	}

	if item.Spec != nil {
		m.log.Debugf("Retrieved template version from the queue: %d", version)
		requeue.nextAvailable = time.Time{}
		requeue.tries++
		return item.Spec, true
	}

	m.log.Errorf("Dropping template version %d from queue: missing or invalid spec", version)
	return nil, false
}

func (m *queueManager) CheckPolicy(ctx context.Context, policyType policy.Type, version string) error {
	v, err := stringToInt64(version)
	if err != nil {
		return err
	}
	requeue, exists := m.requeueLookup[v]
	if !exists {
		// this would be very unexpected so we would need to requeue the version
		return fmt.Errorf("%w: policy check failed: not found: version: %d", errors.ErrRetryable, v)
	}
	m.log.Debugf("Requeue state: %+v", requeue)

	switch policyType {
	case policy.Download:
		if requeue.downloadPolicySatisfied {
			return nil
		}
		return errors.ErrDownloadPolicyNotReady
	case policy.Update:
		if requeue.updatePolicySatisfied {
			return nil
		}
		return errors.ErrUpdatePolicyNotReady
	default:
		return fmt.Errorf("%w: %s", errors.ErrInvalidPolicyType, policyType)
	}
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

func (m *queueManager) getOrCreateRequeueState(ctx context.Context, version int64) *requeueState {
	state, exists := m.requeueLookup[version]
	if !exists {
		m.log.Debugf("Initializing requeueState for version %d", version)
		state = &requeueState{
			version: version,
		}
		m.requeueLookup[version] = state
	}

	if m.updatePolicy(ctx, state) {
		m.log.Debugf("Policy updated for version %d", version)
	}

	return state
}

func (m *queueManager) shouldEnforceDelay(state *requeueState) bool {
	if state.tries == 0 {
		return false
	}

	if state.nextAvailable.IsZero() {
		// incremental delay based on tries
		delay := m.calculateBackoffDelay(state.tries)
		state.nextAvailable = time.Now().Add(delay)
		m.log.Debugf("Incremental delay enforced for version: %d: until: %s", state.version, state.nextAvailable.Format(time.RFC3339))
		return true
	}
	return false
}

func (m *queueManager) calculateBackoffDelay(tries int) time.Duration {
	return poll.CalculateBackoffDelay(&m.pollConfig, tries)
}

func (m *queueManager) hasExceededMaxRetries(state *requeueState, version int64) bool {
	if m.maxRetries > 0 && state.tries >= m.maxRetries {
		m.log.Debugf("Max retries exceeded for version: %d", version)
		return true
	}
	return false
}

// updatePolicy calls into the policyManager to check if the policy have been
// satisfied since the last call an updates accordingly returns true if the
// policy has changed.
func (m *queueManager) updatePolicy(ctx context.Context, requeue *requeueState) bool {
	changed := false
	if !requeue.downloadPolicySatisfied {
		if m.policyManager.IsReady(ctx, policy.Download) {
			changed = true
			requeue.downloadPolicySatisfied = true
		}
	}

	if !requeue.updatePolicySatisfied {
		if m.policyManager.IsReady(ctx, policy.Update) {
			changed = true
			requeue.updatePolicySatisfied = true
		}
	}
	return changed
}

func (m *queueManager) pruneRequeueStatus() {
	maxRequeueSize := 5 * m.queue.maxSize
	if m.queue.maxSize == 0 {
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

type queue struct {
	heap    ItemHeap
	items   map[int64]*Item
	maxSize int
	log     *log.PrefixLogger
}

// newQueue creates a new queue that orders items by version.
// If maxSize is exceeded, the lowest version is removed.
func newQueue(log *log.PrefixLogger, maxSize int) *queue {
	return &queue{
		heap:    make(ItemHeap, 0),
		items:   make(map[int64]*Item),
		maxSize: maxSize,
		log:     log,
	}
}

func (q *queue) Add(item *Item) {
	version := item.Version
	if _, exists := q.items[version]; exists {
		q.log.Tracef("Skipping item with version %d already in queue", version)
		return
	}

	// enforce max size
	if q.maxSize > 0 && q.heap.Len() > 0 && len(q.items) >= q.maxSize {
		// evict the lowest version from the queue
		removed := heap.Pop(&q.heap).(*Item)
		delete(q.items, removed.Version)
		q.log.Debugf("Queue exceeded max size, evicted version: %d", removed.Version)
	}

	q.items[version] = item
	heap.Push(&q.heap, item)
	q.log.Tracef("Added item version %d, heap size now %d", version, q.heap.Len())
}

func (q *queue) Pop() (*Item, bool) {
	if q.heap.Len() == 0 {
		return nil, false
	}
	item := heap.Pop(&q.heap).(*Item)
	delete(q.items, item.Version)
	q.log.Tracef("Popped item version %d, heap size now %d", item.Version, q.heap.Len())
	return item, true
}

func (q *queue) Size() int {
	return len(q.items)
}

// IsEmpty returns true if the queue is empty.
func (q *queue) IsEmpty() bool {
	return q.Size() == 0
}

func (q *queue) Clear() {
	q.items = make(map[int64]*Item)
	q.heap = make(ItemHeap, 0)
}

func (q *queue) Remove(version int64) {
	q.log.Tracef("Removing item version: %d", version)
	delete(q.items, version)

	// ensure heap removal
	for i, heapItem := range q.heap {
		if heapItem.Version == version {
			q.log.Tracef("Removing item version from heap: %d", version)
			heap.Remove(&q.heap, i)
			break
		}
	}
}

type Item struct {
	Version int64
	Spec    *v1beta1.Device
}

// newItem creates a new queue item.
func newItem(data *v1beta1.Device) (*Item, error) {
	version, err := stringToInt64(data.Version())
	if err != nil {
		return nil, err
	}
	return &Item{
		Spec:    data,
		Version: version,
	}, nil
}

// ItemHeap is a priority queue that orders items by version.
type ItemHeap []*Item

func (h ItemHeap) Len() int {
	return len(h)
}

func (h ItemHeap) Less(i, j int) bool {
	return h[i].Version < h[j].Version
}

func (h ItemHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
}

func (h *ItemHeap) Push(x interface{}) {
	*h = append(*h, x.(*Item))
}

func (h *ItemHeap) Pop() interface{} {
	old := *h
	n := len(old)
	item := old[n-1]
	*h = old[0 : n-1]
	return item
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
