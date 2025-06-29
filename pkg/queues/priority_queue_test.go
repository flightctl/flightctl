package queues

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// testStruct is a simple struct used for testing the queue.
type testStruct struct {
	id       string
	priority int64
}

// testExtractor knows how to get the priority from a testStruct.
func testExtractor(item *testStruct) int64 {
	return item.priority
}

// complexPriority is a struct used as a more complex priority type.
type complexPriority struct {
	level    int
	subLevel string
}

// complexTestStruct is a struct using complexPriority.
type complexTestStruct struct {
	name     string
	priority complexPriority
}

// complexExtractor extracts complexPriority from complexTestStruct.
func complexExtractor(item *complexTestStruct) complexPriority {
	return item.priority
}

// complexMinComparator defines a min-heap ordering for complexPriority.
// It prioritizes lower level, then lexicographically smaller subLevel.
func complexMinComparator(a, b complexPriority) bool {
	if a.level != b.level {
		return a.level < b.level
	}
	return a.subLevel < b.subLevel
}

// complexMaxComparator defines a max-heap ordering for complexPriority.
// It prioritizes higher level, then lexicographically larger subLevel.
func complexMaxComparator(a, b complexPriority) bool {
	if a.level != b.level {
		return a.level > b.level
	}
	return a.subLevel > b.subLevel
}

func TestIndexedPriorityQueue_AddPop(t *testing.T) {
	testCases := []struct {
		name          string
		items         []*testStruct
		maxSize       int
		comparator    Comparator[int64]
		expectedOrder []string // IDs in expected pop order
		expectedSize  int
	}{
		{
			name: "min-heap basic ordering",
			items: []*testStruct{
				{id: "c", priority: 3},
				{id: "a", priority: 1},
				{id: "b", priority: 2},
			},
			maxSize:       UnboundedSize,
			comparator:    Min[int64],
			expectedOrder: []string{"a", "b", "c"},
			expectedSize:  3,
		},
		{
			name: "max-heap basic ordering",
			items: []*testStruct{
				{id: "c", priority: 3},
				{id: "a", priority: 1},
				{id: "b", priority: 2},
			},
			maxSize:       UnboundedSize,
			comparator:    Max[int64],
			expectedOrder: []string{"c", "b", "a"},
			expectedSize:  3,
		},
		{
			name: "eviction with maxSize (min-heap)",
			items: []*testStruct{
				{id: "a", priority: 1}, // Should be evicted
				{id: "c", priority: 3},
				{id: "b", priority: 2},
			},
			maxSize:       2,
			comparator:    Min[int64],
			expectedOrder: []string{"b", "c"},
			expectedSize:  2,
		},
		{
			name: "add item with same priority (should be ignored)",
			items: []*testStruct{
				{id: "a", priority: 1},
				{id: "a-duplicate", priority: 1},
			},
			maxSize:       5,
			comparator:    Min[int64],
			expectedOrder: []string{"a"},
			expectedSize:  1,
		},
		{
			name: "unbounded size",
			items: []*testStruct{
				{id: "a", priority: 1},
				{id: "b", priority: 2},
				{id: "c", priority: 3},
				{id: "d", priority: 4},
			},
			maxSize:       UnboundedSize,
			comparator:    Min[int64],
			expectedOrder: []string{"a", "b", "c", "d"},
			expectedSize:  4,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)

			var opts []Option[*testStruct, int64]
			if tc.maxSize != UnboundedSize {
				opts = append(opts, WithMaxSize[*testStruct, int64](tc.maxSize))
			}

			q := NewIndexedPriorityQueue(tc.comparator, testExtractor, opts...)

			for _, item := range tc.items {
				q.Add(item)
			}

			require.Equal(tc.expectedSize, q.Size())

			poppedIDs := make([]string, 0)
			for {
				item, ok := q.Pop()
				if !ok {
					break
				}
				poppedIDs = append(poppedIDs, item.id)
			}

			require.Equal(tc.expectedOrder, poppedIDs)
			require.True(q.IsEmpty())
		})
	}
}

func TestIndexedPriorityQueue_ComplexPriority(t *testing.T) {
	testCases := []struct {
		name          string
		items         []*complexTestStruct
		maxSize       int
		comparator    Comparator[complexPriority]
		expectedOrder []string // names in expected pop order
		expectedSize  int
	}{
		{
			name: "complex min-heap basic ordering",
			items: []*complexTestStruct{
				{name: "c", priority: complexPriority{level: 2, subLevel: "z"}},
				{name: "a", priority: complexPriority{level: 1, subLevel: "a"}},
				{name: "b", priority: complexPriority{level: 1, subLevel: "b"}},
				{name: "d", priority: complexPriority{level: 2, subLevel: "a"}},
			},
			maxSize:       UnboundedSize,
			comparator:    complexMinComparator,
			expectedOrder: []string{"a", "b", "d", "c"},
			expectedSize:  4,
		},
		{
			name: "complex max-heap basic ordering",
			items: []*complexTestStruct{
				{name: "c", priority: complexPriority{level: 2, subLevel: "z"}},
				{name: "a", priority: complexPriority{level: 1, subLevel: "a"}},
				{name: "b", priority: complexPriority{level: 1, subLevel: "b"}},
				{name: "d", priority: complexPriority{level: 2, subLevel: "a"}},
			},
			maxSize:       UnboundedSize,
			comparator:    complexMaxComparator,
			expectedOrder: []string{"c", "d", "b", "a"},
			expectedSize:  4,
		},
		{
			name: "complex eviction with maxSize (min-heap)",
			items: []*complexTestStruct{
				{name: "a", priority: complexPriority{level: 1, subLevel: "a"}}, // Should be evicted
				{name: "c", priority: complexPriority{level: 2, subLevel: "z"}},
				{name: "b", priority: complexPriority{level: 1, subLevel: "b"}},
			},
			maxSize:       2,
			comparator:    complexMinComparator,
			expectedOrder: []string{"b", "c"},
			expectedSize:  2,
		},
		{
			name: "complex add item with same priority (should be ignored)",
			items: []*complexTestStruct{
				{name: "a", priority: complexPriority{level: 1, subLevel: "a"}},
				{name: "a-duplicate", priority: complexPriority{level: 1, subLevel: "a"}},
			},
			maxSize:       5,
			comparator:    complexMinComparator,
			expectedOrder: []string{"a"},
			expectedSize:  1,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)

			var opts []Option[*complexTestStruct, complexPriority]
			if tc.maxSize != UnboundedSize {
				opts = append(opts, WithMaxSize[*complexTestStruct, complexPriority](tc.maxSize))
			}

			q := NewIndexedPriorityQueue(tc.comparator, complexExtractor, opts...)

			for _, item := range tc.items {
				q.Add(item)
			}

			require.Equal(tc.expectedSize, q.Size())

			poppedNames := make([]string, 0)
			for {
				item, ok := q.Pop()
				if !ok {
					break
				}
				poppedNames = append(poppedNames, item.name)
			}

			require.Equal(tc.expectedOrder, poppedNames)
			require.True(q.IsEmpty())
		})
	}
}

func TestIndexedPriorityQueue_Peek(t *testing.T) {
	require := require.New(t)
	q := NewIndexedPriorityQueue(Min[int64], testExtractor)

	// Peek empty
	_, ok := q.Peek()
	require.False(ok)

	// Add items
	q.Add(&testStruct{id: "b", priority: 2})
	q.Add(&testStruct{id: "a", priority: 1})

	// Peek highest priority
	item, ok := q.Peek()
	require.True(ok)
	require.Equal("a", item.id)

	// Ensure item was not removed
	require.Equal(2, q.Size())
	item, ok = q.Peek()
	require.True(ok)
	require.Equal("a", item.id)
}

func TestIndexedPriorityQueue_PeekAt(t *testing.T) {
	require := require.New(t)
	q := NewIndexedPriorityQueue(Min[int64], testExtractor)

	// PeekAt non-existent
	_, ok := q.PeekAt(100)
	require.False(ok)

	// Add items
	itemA := &testStruct{id: "a", priority: 1}
	itemB := &testStruct{id: "b", priority: 5}
	q.Add(itemA)
	q.Add(itemB)

	// PeekAt existing
	peekedItem, ok := q.PeekAt(5)
	require.True(ok)
	require.Equal("b", peekedItem.id)

	// Ensure item was not removed
	require.Equal(2, q.Size())
}

func TestIndexedPriorityQueue_Remove(t *testing.T) {
	require := require.New(t)
	q := NewIndexedPriorityQueue(Min[int64], testExtractor)

	items := []*testStruct{
		{id: "a", priority: 1},
		{id: "b", priority: 2},
		{id: "c", priority: 3},
		{id: "d", priority: 4},
	}
	for _, item := range items {
		q.Add(item)
	}

	// Remove item from the middle
	q.Remove(3) // Remove "c"
	require.Equal(3, q.Size())
	_, ok := q.PeekAt(3)
	require.False(ok, "item should be gone from map")

	// Check heap integrity by popping
	expectedOrder := []string{"a", "b", "d"}
	poppedIDs := make([]string, 0)
	for {
		item, ok := q.Pop()
		if !ok {
			break
		}
		poppedIDs = append(poppedIDs, item.id)
	}
	require.Equal(expectedOrder, poppedIDs)
}

func TestIndexedPriorityQueue_RemoveUpTo(t *testing.T) {
	testCases := []struct {
		name          string
		items         []*testStruct
		removeUpToP   int64
		expectedOrder []string // IDs remaining in queue, in pop order
	}{
		{
			name: "remove a subset",
			items: []*testStruct{
				{id: "a", priority: 1},
				{id: "b", priority: 2},
				{id: "c", priority: 3},
				{id: "d", priority: 4},
			},
			removeUpToP:   3, // Remove all with priority < 3
			expectedOrder: []string{"c", "d"},
		},
		{
			name: "remove all items",
			items: []*testStruct{
				{id: "a", priority: 1},
				{id: "b", priority: 2},
			},
			removeUpToP:   10,
			expectedOrder: []string{},
		},
		{
			name: "remove no items",
			items: []*testStruct{
				{id: "a", priority: 10},
				{id: "b", priority: 20},
			},
			removeUpToP:   5,
			expectedOrder: []string{"a", "b"},
		},
		{
			name:          "remove from empty queue",
			items:         []*testStruct{},
			removeUpToP:   100,
			expectedOrder: []string{},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			q := NewIndexedPriorityQueue(Min[int64], testExtractor)
			for _, item := range tc.items {
				q.Add(item)
			}

			q.RemoveUpTo(tc.removeUpToP)

			remainingIDs := make([]string, 0)
			for {
				item, ok := q.Pop()
				if !ok {
					break
				}
				remainingIDs = append(remainingIDs, item.id)
			}
			require.Equal(tc.expectedOrder, remainingIDs)
		})
	}
}

func TestIndexedPriorityQueue_ClearAndSize(t *testing.T) {
	require := require.New(t)
	q := NewIndexedPriorityQueue(Min[int64], testExtractor)

	// Initial state
	require.Equal(0, q.Size())
	require.True(q.IsEmpty())

	// Add items
	q.Add(&testStruct{id: "a", priority: 1})
	q.Add(&testStruct{id: "b", priority: 2})
	require.Equal(2, q.Size())
	require.False(q.IsEmpty())

	// Clear the queue
	q.Clear()
	require.Equal(0, q.Size())
	require.True(q.IsEmpty())

	// Pop should not work
	_, ok := q.Pop()
	require.False(ok)
}
