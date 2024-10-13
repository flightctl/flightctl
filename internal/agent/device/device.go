package device

import (
	"context"
	"fmt"
	"time"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/applications"
	"github.com/flightctl/flightctl/internal/agent/device/config"
	"github.com/flightctl/flightctl/internal/agent/device/console"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/agent/device/hook"
	"github.com/flightctl/flightctl/internal/agent/device/resource"
	"github.com/flightctl/flightctl/internal/agent/device/spec"
	"github.com/flightctl/flightctl/internal/agent/device/status"
	"github.com/flightctl/flightctl/internal/container"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/lthibault/jitterbug"
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

	log *log.PrefixLogger
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
	log *log.PrefixLogger,
) *Agent {
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
		log:                    log,
	}
}

// Run starts the device agent reconciliation loop.
func (a *Agent) Run(ctx context.Context) error {
	// TODO: needs tuned
	fetchSpecTicker := jitterbug.New(time.Duration(a.fetchSpecInterval), &jitterbug.Norm{Stdev: 30 * time.Millisecond, Mean: 0})
	defer fetchSpecTicker.Stop()
	fetchStatusTicker := jitterbug.New(time.Duration(a.fetchStatusInterval), &jitterbug.Norm{Stdev: 30 * time.Millisecond, Mean: 0})
	defer fetchStatusTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-fetchSpecTicker.C:
			a.fetchDeviceSpec(ctx)
		case <-fetchStatusTicker.C:
			a.fetchDeviceStatus(ctx)
		}
	}
}

func (a *Agent) fetchDeviceSpec(ctx context.Context) {
	startTime := time.Now()
	a.log.Debug("Starting fetch device spec")
	defer func() {
		duration := time.Since(startTime)
		a.log.Debugf("Completed fetch device spec in %v", duration)
	}()

	if err := a.sync(ctx); err != nil {
		infoMsg := fmt.Sprintf("Failed to sync device: %v", err)
		_, updateErr := a.statusManager.Update(ctx, status.SetDeviceSummary(v1alpha1.DeviceSummaryStatus{
			Status: v1alpha1.DeviceSummaryStatusDegraded,
			Info:   util.StrToPtr(infoMsg),
		}))
		if updateErr != nil {
			a.log.Errorf("Failed to update device status: %v", updateErr)
		}
		a.log.Error(infoMsg)
	}
}

func (a *Agent) fetchDeviceStatus(ctx context.Context) {
	startTime := time.Now()
	a.log.Debug("Starting fetch device status")
	defer func() {
		duration := time.Since(startTime)
		a.log.Debugf("Completed fetch device status in %v", duration)
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

func (a *Agent) sync(ctx context.Context) error {
	// TODO: make state reads more efficient do we really need the complete
	// current and desired specs in memory? could we cache the rendered versions?
	current, err := a.specManager.Read(spec.Current)
	if err != nil {
		return err
	}

	desired, err := a.specManager.GetDesired(ctx, current.RenderedVersion)
	if err != nil {
		return err
	}

	if current.RenderedVersion == "" && desired.RenderedVersion == "" {
		return nil
	}

	if err = a.beforeUpdate(ctx, current, desired); err != nil {
		return err
	}

	deviceUpdated, err := a.syncDevice(ctx, current, desired)
	if err != nil {
		return err
	}

	if err = a.afterUpdate(ctx); err != nil {
		// TODO: any error here should be an error status
		return err
	}

	// if we observed a change in the device without error, update the device status to online
	if deviceUpdated {
		_, updateErr := a.statusManager.Update(ctx, status.SetDeviceSummary(v1alpha1.DeviceSummaryStatus{
			Status: v1alpha1.DeviceSummaryStatusOnline,
			Info:   nil,
		}))
		if updateErr != nil {
			a.log.Errorf("Updating device status: %v", updateErr)
		}
	}

	return nil
}

func (a *Agent) beforeUpdate(ctx context.Context, current, desired *v1alpha1.RenderedDeviceSpec) error {
	if err := a.beforeUpdateApplications(ctx, current, desired); err != nil {
		return err
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

	// pull image based application manifests
	imageProviders, err := applications.ImageProvidersFromSpec(desired)
	if err != nil {
		return err
	}

	for _, imageProvider := range imageProviders {
		// TODO: retry pull
		resp, err := a.podmanClient.Pull(ctx, imageProvider.Image)
		if err != nil {
			return err
		}
		a.log.Debugf("Pulled image %q: %s", imageProvider.Image, resp)

		addType, err := applications.TypeFromImage(ctx, a.podmanClient, imageProvider.Image)
		if err != nil {
			return err
		}
		if err := applications.EnsureDependenciesFromType(addType); err != nil {
			return err
		}
	}

	return nil
}

func (a *Agent) syncDevice(ctx context.Context, current, desired *v1alpha1.RenderedDeviceSpec) (bool, error) {
	if err := a.consoleController.Sync(ctx, desired); err != nil {
		a.log.Errorf("Failed to sync console configuration: %s", err)
	}

	if spec.IsUpdating(current, desired) {
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

	if err := a.applicationsController.Sync(ctx, current, desired); err != nil {
		return false, err
	}

	if err := a.hookManager.Sync(current, desired); err != nil {
		return false, err
	}

	if err := a.configController.Sync(ctx, current, desired); err != nil {
		return false, err
	}

	if err := a.resourceController.Sync(ctx, desired); err != nil {
		return false, err
	}

	if err := a.osImageController.Sync(ctx, desired); err != nil {
		return false, err
	}

	// set status collector properties based on new desired spec
	a.statusManager.SetProperties(desired)

	if !spec.IsUpdating(current, desired) {
		return false, nil
	}

	if err := a.specManager.Upgrade(); err != nil {
		return false, err
	}

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
			return false, err
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

	a.log.Infof("Synced device to renderedVersion: %s", desired.RenderedVersion)

	return true, nil
}

func (a *Agent) afterUpdate(ctx context.Context) error {
	a.log.Debug("Executing post actions")
	defer a.log.Debug("Finished executing post actions")

	// execute post actions for applications
	if err := a.appManager.ExecuteActions(ctx); err != nil {
		return err
	}

	return nil
}
