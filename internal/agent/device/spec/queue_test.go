package spec

import (
	"testing"

	v1alpha1 "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

func TestQueue(t *testing.T) {
	testCases := []struct {
		name            string
		maxSize         int
		items           []*Item
		expectOrder     []string
		expectedRequeue map[int64]int
		expectNoItems   bool
	}{
		{
			name:    "ensure priory ordering",
			maxSize: 10,
			items: []*Item{
				{Version: 3, Spec: &v1alpha1.RenderedDeviceSpec{RenderedVersion: "3"}},
				{Version: 1, Spec: &v1alpha1.RenderedDeviceSpec{RenderedVersion: "1"}},
				{Version: 2, Spec: &v1alpha1.RenderedDeviceSpec{RenderedVersion: "2"}},
			},
			expectOrder: []string{"1", "2", "3"},
		},
		{
			name:    "maxSize exceeded lowest version evicted",
			maxSize: 2,
			items: []*Item{
				{Version: 1, Spec: &v1alpha1.RenderedDeviceSpec{RenderedVersion: "1"}},
				{Version: 2, Spec: &v1alpha1.RenderedDeviceSpec{RenderedVersion: "2"}},
				{Version: 3, Spec: &v1alpha1.RenderedDeviceSpec{RenderedVersion: "3"}},
			},
			expectOrder: []string{"2", "3"}, // 1 was evicted
		},
		{
			name:    "add items equal to maxSize",
			maxSize: 1,
			items: []*Item{
				{Version: 1, Spec: &v1alpha1.RenderedDeviceSpec{RenderedVersion: "1"}},
			},
			expectOrder: []string{"1"}, // remove item after maxRetries
		},
		{
			name:    "maxSize unlimited",
			maxSize: 0,
			items: []*Item{
				{Version: 1, Spec: &v1alpha1.RenderedDeviceSpec{RenderedVersion: "1"}},
				{Version: 2, Spec: &v1alpha1.RenderedDeviceSpec{RenderedVersion: "2"}},
			},
			expectOrder: []string{"1", "2"},
		},
		{
			name:    "add same item twice",
			maxSize: 1,
			items: []*Item{
				{Version: 1, Spec: &v1alpha1.RenderedDeviceSpec{RenderedVersion: "1"}},
				{Version: 1, Spec: &v1alpha1.RenderedDeviceSpec{RenderedVersion: "1"}},
			},
			expectOrder: []string{"1"},
		},
	}

	for _, tt := range testCases {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			log := log.NewPrefixLogger("test")
			log.SetLevel(logrus.DebugLevel)
			q := newQueue(log, tt.maxSize)

			// add to queue
			for _, item := range tt.items {
				q.Add(item)
			}

			// ensure priority ordering
			for _, expectedVersion := range tt.expectOrder {
				item, ok := q.Pop()
				require.True(ok)
				require.Equal(expectedVersion, item.Spec.RenderedVersion)
			}
		})
	}
}
