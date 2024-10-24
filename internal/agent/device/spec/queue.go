package spec

import (
	"container/heap"
	"fmt"
	"time"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/pkg/log"
)

var _ PriorityQueue = (*queue)(nil)

type queue struct {
	heap           ItemHeap
	items          map[int64]*Item
	requeueStatus  map[int64]*requeueVersion
	failedVersions map[int64]struct{}
	// maxRetries is the number of times a template version can be requeued before being removed.
	// A value of 0 means infinite retries.
	maxRetries int
	maxSize    int
	// requeueThreshold is the number of times a template version can be requeued before enforcing a delay.
	// A value of 0 means this is disabled.
	requeueThreshold              int
	requeueThresholdDelayDuration time.Duration

	log *log.PrefixLogger
}

// newQueue creates a new queue. The queue is a priority queue that orders items by version.
// The queue will remove the lowest version when the maxSize is exceeded. MaxRetries is the number of times
// a template version can be requeued before being removed. A value of 0 means infinite retries.
func newQueue(
	log *log.PrefixLogger,
	maxRetries,
	maxSize,
	requeueThreshold int,
	requeueThresholdDelayDuration time.Duration,
) *queue {
	return &queue{
		heap:                          make(ItemHeap, 0),
		items:                         make(map[int64]*Item),
		failedVersions:                make(map[int64]struct{}),
		requeueStatus:                 make(map[int64]*requeueVersion),
		maxRetries:                    maxRetries,
		maxSize:                       maxSize,
		requeueThreshold:              requeueThreshold,
		requeueThresholdDelayDuration: requeueThresholdDelayDuration,
		log:                           log,
	}
}

type requeueVersion struct {
	count         int
	nextAvailable time.Time
	tries         int
}

func (q *queue) Add(item *Item) error {
	version := item.Version
	if _, failed := q.failedVersions[version]; failed {
		q.log.Debugf("Skipping adding failed template version: %d", version)
		return nil
	}

	if item.Spec == nil {
		return fmt.Errorf("item spec is nil")
	}

	// prune requeue status
	q.pruneRequeueStatus()

	// handle requeue status logic
	if requeue, exists := q.requeueStatus[version]; exists {
		q.log.Debugf("Template version already in queue: %d", version)

		// if the queue is empty and a requeue threshold is set, enforce requeue delay
		if q.requeueThreshold > 0 && q.heap.Len() == 0 {
			requeue.count++
			if requeue.count >= q.requeueThreshold && requeue.nextAvailable.IsZero() {
				requeue.nextAvailable = time.Now().Add(q.requeueThresholdDelayDuration)
				q.log.Debugf("Requeue threshold exceeded for version %d. Next available in %s", version, q.requeueThresholdDelayDuration.String())
			}
		} else if q.maxRetries > 0 && requeue.tries >= q.maxRetries {
			q.log.Debugf("Max retries exceeded for version %d", version)
			q.SetVersionFailed(item.Spec.RenderedVersion)
			return nil
		}
	} else {
		// initialize requeue tracking
		q.log.Debugf("Adding template version to the queue: %d", version)
		q.requeueStatus[version] = &requeueVersion{}
	}

	if q.enforceMaxSize() {
		q.log.Debugf("Skipping adding template version due to queue size limit: %d", version)
		return nil
	}

	q.items[version] = item
	heap.Push(&q.heap, item)
	q.log.Debugf("Heap size: %d", q.heap.Len())

	return nil
}

func (q *queue) Remove(version string) {
	q.remove(version)
}

func (q *queue) Next() (*Item, bool) {
	if q.heap.Len() == 0 {
		return nil, false
	}
	item := heap.Pop(&q.heap).(*Item)
	now := time.Now()

	requeue, exists := q.requeueStatus[item.Version]
	if exists && now.Before(requeue.nextAvailable) {
		// not yet available push it back and skip
		heap.Push(&q.heap, item)
		q.log.Debugf("Template version %d is not yet ready. Will retry at: %s", item.Version, requeue.nextAvailable.Format(time.RFC3339))
		return nil, false
	}

	if exists {
		// reset delay
		requeue.nextAvailable = time.Time{}
		q.log.Debugf("Template version is now available for retrieval: %d", item.Version)
	}

	delete(q.items, item.Version)
	if item.Spec != nil {
		q.log.Debugf("Retrieved template version from the queue: %d", item.Version)
		requeue.tries++
		return item, true
	}

	return nil, false
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
	q.failedVersions = make(map[int64]struct{})
	q.requeueStatus = make(map[int64]*requeueVersion)
}

func (q *queue) IsVersionFailed(version string) bool {
	_, ok := q.failedVersions[stringToInt64(version)]
	return ok
}

func (q *queue) SetVersionFailed(version string) {
	q.failedVersions[stringToInt64(version)] = struct{}{}
	delete(q.requeueStatus, stringToInt64(version))
	q.remove(version)
}

// enforceMaxSize returns true amd removes the lowest version from the queue if
// the maxSize is exceeded and the version has been tried at least once.
func (q *queue) enforceMaxSize() bool {
	q.log.Debug("Evaluating queue size limits")
	if q.maxSize > 0 && q.heap.Len() > 0 && len(q.items) >= q.maxSize {
		removed := heap.Pop(&q.heap).(*Item)
		if r, exists := q.requeueStatus[removed.Version]; exists {
			if r.tries == 0 {
				// push back the removed item
				heap.Push(&q.heap, removed)
				return true
			}

			delete(q.items, removed.Version)
			q.log.Debugf("Queue exceeded max size, removed version: %d", removed.Version)
		}
	}
	return false
}

func (q *queue) pruneRequeueStatus() {
	// give some buffer to the requeue status
	maxRequeueSize := 5 * q.maxSize
	if len(q.requeueStatus) > maxRequeueSize {
		var minVersion int64
		var initialized bool

		for version := range q.requeueStatus {
			if !initialized || version < minVersion {
				minVersion = version
				initialized = true
			}
		}
		if initialized {
			delete(q.requeueStatus, minVersion)
			q.log.Debugf("Evicted lowest template version: %d", minVersion)
		}
	}
}

// removes an item from the queue. If the item is not in the queue, it will be skipped.
func (q *queue) remove(version string) {
	v := stringToInt64(version)
	if _, ok := q.items[v]; ok {
		q.log.Debugf("Forgetting template version %v", v)
		delete(q.items, v)
	}

	// ensure heap removal
	for i, heapItem := range q.heap {
		if heapItem.Version == v {
			q.log.Debugf("Removing template version from heap: %d", v)
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
