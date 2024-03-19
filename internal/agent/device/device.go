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
	"github.com/lthibault/jitterbug"
	"github.com/sirupsen/logrus"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/klog/v2"
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

	log       *logrus.Logger
	logPrefix string
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
	log *logrus.Logger,
	logPrefix string,
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
		logPrefix:           logPrefix,
	}
}

// Run starts the device agent reconciliation loop.
func (a *Agent) Run(ctx context.Context) error {
	// TODO: needs tuned
	fetchSpecTicker := jitterbug.New(a.fetchSpecInterval, &jitterbug.Norm{Stdev: util.CreateRandomJitterDuration(20, time.Millisecond), Mean: 0})
	defer fetchSpecTicker.Stop()
	fetchStatusTicker := jitterbug.New(a.fetchStatusInterval, &jitterbug.Norm{Stdev: util.CreateRandomJitterDuration(20, time.Millisecond), Mean: 0})
	defer fetchStatusTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-fetchSpecTicker.C:
			klog.V(4).Infof("%s fetching device spec", a.logPrefix)
			if err := a.ensureDevice(ctx); err != nil {
				// TODO handle status updates
				a.log.Errorf("%sfailed to ensure device: %v", a.logPrefix, err)
			}
		case <-fetchStatusTicker.C:
			klog.V(4).Infof("%s fetching device status", a.logPrefix)
			status, err := a.statusManager.Get(ctx)
			if err != nil {
				a.log.Errorf("%s failed to  device status: %v", a.logPrefix, err)
			}
			if err := a.statusManager.Update(ctx, status); err != nil {
				a.log.Errorf("%s failed to update device status: %v", a.logPrefix, err)
			}

		}
	}
}

func (a *Agent) ensureDevice(ctx context.Context) error {
	// TODO: don't keep the spec constantly in memory
	current, desired, skipSync, err := a.specManager.GetRendered(ctx)
	if err != nil {
		return err
	}

	if equality.Semantic.DeepEqual(current, desired) || skipSync {
		a.log.Debugf("%sskipping device reconciliation", a.logPrefix)
		return nil
	}

	// if the current and desired specs are different, we need to reconcile the device
	if err := a.reconcileDevice(ctx, &desired); err != nil {
		return fmt.Errorf("reconcile device: %w", err)
	}

	if err := a.osImageController.Sync(ctx, &desired); err != nil {
		return fmt.Errorf("sync os image: %w", err)
	}

	// write the desired spec to the current spec file
	// this would only happen if there was no os image change as that requires a reboot
	return a.specManager.WriteCurrentRendered(&desired)
}

func (a *Agent) reconcileDevice(_ context.Context, desired *v1alpha1.RenderedDeviceSpec) error {
	if err := a.configController.Sync(desired); err != nil {
		return err
	}

	return nil
}
