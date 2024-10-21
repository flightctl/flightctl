package spec

import (
	"container/heap"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/pkg/log"
)

type Queue struct {
	heap           ItemHeap
	items          map[int64]*Item
	failedVersions map[int64]struct{}
	// maxRetries is the number of times a template version can be requeued before being removed.
	// A value of 0 means infinite retries.
	maxRetries int
	maxSize    int

	log *log.PrefixLogger
}

// NewQueue creates a new queue. The queue is a priority queue that orders items by version.
// The queue will remove the lowest version when the maxSize is exceeded. MaxRetries is the number of times
// a template version can be requeued before being removed. A value of 0 means infinite retries.
func NewQueue(log *log.PrefixLogger, maxRetries, maxSize int) *Queue {
	return &Queue{
		heap:           make(ItemHeap, 0),
		items:          make(map[int64]*Item),
		failedVersions: make(map[int64]struct{}),
		maxRetries:     maxRetries,
		maxSize:        maxSize,
		log:            log,
	}
}

// Add adds an item to the queue. If the item is already in the queue, it will be skipped.
func (q *Queue) Add(item *Item) {
	version := item.Version()
	if _, ok := q.failedVersions[version]; ok {
		q.log.Debugf("Skipping adding to queue for failed template version: %d", version)
		return
	}

	if _, exists := q.items[version]; exists {
		q.log.Debugf("Version %d is already in the queue. Skipping add", version)
		return
	}

	q.items[version] = item
	heap.Push(&q.heap, item)

	if len(q.items) > q.maxSize {
		// remove the lowest version
		removed := heap.Pop(&q.heap).(*Item)
		delete(q.items, removed.Version())
		q.log.Debugf("Queue exceeded max size removed version: %d", removed.Version())
	}
}

// Get returns the next item in the queue. Returns false if the queue is empty.
func (q *Queue) Get() (*Item, bool) {
	if q.heap.Len() == 0 {
		return nil, false
	}

	// pop off the lowest version
	item := heap.Pop(&q.heap).(*Item)

	return item, true
}

// Requeue requeues an item in the queue. If the item is not in the queue, it
// will be skipped.  If the maxRetries is exceeded, the item will be removed
// from the queue. If the queue is full, the lowest version will be removed.
func (q *Queue) Requeue(version int64) {
	item, ok := q.items[version]
	if !ok {
		q.log.Debugf("Template version not found in queue skipping requeue: %d", version)
		return
	}

	// remove if max retries are exceeded
	if q.maxRetries > 0 && item.Retries() >= q.maxRetries {
		q.log.Debugf("Max retries reached for template version: %v", item.Version())
		q.SetVersionFailed(version)
		q.Forget(version)
		return
	}

	item.retries++

	// clean up the heap to reduce duplicates
	for i, heapItem := range q.heap {
		if heapItem.Version() == version {
			q.log.Debugf("Removing template version from heap before requeue: %d", version)
			heap.Remove(&q.heap, i)
			break
		}
	}

	q.log.Debugf("Requeuing template version: %d with retries: %d", version, item.Retries())
	heap.Push(&q.heap, item)

	// ensure maxSize of the queue
	if len(q.items) > q.maxSize {
		removed := heap.Pop(&q.heap).(*Item)
		q.log.Debugf("Queue exceeded max size removed template version: %v", removed.Version())
		delete(q.items, removed.Version())
	}
}

// Forget removes an item from the queue. If the item is not in the queue, it will be skipped.
func (q *Queue) Forget(version int64) {
	if _, ok := q.items[version]; ok {
		q.log.Debugf("Forgetting template version %v", version)
		delete(q.items, version)
	}

	// ensure heap removal
	for i, heapItem := range q.heap {
		if heapItem.Version() == version {
			q.log.Debugf("Removing template version from heap: %d", version)
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
func (q *Queue) SetVersionFailed(version int64) {
	q.log.Debugf("Setting version %v as failed", version)
	q.failedVersions[version] = struct{}{}
}

// IsVersionFailed returns true if a template version is marked as failed.
func (q *Queue) IsVersionFailed(version int64) bool {
	_, ok := q.failedVersions[version]
	return ok
}

type Item struct {
	version int64
	spec    *v1alpha1.RenderedDeviceSpec
	retries int
}

// NewItem creates a new queue item.
func NewItem(data *v1alpha1.RenderedDeviceSpec, version int64) *Item {
	return &Item{
		spec:    data,
		version: version,
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

// Retries returns the number of times the item has been requeued.
func (i *Item) Retries() int {
	return i.retries
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
