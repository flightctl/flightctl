package spec

import (
	"context"
	"time"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/client"
)

type Type string

const (
	Current  Type = "current"
	Desired  Type = "desired"
	Rollback Type = "rollback"

	// defaultMaxRetries is the default number of retries for a spec item set to 0 for infinite retries.
	defaultSpecRequeueMaxRetries = 0
	defaultSpecQueueSize         = 1
	defaultSpecRequeueThreshold  = 1
	// defaultSpecRequeueDelay is the default delay between requeue attempts.
	defaultSpecRequeueDelay = 5 * time.Minute
)

type Manager interface {
	// Initialize initializes the current, desired and rollback spec files on
	// disk. If the files already exist, they are overwritten.
	Initialize() error
	// Ensure ensures that spec files exist on disk and re initializes them if they do not.
	Ensure() error
	// Read returns the rendered device spec of the specified type from disk.
	Read(specType Type) (*v1alpha1.RenderedDeviceSpec, error)
	// Upgrade updates the current rendered spec to the desired rendered spec
	// and resets the rollback spec.
	Upgrade() error
	// SetUpgradeFailed marks the desired rendered spec as failed.
	SetUpgradeFailed()
	// IsUpdating returns true if the device is in the process of reconciling the desired spec.
	IsUpgrading() bool
	// IsOSUpdate returns true if an OS update is in progress by checking the current rendered spec.
	IsOSUpdate() (bool, error)
	// CheckOsReconciliation checks if the booted OS image matches the desired OS image.
	CheckOsReconciliation(ctx context.Context) (string, bool, error)
	// IsRollingBack returns true if the device is in a rollback state.
	IsRollingBack(ctx context.Context) (bool, error)
	// PrepareRollback creates a rollback version of the current rendered spec.
	PrepareRollback(ctx context.Context) error
	// Rollback reverts the device to the state of the rollback rendered spec.
	Rollback() error
	// SetClient sets the management API client.
	SetClient(client.Management)
	// GetDesired returns the desired rendered device spec from the management API.
	GetDesired(ctx context.Context) (*v1alpha1.RenderedDeviceSpec, bool, error)
}

type PriorityQueue interface {
	// Add adds an item to the queue. If the item is already in the queue, it will be skipped.
	Add(item *Item) error
	// Remove removes an item from the queue.
	Remove(version string)
	// Next returns the next item to process.
	Next() (*Item, bool)
	// Size returns the size of the queue.
	Size() int
	// Clear removes all items from the queue.
	Clear()
	// IsEmpty returns true if the queue is empty.
	IsEmpty() bool
	// SetVersionFailed marks a template version as failed. Failed versions will not be requeued.
	SetVersionFailed(version string)
	// IsVersionFailed returns true if a template version is marked as failed.
	IsVersionFailed(version string) bool
}
