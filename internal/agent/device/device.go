package device

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/applications"
	"github.com/flightctl/flightctl/internal/agent/device/config"
	"github.com/flightctl/flightctl/internal/agent/device/console"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/agent/device/hook"
	"github.com/flightctl/flightctl/internal/agent/device/resource"
	"github.com/flightctl/flightctl/internal/agent/device/spec"
	"github.com/flightctl/flightctl/internal/agent/device/status"
	"github.com/flightctl/flightctl/internal/container"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/lthibault/jitterbug"
	"k8s.io/apimachinery/pkg/util/wait"
)

// TODO: expose via config
const (
	// defaultMaxRetries is the default number of retries for a spec item set to 0 for infinite retries.
	defaultSpecRequeueMaxRetries = 0
	defaultSpecQueueSize         = 1
)

// Agent is responsible for managing the applications, configuration and status of the device.
type Agent struct {
	name                   string
	deviceWriter           fileio.Writer
	statusManager          status.Manager
	specManager            spec.Manager
	hookManager            hook.Manager
	appManager             applications.Manager
	applicationsController *applications.Controller
	configController       *config.Controller
	osImageController      *OSImageController
	resourceController     *resource.Controller
	consoleController      *console.ConsoleController
	bootcClient            container.BootcClient
	podmanClient           *client.Podman

	fetchSpecInterval   util.Duration
	fetchStatusInterval util.Duration

	queue                  *spec.Queue
	currentRenderedVersion string

	backoff wait.Backoff
	log     *log.PrefixLogger
}

// NewAgent creates a new device agent.
func NewAgent(
	name string,
	deviceWriter fileio.Writer,
	statusManager status.Manager,
	specManager spec.Manager,
	appManager applications.Manager,
	fetchSpecInterval util.Duration,
	fetchStatusInterval util.Duration,
	hookManager hook.Manager,
	applicationsController *applications.Controller,
	configController *config.Controller,
	osImageController *OSImageController,
	resourceController *resource.Controller,
	consoleController *console.ConsoleController,
	bootcClient container.BootcClient,
	podmanClient *client.Podman,
	backoff wait.Backoff,
	log *log.PrefixLogger,
) *Agent {
	queue := spec.NewQueue(log, defaultSpecRequeueMaxRetries, defaultSpecQueueSize)
	return &Agent{
		name:                   name,
		deviceWriter:           deviceWriter,
		statusManager:          statusManager,
		specManager:            specManager,
		hookManager:            hookManager,
		appManager:             appManager,
		fetchSpecInterval:      fetchSpecInterval,
		fetchStatusInterval:    fetchStatusInterval,
		applicationsController: applicationsController,
		configController:       configController,
		osImageController:      osImageController,
		resourceController:     resourceController,
		consoleController:      consoleController,
		bootcClient:            bootcClient,
		podmanClient:           podmanClient,
		backoff:                backoff,
		queue:                  queue,
		log:                    log,
	}
}

func (a *Agent) initialize() error {
	desired, err := a.specManager.Read(spec.Desired)
	if err != nil {
		a.log.Errorf("Failed to read desired spec: %v", err)
		return err
	}

	a.queue.Add(spec.NewItem(desired, stringToInt64(desired.RenderedVersion)))
	return nil
}

// Run starts the device agent reconciliation loop.
func (a *Agent) Run(ctx context.Context) error {
	fetchSpecTicker := jitterbug.New(time.Duration(a.fetchSpecInterval), &jitterbug.Norm{Stdev: 30 * time.Millisecond, Mean: 0})
	defer fetchSpecTicker.Stop()
	fetchStatusTicker := jitterbug.New(time.Duration(a.fetchStatusInterval), &jitterbug.Norm{Stdev: 30 * time.Millisecond, Mean: 0})
	defer fetchStatusTicker.Stop()

	// initialize the agent
	if err := a.initialize(); err != nil {
		return err
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-fetchSpecTicker.C:
			a.fetchDeviceSpec(ctx, a.fetchDeviceSpecFn)
		case <-fetchStatusTicker.C:
			a.fetchDeviceStatus(ctx)
		}
	}
}

func (a *Agent) sync(ctx context.Context, current, desired *v1alpha1.RenderedDeviceSpec) error {
	if err := a.beforeUpdate(ctx, current, desired); err != nil {
		return fmt.Errorf("before update: %w", err)
	}

	if err := a.syncDevice(ctx, current, desired); err != nil {
		// TODO: enable rollback on failure
		return fmt.Errorf("sync device: %w", err)
	}

	if err := a.afterUpdate(ctx); err != nil {
		return fmt.Errorf("after update: %w: %w", errors.ErrNoRetry, err)
	}

	return nil
}

func (a *Agent) fetchDeviceSpec(ctx context.Context, fn func(ctx context.Context, desired *v1alpha1.RenderedDeviceSpec) error) {
	startTime := time.Now()
	a.log.Debug("Starting fetch of device spec")
	defer func() {
		duration := time.Since(startTime)
		a.log.Debugf("Completed fetch device spec in %v", duration)
	}()

	if err := a.enqueue(ctx); err != nil {
		a.log.Errorf("Failed to enqueue job: %v", err)
		return
	}

	item, ok := a.queue.Get()
	if !ok {
		a.log.Debug("No spec to process, retrying later")
		return
	}

	desiredSpec := item.Spec()
	if err := fn(ctx, desiredSpec); err != nil {
		a.handleSyncError(ctx, desiredSpec, err)
		return
	}

	_, updateErr := a.statusManager.Update(ctx, status.SetDeviceSummary(v1alpha1.DeviceSummaryStatus{
		Status: v1alpha1.DeviceSummaryStatusOnline,
		Info:   nil,
	}))
	if updateErr != nil {
		a.log.Errorf("Updating device status: %v", updateErr)
	}

	a.queue.Forget(item.Version())
}

func (a *Agent) fetchDeviceSpecFn(ctx context.Context, desired *v1alpha1.RenderedDeviceSpec) error {
	current, err := a.specManager.Read(spec.Current)
	if err != nil {
		return err
	}

	if current.RenderedVersion == "" && desired.RenderedVersion == "" {
		return nil
	}

	// reset current rendered version
	if a.currentRenderedVersion == "" {
		a.currentRenderedVersion = current.RenderedVersion
	}

	isUpdating := spec.IsUpdating(current, desired)
	if isUpdating {
		updateErr := a.statusManager.UpdateCondition(ctx, v1alpha1.Condition{
			Type:    v1alpha1.DeviceUpdating,
			Status:  v1alpha1.ConditionStatusTrue,
			Reason:  "Update",
			Message: fmt.Sprintf("The device is updating to renderedVersion: %s", desired.RenderedVersion),
		})
		if updateErr != nil {
			a.log.Warnf("Failed setting status: %v", updateErr)
		}
	}

	if err := a.sync(ctx, current, desired); err != nil {
		return err
	}

	if isUpdating {
		if err := a.specManager.Upgrade(); err != nil {
			return err
		}
		a.log.Infof("Synced device to renderedVersion: %s", desired.RenderedVersion)
	}

	// track the new current
	if a.currentRenderedVersion != desired.RenderedVersion {
		a.currentRenderedVersion = desired.RenderedVersion
	}

	return a.upgradeStatus(ctx, desired)
}

func (a *Agent) upgradeStatus(ctx context.Context, desired *v1alpha1.RenderedDeviceSpec) error {
	updateErr := a.statusManager.UpdateCondition(ctx, v1alpha1.Condition{
		Type:    v1alpha1.DeviceUpdating,
		Status:  v1alpha1.ConditionStatusFalse,
		Reason:  "Updated",
		Message: fmt.Sprintf("Updated to desired renderedVersion: %s", desired.RenderedVersion),
	})
	if updateErr != nil {
		a.log.Warnf("Failed setting status: %v", updateErr)
	}

	updateFns := []status.UpdateStatusFn{
		status.SetConfig(v1alpha1.DeviceConfigStatus{
			RenderedVersion: desired.RenderedVersion,
		}),
	}

	if desired.Os != nil {
		bootcStatus, err := a.bootcClient.Status(ctx)
		if err != nil {
			return err
		}

		updateFns = append(updateFns, status.SetOSImage(v1alpha1.DeviceOSStatus{
			Image:       desired.Os.Image,
			ImageDigest: bootcStatus.GetBootedImageDigest(),
		}))
	}

	_, updateErr = a.statusManager.Update(ctx, updateFns...)
	if updateErr != nil {
		a.log.Warnf("Failed setting status: %v", updateErr)
	}

	return nil
}

func (a *Agent) fetchDeviceStatus(ctx context.Context) {
	startTime := time.Now()
	a.log.Debug("Started collecting device status")
	defer func() {
		duration := time.Since(startTime)
		a.log.Debugf("Completed pushing device status to service in: %v", duration)
	}()

	if err := a.statusManager.Sync(ctx); err != nil {
		msg := err.Error()
		_, updateErr := a.statusManager.Update(ctx, status.SetDeviceSummary(v1alpha1.DeviceSummaryStatus{
			Status: v1alpha1.DeviceSummaryStatusDegraded,
			Info:   &msg,
		}))
		if updateErr != nil {
			a.log.Errorf("Updating device status: %v", updateErr)
		}
		a.log.Errorf("Syncing status: %v", err)
	}
}

func (a *Agent) beforeUpdate(ctx context.Context, current, desired *v1alpha1.RenderedDeviceSpec) error {
	if err := a.beforeUpdateApplications(ctx, current, desired); err != nil {
		return fmt.Errorf("applications: %w", err)
	}

	return nil
}

func (a *Agent) beforeUpdateApplications(ctx context.Context, _, desired *v1alpha1.RenderedDeviceSpec) error {
	if desired.Applications == nil {
		a.log.Debug("No applications to pre-check")
		return nil
	}
	a.log.Debug("Pre-checking applications")
	defer a.log.Debug("Finished pre-checking applications")

	// ensure dependencies for image based application manifests
	imageProviders, err := applications.ImageProvidersFromSpec(desired)
	if err != nil {
		return fmt.Errorf("%w: parsing image providers: %w", errors.ErrNoRetry, err)
	}

	for _, imageProvider := range imageProviders {
		if a.podmanClient.ImageExists(ctx, imageProvider.Image) {
			a.log.Debugf("Image %q already exists", imageProvider.Image)
			continue
		}

		providerImage := imageProvider.Image
		// pull the image if it does not exist. it is possible that the image
		// tag such as latest in which case it will be pulled later. but we
		// don't want to require calling out the network on every sync.
		if !a.podmanClient.ImageExists(ctx, imageProvider.Image) {
			err := wait.ExponentialBackoffWithContext(ctx, a.backoff, func() (bool, error) {
				resp, err := a.podmanClient.Pull(ctx, providerImage)
				if err != nil {
					a.log.Warnf("Failed to pull image %q: %v", providerImage, err)
					return false, nil
				}
				a.log.Debugf("Pulled image %q: %s", providerImage, resp)
				return true, nil
			})
			if err != nil {
				return fmt.Errorf("creating enrollment request: %w", err)
			}
		}

		addType, err := applications.TypeFromImage(ctx, a.podmanClient, providerImage)
		if err != nil {
			return fmt.Errorf("%w: getting application type: %w", errors.ErrNoRetry, err)
		}
		if err := applications.EnsureDependenciesFromType(addType); err != nil {
			return fmt.Errorf("%w: ensuring dependencies: %w", errors.ErrNoRetry, err)
		}
	}

	return nil
}

func (a *Agent) syncDevice(ctx context.Context, current, desired *v1alpha1.RenderedDeviceSpec) error {
	if err := a.consoleController.Sync(ctx, desired); err != nil {
		a.log.Errorf("Failed to sync console configuration: %s", err)
	}

	if err := a.applicationsController.Sync(ctx, current, desired); err != nil {
		return fmt.Errorf("applications: %w", err)
	}

	if err := a.hookManager.Sync(current, desired); err != nil {
		return fmt.Errorf("hooks: %w", err)
	}

	if err := a.configController.Sync(ctx, current, desired); err != nil {
		return fmt.Errorf("config: %w", err)
	}

	if err := a.resourceController.Sync(ctx, desired); err != nil {
		return fmt.Errorf("resources: %w", err)
	}

	if err := a.osImageController.Sync(ctx, desired); err != nil {
		return fmt.Errorf("os image: %w", err)
	}

	// set status collector properties based on new desired spec
	a.statusManager.SetProperties(desired)

	return nil
}

func (a *Agent) afterUpdate(ctx context.Context) error {
	a.log.Debug("Executing post actions")
	defer a.log.Debug("Finished executing post actions")

	// execute post actions for applications
	if err := a.appManager.ExecuteActions(ctx); err != nil {
		a.log.Errorf("Error executing actions: %v", err)
		return err
	}

	return nil
}

func (a *Agent) enqueue(ctx context.Context) error {
	// if updating don't call the spec manager use desired from disk
	desired, err := a.specManager.GetDesired(ctx, a.currentRenderedVersion)
	if err != nil {
		return err
	}

	version := stringToInt64(desired.RenderedVersion)
	item := spec.NewItem(desired, version)

	a.queue.Add(item)
	return nil
}

func (a *Agent) handleSyncError(ctx context.Context, desiredSpec *v1alpha1.RenderedDeviceSpec, syncErr error) {
	version := stringToInt64(desiredSpec.RenderedVersion)
	statusUpdate := v1alpha1.DeviceSummaryStatus{}

	if !errors.IsRetryable(syncErr) {
		statusUpdate.Status = v1alpha1.DeviceSummaryStatusError
		statusUpdate.Info = util.StrToPtr(fmt.Sprintf("Reconciliation failed for version %v: %v", version, syncErr))
		a.queue.SetVersionFailed(version)
		a.queue.Forget(version)
	} else {
		statusUpdate.Status = v1alpha1.DeviceSummaryStatusDegraded
		statusUpdate.Info = util.StrToPtr(fmt.Sprintf("Failed to sync device: %v", syncErr))
		a.queue.Requeue(version)
	}

	if _, err := a.statusManager.Update(ctx, status.SetDeviceSummary(statusUpdate)); err != nil {
		a.log.Errorf("Failed to update device status: %v", err)
	}

	a.log.Error(*statusUpdate.Info)
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
