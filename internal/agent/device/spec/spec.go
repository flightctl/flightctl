package spec

import (
	"context"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/policy"
	"github.com/flightctl/flightctl/internal/agent/device/status"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/poll"
	"github.com/samber/lo"
)

type Type string

const (
	Current  Type = "current"
	Desired  Type = "desired"
	Rollback Type = "rollback"

	// defaultMaxRetries is the default number of retries for a spec item set to 0 for infinite retries.
	defaultSpecRequeueMaxRetries = 0
	// defaultSpecQueueMaxSize is the default maximum number of items in the queue.
	defaultSpecQueueMaxSize = 1
)

// defaultSpecPollConfig is the default poll configuration for the spec priority queue.
var defaultSpecPollConfig = func() poll.Config {
	return poll.Config{
		BaseDelay:    30 * time.Second,
		Factor:       1.5,
		MaxDelay:     5 * time.Minute,
		JitterFactor: 0.1,
	}
}()

// Watcher provides a way to watch for device spec updates.
type Watcher interface {
	// Pop blocks until a device is available or returns error if closed
	Pop() (*v1beta1.Device, error)
	// TryPop attempts to get a device without blocking
	TryPop() (*v1beta1.Device, bool, error)
}

// Manager provides the public API for managing device specifications.
// This interface is used by the device agent for normal operations.
type Manager interface {
	// Initialize initializes the current, desired and rollback device files on
	// disk. If the files already exist, they are overwritten.
	Initialize(ctx context.Context) error
	// Ensure ensures that spec files exist on disk and re initializes them if they do not.
	Ensure() error
	// RenderedVersion returns the rendered version of the specified spec type.
	RenderedVersion(specType Type) string
	// OSVersion returns the OS version of the specified spec type.
	OSVersion(specType Type) string
	// Read returns the rendered device of the specified type from disk.
	Read(specType Type) (*v1beta1.Device, error)
	// Upgrade updates the current rendered spec to the desired rendered spec
	// and resets the rollback spec.
	Upgrade(ctx context.Context) error
	// SetUpgradeFailed marks the desired rendered spec as failed.
	// The specHash parameter allows tracking failed specs by content hash,
	// enabling detection of console-only changes on failed specs.
	SetUpgradeFailed(version string, specHash string) error
	// IsUpdating returns true if the device is in the process of reconciling the desired spec.
	IsUpgrading() bool
	// IsOSUpdate returns true if an OS update is in progress by checking the current rendered spec.
	IsOSUpdate() bool
	// IsOSUpdatePending returns true if an OS update is specified but the device
	// has not yet booted into the new image.
	IsOSUpdatePending(ctx context.Context) (bool, error)
	// CheckOsReconciliation checks if the booted OS image matches the desired OS image.
	CheckOsReconciliation(ctx context.Context) (string, bool, error)
	// IsRollingBack returns true if the device is in a rollback state.
	IsRollingBack(ctx context.Context) (bool, error)
	// CreateRollback creates a rollback version of the current rendered spec.
	CreateRollback(ctx context.Context) error
	// ClearRollback clears the rollback rendered spec.
	ClearRollback() error
	// Rollback reverts the device to the state of the rollback rendered spec.
	Rollback(ctx context.Context, opts ...RollbackOption) error
	// GetDesired returns the desired rendered device from the management API.
	GetDesired(ctx context.Context) (*v1beta1.Device, bool, error)
	// CheckPolicy validates the update policy is ready to process.
	CheckPolicy(ctx context.Context, policyType policy.Type, version string) error
	// SetClient sets the management client for fetching specs.
	SetClient(client client.Management)
	status.Exporter
}

type PriorityQueue interface {
	// Add adds a new spec to the scheduler
	Add(ctx context.Context, spec *v1beta1.Device)
	// Next returns the next spec to process
	Next(ctx context.Context) (*v1beta1.Device, bool)
	// Remove removes a spec from the scheduler
	Remove(version int64)
	// SetFailed marks a rendered spec version and/or spec hash as failed.
	SetFailed(version int64, specHash string)
	// IsFailed returns true if the version or spec hash is marked as failed.
	IsFailed(version int64, specHash string) bool
	// CheckPolicy validates the update policy is ready to process.
	CheckPolicy(ctx context.Context, policyType policy.Type, version string) error
}

type cacheData struct {
	renderedVersion string
	osVersion       string
	specHash        string
}

// cache stores the current, desired and rollback rendered cacheData in memory.
// this eliminates the need to read the spec files from disk on every operation.
type cache struct {
	current  *cacheData
	desired  *cacheData
	rollback *cacheData
	log      *log.PrefixLogger
}

// newCache creates a new cache instance.
func newCache(log *log.PrefixLogger) *cache {
	return &cache{
		current:  &cacheData{},
		desired:  &cacheData{},
		rollback: &cacheData{},
		log:      log,
	}
}

// update updates the rendered version and OS version of the specified spec type.
func (c *cache) update(specType Type, device *v1beta1.Device) {
	if device.Spec == nil {
		c.log.Errorf("Failed to update cache device spec is nil")
		return
	}
	var osImage string
	if device.Spec.Os != nil {
		osImage = device.Spec.Os.Image
	}

	renderedVersion := device.Version()
	specHash := device.SpecHash()
	switch specType {
	case Current:
		c.current.renderedVersion = renderedVersion
		c.current.osVersion = osImage
		c.current.specHash = specHash
	case Desired:
		c.desired.renderedVersion = renderedVersion
		c.desired.osVersion = osImage
		c.desired.specHash = specHash
	case Rollback:
		c.rollback.renderedVersion = renderedVersion
		c.rollback.osVersion = osImage
		c.rollback.specHash = specHash
	}
}

// getRenderedVersion returns the rendered version of the specified spec type.
func (c *cache) getRenderedVersion(specType Type) string {
	switch specType {
	case Current:
		return c.current.renderedVersion
	case Desired:
		return c.desired.renderedVersion
	case Rollback:
		return c.rollback.renderedVersion
	default:
		c.log.Errorf("Invalid spec type: %s", specType)
		return ""
	}
}

// getOSVersion returns the OS version of the specified spec type.
func (c *cache) getOSVersion(specType Type) string {
	switch specType {
	case Current:
		return c.current.osVersion
	case Desired:
		return c.desired.osVersion
	case Rollback:
		return c.rollback.osVersion
	default:
		c.log.Errorf("Invalid spec type: %s", specType)
		return ""
	}
}

func (c *cache) getSpecHash(specType Type) string {
	switch specType {
	case Current:
		return c.current.specHash
	case Desired:
		return c.desired.specHash
	case Rollback:
		return c.rollback.specHash
	default:
		c.log.Errorf("Invalid spec type: %s", specType)
		return ""
	}
}

func newVersionedDevice(version string) *v1beta1.Device {
	return newVersionedDeviceWithHash(version, "")
}

func newVersionedDeviceWithHash(version, specHash string) *v1beta1.Device {
	annotations := map[string]string{
		v1beta1.DeviceAnnotationRenderedVersion: version,
	}
	if specHash != "" {
		annotations[v1beta1.DeviceAnnotationRenderedSpecHash] = specHash
	}
	device := &v1beta1.Device{
		Metadata: v1beta1.ObjectMeta{
			Annotations: lo.ToPtr(annotations),
		},
	}
	device.Spec = &v1beta1.DeviceSpec{}
	return device
}
