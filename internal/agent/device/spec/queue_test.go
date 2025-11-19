package spec

import (
	"context"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/agent/device/policy"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/poll"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestQueue(t *testing.T) {
	testCases := []struct {
		name            string
		maxSize         int
		items           []*Item
		expectOrder     []string
		expectedRequeue map[int64]int
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
		baseDelay        = time.Millisecond * 100
		backoffFactor    = 2.0
		maxDelayDuration = time.Millisecond * 200
		renderedVersion  = "1"
	)
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockPolicyManager := policy.NewMockManager(ctrl)
	mockPolicyManager.EXPECT().IsReady(gomock.Any(), policy.Download).Return(true).Times(1)
	mockPolicyManager.EXPECT().IsReady(gomock.Any(), policy.Update).Return(true).Times(1)

	log := log.NewPrefixLogger("test")
	maxSize := 1
	maxRetries := 0
	q := &queueManager{
		queue:          newQueue(log, maxSize),
		policyManager:  mockPolicyManager,
		specCache:      newCache(log),
		failedVersions: make(map[int64]struct{}),
		requeueLookup:  make(map[int64]*requeueState),
		maxRetries:     maxRetries,
		pollConfig: poll.Config{
			BaseDelay: baseDelay,
			Factor:    backoffFactor,
			MaxDelay:  maxDelayDuration,
		},
		log: log,
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

	// add same item to queue after it is tried should trigger incremental backoff
	q.Add(ctx, item)
	_, ok = q.Next(ctx)
	require.False(ok, "retrieval should be blocked by backoff")

	require.Eventually(func() bool {
		item, ok := q.Next(ctx)
		return ok && item.Version() == renderedVersion
	}, time.Second, time.Millisecond*10, "retrieval after backoff duration should succeed")
}

func TestRequeueRollback(t *testing.T) {
	require := require.New(t)
	const (
		baseDelay        = time.Millisecond * 100
		backoffFactor    = 2
		maxDelayDuration = time.Millisecond * 200
		renderedVersion1 = "1"
		renderedVersion2 = "2"
	)
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockPolicyManager := policy.NewMockManager(ctrl)
	mockPolicyManager.EXPECT().IsReady(gomock.Any(), policy.Download).Return(true).AnyTimes()
	mockPolicyManager.EXPECT().IsReady(gomock.Any(), policy.Update).Return(true).AnyTimes()

	log := log.NewPrefixLogger("test")
	log.SetLevel(logrus.DebugLevel)
	maxSize := 1
	maxRetries := 0
	q := &queueManager{
		queue:          newQueue(log, maxSize),
		specCache:      newCache(log),
		policyManager:  mockPolicyManager,
		failedVersions: make(map[int64]struct{}),
		requeueLookup:  make(map[int64]*requeueState),
		maxRetries:     maxRetries,
		pollConfig: poll.Config{
			BaseDelay: baseDelay,
			Factor:    backoffFactor,
			MaxDelay:  maxDelayDuration,
		},
		log: log,
	}

	// add version 1
	v1 := newVersionedDevice(renderedVersion1)
	_, ok := q.Next(ctx)
	require.False(ok, "queue should be empty")
	q.Add(ctx, v1)

	version, err := stringToInt64(v1.Version())
	require.NoError(err)

	// verify requeue state
	status := q.requeueLookup[version]
	require.NotNil(status)
	require.Equal(0, status.tries, "tries should be zero")
	require.True(status.nextAvailable.IsZero(), "nextAvailable should be zero")

	// retrieve version 1
	_, ok = q.Next(ctx)
	require.True(ok, "first retrieval should succeed")

	// add version 2
	v2 := newVersionedDevice("2")
	q.Add(ctx, v2)

	// retrieve version 2
	_, ok = q.Next(ctx)
	require.True(ok, "new version should be immediately available")

	// re-add version 1 (rollback) and version 2 (retryable)
	q.Add(ctx, v1)
	q.Add(ctx, v2)

	// version 2 should now be blocked by backoff
	_, ok = q.Next(ctx)
	require.False(ok, "retrieval should be blocked by backoff")

	require.Eventually(func() bool {
		item, ok := q.Next(ctx)
		return ok && item.Version() == renderedVersion2
	}, time.Second, time.Millisecond*350, "retrieval after backoff duration should succeed")
}

func TestPolicy(t *testing.T) {
	tests := []struct {
		name               string
		setupMocks         func(mockPolicyManager *policy.MockManager)
		wantNext           bool
		wantDesiredVersion string
	}{
		{
			name: "both policies ready on retry",
			setupMocks: func(mockPolicyManager *policy.MockManager) {
				// check policy during init Add
				mockPolicyManager.EXPECT().IsReady(gomock.Any(), policy.Download).Return(false)
				mockPolicyManager.EXPECT().IsReady(gomock.Any(), policy.Update).Return(false)
				// check policy during Add
				mockPolicyManager.EXPECT().IsReady(gomock.Any(), policy.Download).Return(false)
				mockPolicyManager.EXPECT().IsReady(gomock.Any(), policy.Update).Return(false)

				// evaluate policy first Next
				mockPolicyManager.EXPECT().IsReady(gomock.Any(), policy.Download).Return(false)
				mockPolicyManager.EXPECT().IsReady(gomock.Any(), policy.Update).Return(false)
				// evaluate policy ready on retry Next
				mockPolicyManager.EXPECT().IsReady(gomock.Any(), policy.Download).Return(true)
				mockPolicyManager.EXPECT().IsReady(gomock.Any(), policy.Update).Return(true)
			},
			wantNext:           true,
			wantDesiredVersion: "2",
		},
		{
			name: "download and update not ready on retry",
			setupMocks: func(mockPolicyManager *policy.MockManager) {
				// check policy during init Add
				mockPolicyManager.EXPECT().IsReady(gomock.Any(), policy.Download).Return(false)
				mockPolicyManager.EXPECT().IsReady(gomock.Any(), policy.Update).Return(false)
				// check policy during Add
				mockPolicyManager.EXPECT().IsReady(gomock.Any(), policy.Download).Return(false)
				mockPolicyManager.EXPECT().IsReady(gomock.Any(), policy.Update).Return(false)

				// evaluate policy first Next
				mockPolicyManager.EXPECT().IsReady(gomock.Any(), policy.Download).Return(false)
				mockPolicyManager.EXPECT().IsReady(gomock.Any(), policy.Update).Return(false)
				// evaluate policy ready on retry Next
				mockPolicyManager.EXPECT().IsReady(gomock.Any(), policy.Download).Return(false)
				mockPolicyManager.EXPECT().IsReady(gomock.Any(), policy.Update).Return(false)
			},
			wantNext: false,
		},
		{
			name: "download ready update not on retry",
			setupMocks: func(mockPolicyManager *policy.MockManager) {
				// check policy during init Add
				mockPolicyManager.EXPECT().IsReady(gomock.Any(), policy.Download).Return(false)
				mockPolicyManager.EXPECT().IsReady(gomock.Any(), policy.Update).Return(false)
				// check policy during Add
				mockPolicyManager.EXPECT().IsReady(gomock.Any(), policy.Download).Return(false)
				mockPolicyManager.EXPECT().IsReady(gomock.Any(), policy.Update).Return(false)

				// evaluate policy first Next
				mockPolicyManager.EXPECT().IsReady(gomock.Any(), policy.Download).Return(false)
				mockPolicyManager.EXPECT().IsReady(gomock.Any(), policy.Update).Return(false)
				// evaluate policy ready on retry Next
				mockPolicyManager.EXPECT().IsReady(gomock.Any(), policy.Download).Return(true)
				mockPolicyManager.EXPECT().IsReady(gomock.Any(), policy.Update).Return(false)
			},
			wantNext:           true,
			wantDesiredVersion: "6",
		},
		{
			name: "download not ready update ready on retry",
			setupMocks: func(mockPolicyManager *policy.MockManager) {
				// check policy during init Add
				mockPolicyManager.EXPECT().IsReady(gomock.Any(), policy.Download).Return(false)
				mockPolicyManager.EXPECT().IsReady(gomock.Any(), policy.Update).Return(false)
				// check policy during Add
				mockPolicyManager.EXPECT().IsReady(gomock.Any(), policy.Download).Return(false)
				mockPolicyManager.EXPECT().IsReady(gomock.Any(), policy.Update).Return(false)

				// evaluate policy first Next
				mockPolicyManager.EXPECT().IsReady(gomock.Any(), policy.Download).Return(false)
				mockPolicyManager.EXPECT().IsReady(gomock.Any(), policy.Update).Return(false)
				// evaluate policy ready on retry Next
				mockPolicyManager.EXPECT().IsReady(gomock.Any(), policy.Download).Return(false)
				mockPolicyManager.EXPECT().IsReady(gomock.Any(), policy.Update).Return(true)
			},
			wantNext:           true,
			wantDesiredVersion: "6",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			ctx := context.Background()
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockPolicyManager := policy.NewMockManager(ctrl)

			log := log.NewPrefixLogger("test")
			log.SetLevel(logrus.TraceLevel)
			maxSize := 1
			maxRetries := 0
			q := &queueManager{
				queue:          newQueue(log, maxSize),
				specCache:      newCache(log),
				policyManager:  mockPolicyManager,
				failedVersions: make(map[int64]struct{}),
				requeueLookup:  make(map[int64]*requeueState),
				maxRetries:     maxRetries,
				pollConfig: poll.Config{
					BaseDelay: 1 * time.Second,
					Factor:    2.0,
					MaxDelay:  5 * time.Second,
				},
				log: log,
			}

			tt.setupMocks(mockPolicyManager)

			// init to exercise eviction
			q.Add(ctx, newVersionedDevice("0"))

			// tested Add
			q.Add(ctx, newVersionedDevice(tt.wantDesiredVersion))

			// first call output is not validated here instead via mock.EXPECT()
			_, _ = q.Next(ctx)

			result, ok := q.Next(ctx)
			if tt.wantNext {
				require.Equal(tt.wantDesiredVersion, result.Version())
			} else {
				require.False(ok)
			}
		})
	}
}

func TestCalculateBackoffDelay(t *testing.T) {
	require := require.New(t)

	q := &queueManager{
		pollConfig: poll.Config{
			BaseDelay: 100 * time.Millisecond,
			Factor:    2.0,
			MaxDelay:  500 * time.Millisecond,
		},
	}

	// progressive delays
	require.Equal(100*time.Millisecond, q.calculateBackoffDelay(1), "First try should use base delay")
	require.Equal(200*time.Millisecond, q.calculateBackoffDelay(2), "Second try should double")
	require.Equal(400*time.Millisecond, q.calculateBackoffDelay(3), "Third try should double again")
	require.Equal(500*time.Millisecond, q.calculateBackoffDelay(4), "Fourth try should cap at max delay")
	require.Equal(500*time.Millisecond, q.calculateBackoffDelay(5), "Fifth try should remain at max delay")
}

func TestRejectOldVersions(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockPolicyManager := policy.NewMockManager(ctrl)
	mockPolicyManager.EXPECT().IsReady(gomock.Any(), gomock.Any()).Return(true).AnyTimes()

	log := log.NewPrefixLogger("test")
	log.SetLevel(logrus.DebugLevel)

	cache := newCache(log)
	cache.current.renderedVersion = "5"

	q := &queueManager{
		queue:          newQueue(log, 10),
		policyManager:  mockPolicyManager,
		specCache:      cache,
		failedVersions: make(map[int64]struct{}),
		requeueLookup:  make(map[int64]*requeueState),
		maxRetries:     0,
		pollConfig: poll.Config{
			BaseDelay: time.Second,
			Factor:    2.0,
			MaxDelay:  5 * time.Second,
		},
		log: log,
	}

	// add old version 3
	oldDevice := newVersionedDevice("3")
	q.Add(ctx, oldDevice)

	// verify old version is not in queue
	_, exists := q.Next(ctx)
	require.False(exists, "old version should not be added to queue")

	// add newer version 5
	currentDevice := newVersionedDevice("5")
	q.Add(ctx, currentDevice)

	// verify current version is in queue
	device, exists := q.Next(ctx)
	require.True(exists, "current version should be in queue")
	require.Equal("5", device.Version())

	// add newer version 7
	newerDevice := newVersionedDevice("7")
	q.Add(ctx, newerDevice)

	// verify newer version is in queue
	device, exists = q.Next(ctx)
	require.True(exists, "newer version should be in queue")
	require.Equal("7", device.Version())
}
