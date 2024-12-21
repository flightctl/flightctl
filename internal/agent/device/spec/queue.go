package spec

import (
	"container/heap"
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/policy"
	"github.com/flightctl/flightctl/pkg/log"
)

// requeueState represents the state of a queued template version.
type requeueState struct {
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
	// maxRetries is the number of times a template version can be requeued before being removed.
	// A value of 0 means infinite retries.
	maxRetries int
	// delayThreshold is the number of times a template version can be requeued before enforcing a delay.
	// A value of 0 means this is disabled.
	delayThreshold int
	// delayDuration is the duration to wait before the item is
	// available to be retrieved form the queue.
	delayDuration time.Duration

	log *log.PrefixLogger
}

func newPriorityQueue(
	maxSize int,
	maxRetries int,
	delayThreshold int,
	delayDuration time.Duration,
	policyManager policy.Manager,
	log *log.PrefixLogger,
) PriorityQueue {
	return &queueManager{
		queue:          newQueue(log, maxSize),
		policyManager:  policyManager,
		failedVersions: make(map[int64]struct{}),
		requeueLookup:  make(map[int64]*requeueState),
		maxRetries:     maxRetries,
		delayThreshold: delayThreshold,
		delayDuration:  delayDuration,
		log:            log,
	}
}

func (m *queueManager) Add(ctx context.Context, spec *v1alpha1.RenderedDeviceSpec) {
	if ctx.Err() != nil {
		return
	}

	item := newItem(spec)
	version := item.Version
	if _, failed := m.failedVersions[version]; failed {
		m.log.Debugf("Skipping adding failed template version: %d", version)
		return
	}

	m.pruneRequeueStatus()

	state := m.getOrCreateRequeueState(ctx, version)
	if m.shouldEnforceDelay(state) {
		m.log.Debugf("Enforcing delay for version: %d", version)
	}

	if m.hasExceededMaxRetries(state, version) {
		m.setFailed(version)
		return
	}

	m.queue.Add(item)
}

func (m *queueManager) Remove(version string) {
	m.queue.Remove(stringToInt64(version))
}

func (m *queueManager) Next(ctx context.Context) (*v1alpha1.RenderedDeviceSpec, bool) {
	if ctx.Err() != nil {
		return nil, false
	}
	item, ok := m.queue.Pop()
	if !ok {
		return nil, false
	}

	m.log.Debugf("Evaluating template version: %d", item.Version)
	now := time.Now()
	requeue, exists := m.requeueLookup[item.Version]
	if exists && now.Before(requeue.nextAvailable) {
		m.queue.Add(item)
		m.log.Debugf("Template version %d is not yet ready. Available after: %s", item.Version, requeue.nextAvailable.Format(time.RFC3339))
		return nil, false
	}
	if m.updatePolicy(ctx, requeue) {
		m.log.Debugf("Template version policy updated: %d", item.Version)
	}

	if exists {
		requeue.nextAvailable = time.Time{}
		m.log.Debugf("Template version is now available for retrieval: %d", item.Version)
		requeue.tries++
	}

	if item.Spec != nil {
		m.log.Debugf("Retrieved template version from the queue: %d", item.Version)
		return item.Spec, true
	}
	return nil, false
}

func (m *queueManager) CheckPolicy(ctx context.Context, policyType policy.Type, version string) error {
	v := stringToInt64(version)
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

func (m *queueManager) SetFailed(version string) {
	v := stringToInt64(version)
	m.setFailed(v)
}

func (m *queueManager) setFailed(version int64) {
	m.failedVersions[version] = struct{}{}
	delete(m.requeueLookup, version)
	m.queue.Remove(version)
}

func (m *queueManager) IsFailed(version string) bool {
	_, ok := m.failedVersions[stringToInt64(version)]
	return ok
}

func (m *queueManager) getOrCreateRequeueState(ctx context.Context, version int64) *requeueState {
	state, exists := m.requeueLookup[version]
	if !exists {
		m.log.Debugf("Initializing requeueState for version %d", version)
		state = &requeueState{}
		m.requeueLookup[version] = state
	}

	if m.updatePolicy(ctx, state) {
		m.log.Debugf("Policy updated for version %d", version)
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

		for version := range m.requeueLookup {
			if !initialized || version < minVersion {
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
	if _, ok := q.items[version]; ok {
		delete(q.items, version)
		q.log.Debugf("Forgetting item version %v", version)
	}

	// ensure heap removal
	for i, heapItem := range q.heap {
		if heapItem.Version == version {
			q.log.Debugf("Removing item version from heap: %d", version)
			heap.Remove(&q.heap, i)
			break
		}
	}
}

type Item struct {
	Version int64
	Spec    *v1alpha1.RenderedDeviceSpec
}

// newItem creates a new queue item.
func newItem(data *v1alpha1.RenderedDeviceSpec) *Item {
	return &Item{
		Spec:    data,
		Version: stringToInt64(data.RenderedVersion),
	}
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

func stringToInt64(s string) int64 {
	i, _ := strconv.ParseInt(s, 10, 64)
	return i
}
