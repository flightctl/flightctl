package spec

import (
	"context"
	"testing"

	"github.com/flightctl/flightctl/pkg/log"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestConsumeLatestSkipsFailedVersion(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockPriorityQueue := NewMockPriorityQueue(ctrl)
	mockWatcher := NewMockWatcher(ctrl)
	log := log.NewPrefixLogger("test")

	cache := newCache(log)
	// Set current to "3" and desired to "4"
	// getRenderedVersion() will check if desired (4) is failed
	// If not failed, it returns current version ("3")
	// Then consumeLatest compares lastConsumedDevice ("5") != "3", triggering requeue check
	cache.current.renderedVersion = "3"
	cache.desired.renderedVersion = "4"

	s := &manager{
		log:                log,
		queue:              mockPriorityQueue,
		cache:              cache,
		watcher:            mockWatcher,
		lastConsumedDevice: newVersionedDevice("5"),
	}

	ctx := context.Background()

	// Mock: no new messages from watcher
	mockWatcher.EXPECT().TryPop().Return(nil, false, nil)
	// Mock: getRenderedVersion() checks if desired (4) is failed - return false
	mockPriorityQueue.EXPECT().IsFailed(int64(4), gomock.Any()).Return(false)
	// Mock: consumeLatest checks if lastConsumed (5) is failed - return true to skip requeue
	mockPriorityQueue.EXPECT().IsFailed(int64(5), gomock.Any()).Return(true)
	// Expect NO call to Add() since version 5 is failed

	consumed, err := s.consumeLatest(ctx)
	require.NoError(err)
	require.False(consumed, "consumeLatest should return false for failed versions")
}

func TestConsumeLatestRequeuesNonFailedVersion(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockPriorityQueue := NewMockPriorityQueue(ctrl)
	mockWatcher := NewMockWatcher(ctrl)
	log := log.NewPrefixLogger("test")

	cache := newCache(log)
	// Set current to "3" and desired to "4"
	cache.current.renderedVersion = "3"
	cache.desired.renderedVersion = "4"

	lastConsumedDevice := newVersionedDevice("5")
	s := &manager{
		log:                log,
		queue:              mockPriorityQueue,
		cache:              cache,
		watcher:            mockWatcher,
		lastConsumedDevice: lastConsumedDevice,
	}

	ctx := context.Background()

	// Mock: no new messages from watcher
	mockWatcher.EXPECT().TryPop().Return(nil, false, nil)
	// Mock: getRenderedVersion() checks if desired (4) is failed - return false
	mockPriorityQueue.EXPECT().IsFailed(int64(4), gomock.Any()).Return(false)
	// Mock: consumeLatest checks if lastConsumed (5) is failed - return false to allow requeue
	mockPriorityQueue.EXPECT().IsFailed(int64(5), gomock.Any()).Return(false)
	mockPriorityQueue.EXPECT().Add(ctx, lastConsumedDevice)

	consumed, err := s.consumeLatest(ctx)
	require.NoError(err)
	require.True(consumed, "consumeLatest should return true when requeuing non-failed version")
}
