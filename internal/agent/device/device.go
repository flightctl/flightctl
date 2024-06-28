package device

import (
	"context"
	"fmt"
	"time"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/device/config"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/agent/device/spec"
	"github.com/flightctl/flightctl/internal/agent/device/status"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/lthibault/jitterbug"
	"k8s.io/apimachinery/pkg/api/equality"
)

// Agent is responsible for managing the applications, configuration and status of the device.
type Agent struct {
	name              string
	deviceWriter      *fileio.Writer
	statusManager     status.Manager
	specManager       *spec.Manager
	configController  *config.Controller
	osImageController *OSImageController

	fetchSpecInterval   util.Duration
	fetchStatusInterval util.Duration

	log *log.PrefixLogger
}

// NewAgent creates a new device agent.
func NewAgent(
	name string,
	deviceWriter *fileio.Writer,
	statusManager status.Manager,
	specManager *spec.Manager,
	fetchSpecInterval util.Duration,
	fetchStatusInterval util.Duration,
	configController *config.Controller,
	osImageController *OSImageController,
	log *log.PrefixLogger,
) *Agent {
	return &Agent{
		name:                name,
		deviceWriter:        deviceWriter,
		statusManager:       statusManager,
		specManager:         specManager,
		fetchSpecInterval:   fetchSpecInterval,
		fetchStatusInterval: fetchStatusInterval,
		configController:    configController,
		osImageController:   osImageController,
		log:                 log,
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
			if err := a.syncDevice(ctx); err != nil {
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

			_, updateErr := a.statusManager.Update(ctx, status.SetDeviceSummary(v1alpha1.DeviceSummaryStatus{
				Status: v1alpha1.DeviceSummaryStatusOnline,
				Info:   nil,
			}))
			if updateErr != nil {
				a.log.Errorf("Failed to update device status: %v", updateErr)
			}
		case <-fetchStatusTicker.C:
			a.log.Debug("Fetching device status")
			if err := a.statusManager.Sync(ctx); err != nil {
				a.log.Errorf("Failed to update device status: %v", err)
			}

		}
	}
}

func (a *Agent) syncDevice(ctx context.Context) error {
	// TODO: don't keep the spec constantly in memory
	current, desired, skipSync, err := a.specManager.GetRendered(ctx)
	if err != nil {
		return err
	}

	if equality.Semantic.DeepEqual(current, desired) || skipSync {
		a.log.Debug("Skipping device reconciliation")
		return nil
	}

	updateErr := a.statusManager.UpdateCondition(ctx, v1alpha1.Condition{
		Type:    v1alpha1.DeviceUpdating,
		Status:  v1alpha1.ConditionStatusTrue,
		Reason:  "Updating",
		Message: fmt.Sprintf("The device is updating to renderedVersion: %s", desired.RenderedVersion),
	})
	if updateErr != nil {
		a.log.Warnf("Failed setting status: %v", updateErr)
	}

	if err := a.configController.Sync(&desired); err != nil {
		return err
	}

	if err := a.osImageController.Sync(ctx, &desired); err != nil {
		return err
	}

	// set status collector properties based on new desired spec
	a.statusManager.SetProperties(&desired)

	// write the desired spec to the current spec file
	// this would only happen if there was no os image change as that requires a reboot
	if err := a.specManager.WriteCurrentRendered(&desired); err != nil {
		return err
	}

	updateErr = a.statusManager.UpdateCondition(ctx, v1alpha1.Condition{
		Type:    v1alpha1.DeviceUpdating,
		Status:  v1alpha1.ConditionStatusFalse,
		Reason:  "Updated",
		Message: fmt.Sprintf("Updated to desired renderedVersion: %s", desired.RenderedVersion),
	})
	if updateErr != nil {
		a.log.Warnf("Failed setting status: %v", updateErr)
	}

	updateFns := []status.UpdateStatusFn{
		status.SetOSImage(v1alpha1.DeviceOSStatus{
			Image: current.Os.Image,
		}),
		status.SetConfig(v1alpha1.DeviceConfigStatus{
			RenderedVersion: current.RenderedVersion,
		}),
	}

	_, updateErr = a.statusManager.Update(ctx, updateFns...)
	if updateErr != nil {
		a.log.Warnf("Failed setting status: %v", updateErr)
	}

	return nil
}
