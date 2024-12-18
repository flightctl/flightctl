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

type requeueState struct {
	count             int
	nextAvailable     time.Time
	tries             int
	downloadSatisfied bool
	updateSatisfied   bool
}

type queueManager struct {
	queue                 *queue
	policyManager         policy.Manager
	failedVersions        map[int64]struct{}
	requeueStatus         map[int64]*requeueState
	maxRetries            int
	requeueDelayThreshold int
	requeueDelayDuration  time.Duration

	log *log.PrefixLogger
}

func newPriorityQueue(log *log.PrefixLogger, policyManager policy.Manager) PriorityQueue {
	return &queueManager{
		queue:                 newQueue(log, defaultSpecQueueSize),
		policyManager:         policyManager,
		failedVersions:        make(map[int64]struct{}),
		requeueStatus:         make(map[int64]*requeueState),
		maxRetries:            defaultSpecRequeueMaxRetries,
		requeueDelayThreshold: defaultSpecRequeueThreshold,
		requeueDelayDuration:  defaultSpecRequeueDelay,
		log:                   log,
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

	if requeue, exists := m.requeueStatus[version]; exists {
		if m.updatePolicy(ctx, requeue) {
			m.log.Debugf("Template version policy updated: %d", version)
		}

		if m.requeueDelayThreshold > 0 && m.queue.IsEmpty() && requeue.tries > 0 {
			requeue.count++
			if requeue.count >= m.requeueDelayThreshold && requeue.nextAvailable.IsZero() {
				requeue.nextAvailable = time.Now().Add(m.requeueDelayDuration)
				m.log.Debugf("Requeue delay threshold exceeded for version %d. Next available in %s", version, m.requeueDelayDuration.String())
			}
		} else if m.maxRetries > 0 && requeue.tries >= m.maxRetries {
			m.log.Debugf("Max retries exceeded for version %d", version)
			m.SetFailed(spec.RenderedVersion)
			return
		}
	} else {
		m.log.Debugf("Adding template version to the queue: %d", version)
		requeue := &requeueState{}
		if m.updatePolicy(ctx, requeue) {
			m.log.Debugf("Template version policy updated: %d", version)
		}
		m.requeueStatus[version] = requeue
	}

	m.queue.Add(item)
}

func (m *queueManager) updatePolicy(ctx context.Context, requeue *requeueState) bool {
	changed := false
	if !requeue.downloadSatisfied {
		if m.policyManager.IsReady(ctx, policy.Download) {
			changed = true
			requeue.downloadSatisfied = true
		}
	}

	if !requeue.updateSatisfied {
		if m.policyManager.IsReady(ctx, policy.Update) {
			changed = true
			requeue.updateSatisfied = true
		}
	}
	return changed
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

	now := time.Now()
	requeue, exists := m.requeueStatus[item.Version]
	if exists && now.Before(requeue.nextAvailable) {
		m.queue.Add(item)
		m.log.Debugf("Template version %d is not yet ready. Will retry at: %s", item.Version, requeue.nextAvailable.Format(time.RFC3339))
		return nil, false
	}
	if m.updatePolicy(ctx, requeue) {
		m.log.Debugf("Template version policy updated: %d", item.Version)
	}

	if !m.isUpdatePolicyReady(requeue) {
		m.log.Debugf("Template version %d is not ready due to policy", item.Version)
		m.queue.Add(item)
		return nil, false
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

func (m *queueManager) isUpdatePolicyReady(requeue *requeueState) bool {
	return requeue.downloadSatisfied || requeue.updateSatisfied
}

func (m *queueManager) CheckPolicy(ctx context.Context, policyType policy.Type, version string) error {
	v := stringToInt64(version)
	requeue, exists := m.requeueStatus[v]
	if !exists {
		// this would be very unexpected so we would need to requeue the version
		return fmt.Errorf("%w: version %d", errors.ErrRetryable, v)
	}
	m.log.Debugf("Requeue state: %+v", requeue)

	switch policyType {
	case policy.Download:
		if requeue.downloadSatisfied {
			return nil
		}
		return errors.ErrDownloadPolicyNotReady
	case policy.Update:
		if requeue.updateSatisfied {
			return nil
		}
		return errors.ErrUpdatePolicyNotReady
	default:
		return fmt.Errorf("%w: %s", errors.ErrInvalidPolicyType, policyType)
	}
}

func (m *queueManager) SetFailed(version string) {
	v := stringToInt64(version)
	m.failedVersions[v] = struct{}{}
	delete(m.requeueStatus, v)
	m.queue.Remove(v)
}

func (m *queueManager) IsFailed(version string) bool {
	_, ok := m.failedVersions[stringToInt64(version)]
	return ok
}

func (m *queueManager) pruneRequeueStatus() {
	maxRequeueSize := 5 * m.queue.maxSize
	if m.queue.maxSize == 0 {
		return
	}

	if len(m.requeueStatus) > maxRequeueSize {
		var minVersion int64
		var initialized bool

		for version := range m.requeueStatus {
			if !initialized || version < minVersion {
				minVersion = version
				initialized = true
			}
		}
		if initialized {
			delete(m.requeueStatus, minVersion)
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
		q.log.Debugf("Skipping item with version %d already in queue", version)
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
	q.log.Debugf("Added item version %d, heap size now %d", version, q.heap.Len())
}

func (q *queue) Pop() (*Item, bool) {
	if q.heap.Len() == 0 {
		return nil, false
	}
	item := heap.Pop(&q.heap).(*Item)
	delete(q.items, item.Version)
	q.log.Debugf("Popped item version %d, heap size now %d", item.Version, q.heap.Len())
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
