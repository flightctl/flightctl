package spec

import (
	"strconv"
	"testing"

	v1alpha1 "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/stretchr/testify/require"
)

func TestQueue(t *testing.T) {
	testCases := []struct {
		name           string
		maxRetries     int
		maxSize        int
		items          []*Item
		requeues       []int64
		expectOrder    []string
		expectQueueLen int
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
			requeues:       []int64{1, 1, 1},
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
			requeues:       []int64{1, 1},
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
			requeues:       []int64{1, 1},
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
			requeues:       []int64{1, 2},
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
			requeues:       []int64{1, 1, 1, 2, 2, 2},
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
			requeues:       []int64{1, 1},
			expectOrder:    []string{"1"},
			expectQueueLen: 0,
		},
	}

	for _, tt := range testCases {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			log := log.NewPrefixLogger("test")
			q := NewQueue(log, tt.maxRetries, tt.maxSize)

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
				require.Equal(expectedVersion, item.Spec().RenderedVersion)
			}

			// cleanup
			for _, expectedVersion := range tt.expectOrder {
				q.Forget(stringToInt64(expectedVersion))
			}

			require.Equal(tt.expectQueueLen, q.Len())
		})
	}
}

func stringToInt64(s string) int64 {
	if s == "" {
		return 0
	}
	i, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0
	}
	if i < 0 {
		return 0
	}
	return i
}
