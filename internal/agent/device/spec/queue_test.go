package spec

import (
	"context"
	"testing"
	"time"

	"github.com/flightctl/flightctl/pkg/log"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

func TestQueue(t *testing.T) {
	testCases := []struct {
		name        string
		maxSize     int
		items       []*Item
		expectOrder []string
	}{
		{
			name:    "ensure priory ordering",
			maxSize: 10,
			items: []*Item{
				{Version: 3, Spec: newVersionedDevice("3")},
				{Version: 1, Spec: newVersionedDevice("1")},
				{Version: 2, Spec: newVersionedDevice("2")},
			},
			expectOrder: []string{"1", "2", "3"},
		},
		{
			name:    "maxSize exceeded lowest version evicted",
			maxSize: 2,
			items: []*Item{
				{Version: 1, Spec: newVersionedDevice("1")},
				{Version: 2, Spec: newVersionedDevice("2")},
				{Version: 3, Spec: newVersionedDevice("3")},
			},
			expectOrder: []string{"2", "3"}, // 1 was evicted
		},
		{
			name:    "add items equal to maxSize",
			maxSize: 1,
			items: []*Item{
				{Version: 1, Spec: newVersionedDevice("1")},
			},
			expectOrder: []string{"1"}, // remove item after maxRetries
		},
		{
			name:    "maxSize unlimited",
			maxSize: 0,
			items: []*Item{
				{Version: 1, Spec: newVersionedDevice("1")},
				{Version: 2, Spec: newVersionedDevice("2")},
			},
			expectOrder: []string{"1", "2"},
		},
		{
			name:    "add same item twice",
			maxSize: 1,
			items: []*Item{
				{Version: 1, Spec: newVersionedDevice("1")},
				{Version: 1, Spec: newVersionedDevice("1")},
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
				require.Equal(expectedVersion, item.Spec.Version())
			}
		})
	}
}

func TestRequeueThreshold(t *testing.T) {
	require := require.New(t)
	const (
		delayThreshold  = 1
		delayDuration   = time.Millisecond * 200
		renderedVersion = "1"
	)
	ctx := context.Background()

	log := log.NewPrefixLogger("test")
	maxSize := 1
	maxRetries := 0
	q := &queueManager{
		queue:          newQueue(log, maxSize),
		failedVersions: make(map[int64]struct{}),
		requeueLookup:  make(map[int64]*requeueState),
		maxRetries:     maxRetries,
		delayThreshold: delayThreshold,
		delayDuration:  delayDuration,
		log:            log,
	}

	item := newVersionedDevice(renderedVersion)

	_, ok := q.Next(ctx)
	require.False(ok, "queue should be empty")

	// add item to queue
	q.Add(ctx, item)

	version, err := stringToInt64(item.Version())
	require.NoError(err)

	// ensure item is immediately available
	status := q.requeueLookup[version]
	require.NotNil(status)
	require.Equal(0, status.tries, "tries should be zero")
	require.True(status.nextAvailable.IsZero(), "nextAvailable should be zero")

	// add same item to queue before it is tried
	q.Add(ctx, item)

	// ensure item is immediately available
	status = q.requeueLookup[version]
	require.NotNil(status)
	require.Equal(0, status.tries, "tries should be zero")
	require.True(status.nextAvailable.IsZero(), "nextAvailable should be zero")

	// retrieve item
	_, ok = q.Next(ctx)
	require.True(ok, "first retrieval should succeed")

	// add same item to queue after it is tried should trigger requeue delay duration
	q.Add(ctx, item)
	_, ok = q.Next(ctx)
	require.False(ok, "retrieval before threshold duration should return false")

	require.Eventually(func() bool {
		item, ok := q.Next(ctx)
		return ok && item.Version() == renderedVersion
	}, time.Second, time.Millisecond*10, "retrieval after threshold duration should succeed")
}

func TestAddWithDelay(t *testing.T) {
	testCases := []struct {
		name          string
		delayDuration time.Duration
		expectDelay   bool
	}{
		{
			name:          "immediate availability",
			delayDuration: 0,
			expectDelay:   false,
		},
		{
			name:          "delayed availability",
			delayDuration: time.Millisecond * 100,
			expectDelay:   true,
		},
	}

	for _, tt := range testCases {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			ctx := context.Background()

			log := log.NewPrefixLogger("test")
			q := newPriorityQueue(1, 0, 0, 0, log)

			device := newVersionedDevice("1")
			nextAvailable := time.Now().Add(tt.delayDuration)

			// Add with delay
			q.AddWithDelay(ctx, device, nextAvailable)

			if tt.expectDelay {
				// Should not be available immediately
				_, ok := q.Next(ctx)
				require.False(ok, "device should not be available immediately when delayed")

				// Wait for delay to pass and try again
				time.Sleep(tt.delayDuration + time.Millisecond*10)
				retrieved, ok := q.Next(ctx)
				require.True(ok, "device should be available after delay")
				require.Equal("1", retrieved.Version())
			} else {
				// Should be available immediately
				retrieved, ok := q.Next(ctx)
				require.True(ok, "device should be available immediately")
				require.Equal("1", retrieved.Version())
			}
		})
	}
}

func TestAddWithDelayOrdering(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()

	log := log.NewPrefixLogger("test")
	q := newPriorityQueue(10, 0, 0, 0, log)

	now := time.Now()

	// Add devices with different delays
	q.AddWithDelay(ctx, newVersionedDevice("3"), now.Add(time.Millisecond*300))
	q.AddWithDelay(ctx, newVersionedDevice("1"), now.Add(time.Millisecond*100))
	q.AddWithDelay(ctx, newVersionedDevice("2"), now.Add(time.Millisecond*200))

	// None should be available immediately
	_, ok := q.Next(ctx)
	require.False(ok, "no devices should be available immediately")

	// Wait for all delays to pass
	time.Sleep(time.Millisecond * 350)

	// Should get devices in version order (priority queue ordering)
	device1, ok := q.Next(ctx)
	require.True(ok)
	require.Equal("1", device1.Version())

	device2, ok := q.Next(ctx)
	require.True(ok)
	require.Equal("2", device2.Version())

	device3, ok := q.Next(ctx)
	require.True(ok)
	require.Equal("3", device3.Version())
}
