package spec

import (
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
		requeues        []string
		expectOrder     []string
		expectQueueLen  int
		expectedRequeue map[int64]int
	}{
		{
			name:       "ensure priory ordering",
			maxRetries: 3,
			maxSize:    10,
			items: []*Item{
				{version: 3, spec: &v1alpha1.RenderedDeviceSpec{RenderedVersion: "3"}},
				{version: 1, spec: &v1alpha1.RenderedDeviceSpec{RenderedVersion: "1"}},
				{version: 2, spec: &v1alpha1.RenderedDeviceSpec{RenderedVersion: "2"}},
			},
			expectOrder:    []string{"1", "2", "3"},
			expectQueueLen: 0,
		},
		{
			name:       "maxSize exceeded removes lowest version",
			maxRetries: 3,
			maxSize:    2,
			items: []*Item{
				{version: 1, spec: &v1alpha1.RenderedDeviceSpec{RenderedVersion: "1"}},
				{version: 2, spec: &v1alpha1.RenderedDeviceSpec{RenderedVersion: "2"}},
				{version: 3, spec: &v1alpha1.RenderedDeviceSpec{RenderedVersion: "3"}},
			},
			expectOrder:    []string{"2", "3"},
			expectQueueLen: 0,
		},
		{
			name:       "requeue with maxRetries exceeded",
			maxRetries: 2,
			maxSize:    10,
			items: []*Item{
				{version: 1, spec: &v1alpha1.RenderedDeviceSpec{RenderedVersion: "1"}},
			},
			requeues:       []string{"1", "1", "1"},
			expectOrder:    []string{}, // remove item after maxRetries
			expectQueueLen: 0,
		},
		{
			name:       "requeue within maxRetries",
			maxRetries: 3,
			maxSize:    10,
			items: []*Item{
				{version: 1, spec: &v1alpha1.RenderedDeviceSpec{RenderedVersion: "1"}},
			},
			requeues:       []string{"1", "1"},
			expectOrder:    []string{"1"},
			expectQueueLen: 0,
		},
		{
			name:       "adding new item after requeue",
			maxRetries: 3,
			maxSize:    10,
			items: []*Item{
				{version: 1, spec: &v1alpha1.RenderedDeviceSpec{RenderedVersion: "1"}},
			},
			requeues:       []string{"1", "1"},
			expectOrder:    []string{"1"},
			expectQueueLen: 0,
		},
		{
			name:       "requeue different versions",
			maxRetries: 3,
			maxSize:    10,
			items: []*Item{
				{version: 1, spec: &v1alpha1.RenderedDeviceSpec{RenderedVersion: "1"}},
				{version: 2, spec: &v1alpha1.RenderedDeviceSpec{RenderedVersion: "2"}},
			},
			requeues:       []string{"1", "2"},
			expectOrder:    []string{"1", "2"},
			expectQueueLen: 0,
		},
		{
			name:       "maxSize and maxRetries both exceeded",
			maxRetries: 2,
			maxSize:    2,
			items: []*Item{
				{version: 1, spec: &v1alpha1.RenderedDeviceSpec{RenderedVersion: "1"}},
				{version: 2, spec: &v1alpha1.RenderedDeviceSpec{RenderedVersion: "2"}},
				{version: 3, spec: &v1alpha1.RenderedDeviceSpec{RenderedVersion: "3"}},
			},
			requeues:       []string{"1", "1", "1", "2", "2", "2"},
			expectOrder:    []string{"3"},
			expectQueueLen: 0,
		},
		{
			name:       "requeue without maxRetries hit",
			maxRetries: 5,
			maxSize:    10,
			items: []*Item{
				{version: 1, spec: &v1alpha1.RenderedDeviceSpec{RenderedVersion: "1"}},
			},
			requeues:       []string{"1", "1"},
			expectOrder:    []string{"1"},
			expectQueueLen: 0,
		},
	}

	for _, tt := range testCases {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			log := log.NewPrefixLogger("test")
			q := newQueue(log, tt.maxRetries, tt.maxSize, defaultSpecRequeueThreshold, defaultSpecRequeueDelay)

			// add to queue
			for _, item := range tt.items {
				q.Add(item)
			}

			// simulate requeues
			for _, version := range tt.requeues {
				q.Requeue(version)
			}

			// ensure priority ordering
			for _, expectedVersion := range tt.expectOrder {
				item, ok := q.Get()
				require.True(ok)
				require.Equal(expectedVersion, item.RenderedVersion)
			}

			// cleanup
			for _, expectedVersion := range tt.expectOrder {
				q.forget(expectedVersion)
			}

			require.Equal(tt.expectQueueLen, q.Len())
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

			q.Add(item)
			_, ok := q.Get()
			require.True(ok, "first retrieval should succeed")

			q.Add(item)
			_, ok = q.Get()
			require.False(ok, "retrieval before threshold duration should return false")

			require.Eventually(func() bool {
				item, ok := q.Get()
				return ok && item.RenderedVersion == tt.versionToRequeue
			}, time.Second, time.Millisecond*10, "retrieval after threshold duration should succeed")
		})
	}
}
