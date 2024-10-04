package device

import (
	"context"
	"fmt"
	"time"

	"github.com/flightctl/flightctl/api/v1alpha1"
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
	name               string
	deviceWriter       fileio.Writer
	statusManager      status.Manager
	specManager        spec.Manager
	hookManager        hook.Manager
	configController   *config.Controller
	osImageController  *OSImageController
	resourceController *resource.Controller
	consoleController  *console.ConsoleController
	bootcClient        container.BootcClient

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
	fetchSpecInterval util.Duration,
	fetchStatusInterval util.Duration,
	hookManager hook.Manager,
	configController *config.Controller,
	osImageController *OSImageController,
	resourceController *resource.Controller,
	consoleController *console.ConsoleController,
	log *log.PrefixLogger,
	bootcClient container.BootcClient,
) *Agent {
	return &Agent{
		name:                name,
		deviceWriter:        deviceWriter,
		statusManager:       statusManager,
		specManager:         specManager,
		hookManager:         hookManager,
		fetchSpecInterval:   fetchSpecInterval,
		fetchStatusInterval: fetchStatusInterval,
		configController:    configController,
		osImageController:   osImageController,
		resourceController:  resourceController,
		consoleController:   consoleController,
		log:                 log,
		bootcClient:         bootcClient,
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
			a.log.Debug("Fetching device spec")
			deviceUpdated, err := a.syncDevice(ctx)
			if err != nil {
				infoMsg := fmt.Sprintf("Failed to sync device: %v", err)
				_, updateErr := a.statusManager.Update(ctx, status.SetDeviceSummary(v1alpha1.DeviceSummaryStatus{
					Status: v1alpha1.DeviceSummaryStatusDegraded,
					Info:   util.StrToPtr(infoMsg),
				}))
				if updateErr != nil {
					a.log.Errorf("Failed to update device status: %v", updateErr)
				}
				a.log.Error(infoMsg)
				continue
			}

			if deviceUpdated {
				_, updateErr := a.statusManager.Update(ctx, status.SetDeviceSummary(v1alpha1.DeviceSummaryStatus{
					Status: v1alpha1.DeviceSummaryStatusOnline,
					Info:   nil,
				}))
				if updateErr != nil {
					a.log.Errorf("Updating device status: %v", updateErr)
				}
			}
		case <-fetchStatusTicker.C:
			a.log.Debug("Fetching device status")
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
	}
}

func (a *Agent) syncDevice(ctx context.Context) (bool, error) {
	// TODO: make state reads more efficient do we really need the complete
	// current and desired specs in memory? could we cache the rendered versions?
	current, err := a.specManager.Read(spec.Current)
	if err != nil {
		return false, err
	}

	desired, err := a.specManager.GetDesired(ctx, current.RenderedVersion)
	if err != nil {
		return false, err
	}

	if current.RenderedVersion == "" && desired.RenderedVersion == "" {
		return false, nil
	}

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
