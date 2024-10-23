package spec

import (
	"container/heap"
	"testing"
	"time"

	v1alpha1 "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

func TestQueue(t *testing.T) {
	testCases := []struct {
		name            string
		maxRetries      int
		maxSize         int
		items           []*Item
		expectOrder     []string
		expectedRequeue map[int64]int
	}{
		{
			name:       "ensure priory ordering",
			maxRetries: 3,
			maxSize:    10,
			items: []*Item{
				{Version: 3, Spec: &v1alpha1.RenderedDeviceSpec{RenderedVersion: "3"}},
				{Version: 1, Spec: &v1alpha1.RenderedDeviceSpec{RenderedVersion: "1"}},
				{Version: 2, Spec: &v1alpha1.RenderedDeviceSpec{RenderedVersion: "2"}},
			},
			expectOrder: []string{"1", "2", "3"},
		},
		{
			name:       "maxSize exceeded lowest version not tried yet",
			maxRetries: 3,
			maxSize:    2,
			items: []*Item{
				{Version: 1, Spec: &v1alpha1.RenderedDeviceSpec{RenderedVersion: "1"}},
				{Version: 2, Spec: &v1alpha1.RenderedDeviceSpec{RenderedVersion: "2"}},
				{Version: 3, Spec: &v1alpha1.RenderedDeviceSpec{RenderedVersion: "3"}},
			},
			expectOrder: []string{"1", "2"}, // 3 was skipped
		},
		{
			name:       "requeue with maxRetries exceeded",
			maxRetries: 2,
			maxSize:    10,
			items: []*Item{
				{Version: 1, Spec: &v1alpha1.RenderedDeviceSpec{RenderedVersion: "1"}},
			},
			expectOrder: []string{}, // remove item after maxRetries
		},
		{
			name:       "requeue within maxRetries",
			maxRetries: 3,
			maxSize:    10,
			items: []*Item{
				{Version: 1, Spec: &v1alpha1.RenderedDeviceSpec{RenderedVersion: "1"}},
			},
			expectOrder: []string{"1"},
		},
		{
			name:       "adding new item after requeue",
			maxRetries: 3,
			maxSize:    10,
			items: []*Item{
				{Version: 1, Spec: &v1alpha1.RenderedDeviceSpec{RenderedVersion: "1"}},
			},
			expectOrder: []string{"1"},
		},
		{
			name:       "requeue different versions",
			maxRetries: 3,
			maxSize:    10,
			items: []*Item{
				{Version: 1, Spec: &v1alpha1.RenderedDeviceSpec{RenderedVersion: "1"}},
				{Version: 2, Spec: &v1alpha1.RenderedDeviceSpec{RenderedVersion: "2"}},
			},
			expectOrder: []string{"1", "2"},
		},
		{
			name:       "requeue without maxRetries hit",
			maxRetries: 5,
			maxSize:    10,
			items: []*Item{
				{Version: 1, Spec: &v1alpha1.RenderedDeviceSpec{RenderedVersion: "1"}},
			},
			expectOrder: []string{"1"},
		},
	}

	for _, tt := range testCases {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			log := log.NewPrefixLogger("test")
			q := newQueue(log, tt.maxRetries, tt.maxSize, defaultSpecRequeueThreshold, defaultSpecRequeueDelay)

			// add to queue
			for _, item := range tt.items {
				err := q.Add(item)
				require.NoError(err)
			}

			// ensure priority ordering
			for _, expectedVersion := range tt.expectOrder {
				item, ok := q.Next()
				require.True(ok)
				require.Equal(expectedVersion, item.Spec.RenderedVersion)
			}
		})
	}
}

func TestRequeueThreshold(t *testing.T) {
	require := require.New(t)
	testCases := []struct {
		name                     string
		requeueThreshold         int
		requeueThresholdDuration time.Duration
		versionToRequeue         string
		expectedNextAvailable    bool
		sleepDuration            time.Duration
	}{
		{
			name:                     "test requeue threshold",
			requeueThreshold:         1,
			requeueThresholdDuration: time.Millisecond * 200,
			versionToRequeue:         "1",
		},
		// TODO: extend test coverage
	}

	for _, tt := range testCases {
		t.Run(tt.name, func(t *testing.T) {
			log := log.NewPrefixLogger("test")
			log.SetLevel(logrus.DebugLevel)
			maxSize := 1
			q := newQueue(log, 0, maxSize, tt.requeueThreshold, tt.requeueThresholdDuration)
			item := newItem(&v1alpha1.RenderedDeviceSpec{RenderedVersion: tt.versionToRequeue})

			err := q.Add(item)
			require.NoError(err)
			_, ok := q.Next()
			require.True(ok, "first retrieval should succeed")

			err = q.Add(item)
			require.NoError(err)
			_, ok = q.Next()
			require.False(ok, "retrieval before threshold duration should return false")

			require.Eventually(func() bool {
				item, ok := q.Next()
				return ok && item.Spec.RenderedVersion == tt.versionToRequeue
			}, time.Second, time.Millisecond*10, "retrieval after threshold duration should succeed")
		})
	}
}

func TestEnforceMaxSize(t *testing.T) {
	require := require.New(t)
	testCases := []struct {
		name                    string
		maxSize                 int
		initialItems            map[int64]*Item
		initialHeap             []*Item
		expectedHeap            []*Item
		requeueStatus           map[int64]*requeueVersion
		expectedMaxSizeExceeded bool
		expectedItems           map[int64]*Item
	}{
		{
			name:    "Max size not exceeded",
			maxSize: 5,
			initialItems: map[int64]*Item{
				1: {Version: 1},
				2: {Version: 2},
				3: {Version: 3},
			},
			initialHeap: []*Item{
				{Version: 1},
				{Version: 2},
				{Version: 3},
			},
			expectedHeap: []*Item{
				{Version: 1},
				{Version: 2},
				{Version: 3},
			},
			requeueStatus:           map[int64]*requeueVersion{},
			expectedMaxSizeExceeded: false,
			expectedItems: map[int64]*Item{
				1: {Version: 1},
				2: {Version: 2},
				3: {Version: 3},
			},
		},
		{
			name:    "Max size exceeded, removed item not tried",
			maxSize: 3,
			initialItems: map[int64]*Item{
				1: {Version: 1},
				2: {Version: 2},
				3: {Version: 3},
			},
			initialHeap: []*Item{
				{Version: 1},
				{Version: 2},
				{Version: 3},
			},
			expectedHeap: []*Item{
				{Version: 1}, //   1
				{Version: 3}, // /   \
				{Version: 2}, // 2   3
			},
			requeueStatus: map[int64]*requeueVersion{
				1: {tries: 0},
			},
			expectedMaxSizeExceeded: true,
			expectedItems: map[int64]*Item{
				1: {Version: 1},
				2: {Version: 2},
				3: {Version: 3},
			},
		},
		{
			name:    "Max size exceeded, removed item tried",
			maxSize: 3,
			initialItems: map[int64]*Item{
				1: {Version: 1},
				2: {Version: 2},
				3: {Version: 3},
			},
			initialHeap: []*Item{
				{Version: 1},
				{Version: 2},
				{Version: 3},
			},
			expectedHeap: []*Item{
				// {Version: 1}, <- removed
				{Version: 2},
				{Version: 3},
			},
			requeueStatus: map[int64]*requeueVersion{
				1: {tries: 1},
			},
			expectedMaxSizeExceeded: false,
			expectedItems: map[int64]*Item{
				2: {Version: 2},
				3: {Version: 3},
			},
		},
		{
			name:    "Max size is zero",
			maxSize: 0,
			initialItems: map[int64]*Item{
				1: {Version: 1},
				2: {Version: 2},
			},
			initialHeap: []*Item{
				{Version: 1},
				{Version: 2},
			},
			expectedHeap: []*Item{
				{Version: 1},
				{Version: 2},
			},
			requeueStatus:           map[int64]*requeueVersion{},
			expectedMaxSizeExceeded: false,
			expectedItems: map[int64]*Item{
				1: {Version: 1},
				2: {Version: 2},
			},
		},
		{
			name:    "Heap is empty when max size exceeded",
			maxSize: 2,
			initialItems: map[int64]*Item{
				1: {Version: 1},
				2: {Version: 2},
			},
			initialHeap:             []*Item{}, // Heap is empty
			requeueStatus:           map[int64]*requeueVersion{},
			expectedMaxSizeExceeded: false,
			expectedItems: map[int64]*Item{
				1: {Version: 1},
				2: {Version: 2},
			},
		},
	}

	for _, tt := range testCases {
		t.Run(tt.name, func(t *testing.T) {
			log := log.NewPrefixLogger("test")
			log.SetLevel(logrus.DebugLevel)

			itemHeap := make(ItemHeap, len(tt.initialHeap))
			copy(itemHeap, tt.initialHeap)
			heap.Init(&itemHeap)

			// // Initialize Queue
			q := &queue{
				log:                           log,
				maxSize:                       tt.maxSize,
				items:                         tt.initialItems,
				heap:                          itemHeap,
				requeueStatus:                 tt.requeueStatus,
				requeueThreshold:              0,
				requeueThresholdDelayDuration: 0,
			}

			exceeded := q.enforceMaxSize()

			require.Equal(tt.expectedMaxSizeExceeded, exceeded, "exceeded mismatch")

			require.Equal(tt.expectedItems, q.items, "items mismatch")

			for i, item := range tt.expectedHeap {
				require.Equal(item, q.heap[i], "heap item mismatch")
			}
		})
	}
}
