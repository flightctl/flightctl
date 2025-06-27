package queues

import (
	"container/heap"

	"golang.org/x/exp/constraints"
)

const (
	// UnboundedSize is the default maximum size for a queue, meaning it has no size limit.
	UnboundedSize = 0
)

// Extractor defines the function signature for extracting the priority from a value.
type Extractor[T any, P comparable] func(value T) P

// Comparator defines the function signature for comparing two priorities.
// It should return true if priority 'a' is higher than priority 'b'.
type Comparator[P any] func(a, b P) bool

// Min is a generic comparator for creating a min-heap.
// It works with any type that supports the '<' operator.
func Min[P constraints.Ordered](a, b P) bool {
	return a < b
}

// Max is a generic comparator for creating a max-heap.
// It works with any type that supports the '>' operator.
func Max[P constraints.Ordered](a, b P) bool {
	return a > b
}

// Option is a function that configures a IndexedPriorityQueue.
type Option[T any, P comparable] func(*IndexedPriorityQueue[T, P])

// WithMaxSize returns an Option that sets the maximum size of the queue.
// If size is 0 or less, the queue is considered unbounded.
func WithMaxSize[T any, P comparable](size int) Option[T, P] {
	return func(q *IndexedPriorityQueue[T, P]) {
		if size > UnboundedSize {
			q.maxSize = size
		}
	}
}

// Item represents a single element in the priority queue.
// It has a generic Value and a Priority of type P.
type Item[T any, P comparable] struct {
	Value    T
	Priority P
}

// itemHeap is a min-heap of Items, ordered by Priority.
// It implements the heap.Interface.
type itemHeap[T any, P comparable] struct {
	items      []*Item[T, P]
	comparator Comparator[P]
}

// Len returns the number of items in the heap.
func (h *itemHeap[T, P]) Len() int { return len(h.items) }

// Swap swaps the items at the given indices.
func (h *itemHeap[T, P]) Swap(i, j int) { h.items[i], h.items[j] = h.items[j], h.items[i] }

// Less uses the provided comparator to determine priority.
func (h *itemHeap[T, P]) Less(i, j int) bool {
	return h.comparator(h.items[i].Priority, h.items[j].Priority)
}

// Push adds an item to the heap.
func (h *itemHeap[T, P]) Push(x any) {
	h.items = append(h.items, x.(*Item[T, P]))
}

// Pop removes an item from the heap.
func (h *itemHeap[T, P]) Pop() any {
	old := h.items
	n := len(old)
	item := old[n-1]
	old[n-1] = nil // Prevent memory leak
	h.items = old[0 : n-1]
	return item
}

// IndexedPriorityQueue is a generic priority queue with O(1) access by priority.
// Note: This implementation is NOT thread-safe. Concurrent access must be
// synchronized externally.
type IndexedPriorityQueue[T any, P comparable] struct {
	heap      *itemHeap[T, P]
	items     map[P]*Item[T, P] // The key is now the generic, comparable priority type P
	maxSize   int
	extractor Extractor[T, P] // The new extractor function
}

// NewIndexedPriorityQueue creates a new generic priority queue.
func NewIndexedPriorityQueue[T any, P comparable](
	comparator Comparator[P],
	extractor Extractor[T, P],
	opts ...Option[T, P], // Variadic options
) *IndexedPriorityQueue[T, P] {
	q := &IndexedPriorityQueue[T, P]{
		heap: &itemHeap[T, P]{
			items:      make([]*Item[T, P], 0),
			comparator: comparator,
		},
		items:     make(map[P]*Item[T, P]),
		maxSize:   UnboundedSize,
		extractor: extractor,
	}
	for _, opt := range opts {
		opt(q)
	}

	return q
}

// Add is the new, simplified public method.
// It takes the raw value, extracts its priority, and adds it to the queue.
// If the new value added would cause the size to exceed the configured max size,
// the highest priority item will be evicted. For a min-heap, this is the smallest value
func (q *IndexedPriorityQueue[T, P]) Add(value T) {
	priority := q.extractor(value)
	item := &Item[T, P]{
		Value:    value,
		Priority: priority,
	}
	q.addItem(item)
}

func (q *IndexedPriorityQueue[T, P]) addItem(item *Item[T, P]) {
	if _, exists := q.items[item.Priority]; exists {
		return
	}

	if q.maxSize != UnboundedSize && len(q.items) >= q.maxSize {
		removed := heap.Pop(q.heap).(*Item[T, P])
		delete(q.items, removed.Priority)
	}

	q.items[item.Priority] = item
	heap.Push(q.heap, item)
}

// Pop returns and removes the highest-priority item from the queue.
func (q *IndexedPriorityQueue[T, P]) Pop() (T, bool) {
	if len(q.heap.items) == 0 {
		var zero T // The zero value for type T
		return zero, false
	}
	item := heap.Pop(q.heap).(*Item[T, P])
	delete(q.items, item.Priority)
	return item.Value, true
}

// Peek returns the highest-priority item without removing it from the queue.
func (q *IndexedPriorityQueue[T, P]) Peek() (T, bool) {
	if len(q.heap.items) == 0 {
		var zero T
		return zero, false
	}
	return q.heap.items[0].Value, true
}

// PeekAt returns the item with the given priority without removing it.
func (q *IndexedPriorityQueue[T, P]) PeekAt(priority P) (T, bool) {
	item, exists := q.items[priority]
	if !exists {
		var zero T
		return zero, false
	}
	return item.Value, true
}

// RemoveUpTo removes all items from the queue that have a higher priority
// (as defined by the comparator) than the given priority.
func (q *IndexedPriorityQueue[T, P]) RemoveUpTo(priority P) {
	for {
		if len(q.heap.items) == 0 {
			break
		}
		topPriority := q.heap.items[0].Priority
		// If topPriority is not higher than the given priority, stop.
		if !q.heap.comparator(topPriority, priority) {
			break
		}
		// Otherwise, pop the item.
		item := heap.Pop(q.heap).(*Item[T, P])
		delete(q.items, item.Priority)
	}
}

// Size returns the number of items in the queue.
func (q *IndexedPriorityQueue[T, P]) Size() int {
	return len(q.items)
}

// IsEmpty returns true if the queue is empty.
func (q *IndexedPriorityQueue[T, P]) IsEmpty() bool {
	return q.Size() == 0
}

// Clear removes all items from the queue.
func (q *IndexedPriorityQueue[T, P]) Clear() {
	q.items = make(map[P]*Item[T, P])
	q.heap.items = make([]*Item[T, P], 0)
}

// Remove removes an item from the queue by its priority.
func (q *IndexedPriorityQueue[T, P]) Remove(priority P) {
	delete(q.items, priority)

	// ensure heap removal
	for i, heapItem := range q.heap.items {
		if heapItem.Priority == priority {
			heap.Remove(q.heap, i)
			break
		}
	}
}
