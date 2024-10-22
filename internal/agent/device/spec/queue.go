package spec

import (
	"container/heap"
	"time"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/pkg/log"
)

type Queue struct {
	heap           ItemHeap
	items          map[int64]*Item
	requeueTracker map[int64]*requeueVersion
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
) *Queue {
	return &Queue{
		heap:                          make(ItemHeap, 0),
		items:                         make(map[int64]*Item),
		failedVersions:                make(map[int64]struct{}),
		requeueTracker:                make(map[int64]*requeueVersion),
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
	retries       int
}

// Add adds an item to the queue. If the item is already in the queue, it will be skipped.
func (q *Queue) Add(item *Item) {
	version := item.Version()
	if _, ok := q.failedVersions[version]; ok {
		q.log.Debugf("Skipping adding to queue for failed template version: %d", version)
		return
	}

	if requeue, exists := q.requeueTracker[version]; exists {
		q.log.Debugf("Template version is already in the queue: %d", version)

		// if requeueThreshold is set, enforce a delay before requeuing only if the queue has no items
		if q.requeueThreshold > 0 && q.heap.Len() == 0 {
			requeue.count++

			// If requeueThreshold is set, enforce a delay before requeuing
			if requeue.count >= q.requeueThreshold && requeue.nextAvailable.IsZero() {
				requeue.nextAvailable = time.Now().Add(q.requeueThresholdDelayDuration)
				q.log.Debugf("Threshold exceeded for version %d. Next available in %s", version, q.requeueThresholdDelayDuration.String())
			}
		}
		// return
	} else {
		q.log.Debugf("Adding template version to the queue: %d", version)
		q.requeueTracker[version] = &requeueVersion{}
	}

	if q.maxSize > 0 && len(q.items) >= q.maxSize {
		// remove the lowest version
		removed := heap.Pop(&q.heap).(*Item)
		delete(q.items, removed.Version())
		q.log.Debugf("Queue exceeded max size removed version: %d", removed.Version())
	}

	q.items[version] = item
	heap.Push(&q.heap, item)

	q.log.Debugf("Heap size: %d", q.heap.Len())
}

// Get returns the next item in the queue. Returns false if the queue is empty.
func (q *Queue) Get() (*v1alpha1.RenderedDeviceSpec, bool) {
	if q.heap.Len() == 0 {
		return nil, false
	}
	item := heap.Pop(&q.heap).(*Item)
	now := time.Now()

	requeue, exists := q.requeueTracker[item.Version()]
	// check if item is available
	if exists && !requeue.nextAvailable.IsZero() {
		if now.Before(requeue.nextAvailable) {
			// not yet available push it back and skip
			heap.Push(&q.heap, item)
			q.log.Debugf("Template version is not ready: %d", item.Version())
			return nil, false
		}
		// reset delay
		requeue.nextAvailable = time.Time{}
		q.log.Debugf("Template version is now available for retrieval: %d", item.Version())
	}

	delete(q.items, item.Version())
	q.log.Debugf("Retrieved template version from the queue: %d", item.Version())

	if item.spec != nil {
		return item.spec, true
	}

	return nil, false
}

// Requeue requeues an item in the queue. If the item is not in the queue, it
// will be skipped.  If the maxRetries is exceeded, the item will be removed
// from the queue. If the queue is full, the lowest version will be removed.
func (q *Queue) Requeue(version string) {
	v := stringToInt64(version)
	item, ok := q.items[v]
	if !ok {
		q.log.Debugf("Template version not found in queue skipping requeue: %s", version)
		return
	}

	if requeue, exists := q.requeueTracker[v]; exists {
		// remove if max retries are exceeded
		if q.maxRetries > 0 && requeue.retries >= q.maxRetries {
			q.log.Debugf("Max retries reached for template version: %s", version)
			q.SetVersionFailed(version)
			return
		}
		requeue.retries++
	}

	// clean up the heap to reduce duplicates
	for i, heapItem := range q.heap {
		if heapItem.Version() == v {
			q.log.Debugf("Removing template version from heap before requeue: %s", version)
			heap.Remove(&q.heap, i)
			break
		}
	}

	heap.Push(&q.heap, item)

	// ensure maxSize of the queue
	if len(q.items) > q.maxSize {
		removed := heap.Pop(&q.heap).(*Item)
		q.log.Debugf("Queue exceeded max size removed template version: %v", removed.Version())
		delete(q.items, removed.Version())
	}
}

// Forget removes an item from the queue. If the item is not in the queue, it will be skipped.
func (q *Queue) forget(version string) {
	v := stringToInt64(version)
	if _, ok := q.items[v]; ok {
		q.log.Debugf("Forgetting template version %v", v)
		delete(q.items, v)
	}

	// ensure heap removal
	for i, heapItem := range q.heap {
		if heapItem.Version() == v {
			q.log.Debugf("Removing template version from heap: %d", v)
			heap.Remove(&q.heap, i)
			break
		}
	}
}

// Len returns the number of items in the queue.
func (q *Queue) Len() int {
	return len(q.items)
}

// IsEmpty returns true if the queue is empty.
func (q *Queue) IsEmpty() bool {
	return q.Len() == 0
}

// SetVersionFailed marks a version as failed. Failed versions will not be requeued.
func (q *Queue) SetVersionFailed(version string) {
	q.log.Debugf("Setting version %v as failed", version)
	q.failedVersions[stringToInt64(version)] = struct{}{}
	delete(q.requeueTracker, stringToInt64(version))
	q.forget(version)
}

// IsVersionFailed returns true if a template version is marked as failed.
func (q *Queue) IsVersionFailed(version string) bool {
	_, ok := q.failedVersions[stringToInt64(version)]
	return ok
}

type Item struct {
	version int64
	spec    *v1alpha1.RenderedDeviceSpec
}

// newItem creates a new queue item.
func newItem(data *v1alpha1.RenderedDeviceSpec) *Item {
	return &Item{
		spec:    data,
		version: stringToInt64(data.RenderedVersion),
	}
}

// Version returns the template version of the item.
func (i *Item) Version() int64 {
	return i.version
}

// Spec returns the rendered device spec of the item.
func (i *Item) Spec() *v1alpha1.RenderedDeviceSpec {
	return i.spec
}

// ItemHeap is a priority queue that orders items by version.
type ItemHeap []*Item

func (h ItemHeap) Len() int {
	return len(h)
}

func (h ItemHeap) Less(i, j int) bool {
	return h[i].Version() < h[j].Version()
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
