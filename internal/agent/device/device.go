package device

import (
	"context"
	"fmt"
	"time"

	"github.com/flightctl/flightctl/internal/agent/device/config"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/agent/device/spec"
	"github.com/flightctl/flightctl/internal/agent/device/status"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/lthibault/jitterbug"
	"k8s.io/apimachinery/pkg/api/equality"
)

// Agent is responsible for managing the workloads, configuration and status of the device.
type Agent struct {
	name              string
	deviceWriter      *fileio.Writer
	statusManager     status.Manager
	specManager       *spec.Manager
	configController  *config.Controller
	osImageController *OSImageController

	fetchSpecInterval   time.Duration
	fetchStatusInterval time.Duration

	log *log.PrefixLogger
}

// NewAgent creates a new device agent.
func NewAgent(
	name string,
	deviceWriter *fileio.Writer,
	statusManager status.Manager,
	specManager *spec.Manager,
	fetchSpecInterval time.Duration,
	fetchStatusInterval time.Duration,
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
func (a *Agent) Run(ctx context.Context) {
	// TODO: needs tuned
	fetchSpecTicker := jitterbug.New(a.fetchSpecInterval, &jitterbug.Norm{Stdev: 30 * time.Millisecond, Mean: 0})
	defer fetchSpecTicker.Stop()
	fetchStatusTicker := jitterbug.New(a.fetchStatusInterval, &jitterbug.Norm{Stdev: 30 * time.Millisecond, Mean: 0})
	defer fetchStatusTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-fetchSpecTicker.C:
			a.log.Debug("Fetching device spec")
			if err := a.syncDevice(ctx); err != nil {
				// TODO handle status updates
				a.log.Errorf("Failed to ensure device: %v", err)
			}
		case <-fetchStatusTicker.C:
			a.log.Debug("Fetching device status")
			status, err := a.statusManager.Get(ctx)
			if err != nil {
				a.log.Errorf("Failed to get device status: %v", err)
			}
			if err := a.statusManager.Update(ctx, status); err != nil {
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

	if err := a.configController.Sync(&desired); err != nil {
		return err
	}

	if err := a.osImageController.Sync(ctx, &desired); err != nil {
		return fmt.Errorf("sync os image: %w", err)
	}

	// set status collector properties based on new desired spec
	a.statusManager.SetProperties(&desired)

	// write the desired spec to the current spec file
	// this would only happen if there was no os image change as that requires a reboot
	return a.specManager.WriteCurrentRendered(&desired)
}
