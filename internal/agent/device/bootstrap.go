package device

import (
	"context"
	"fmt"
	"os"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/agent/device/hook"
	"github.com/flightctl/flightctl/internal/agent/device/lifecycle"
	"github.com/flightctl/flightctl/internal/agent/device/spec"
	"github.com/flightctl/flightctl/internal/agent/device/status"
	"github.com/flightctl/flightctl/internal/agent/device/systeminfo"
	"github.com/flightctl/flightctl/internal/agent/identity"
	baseclient "github.com/flightctl/flightctl/internal/client"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/version"
	"github.com/samber/lo"
)

const (
	BootstrapComplete = "Bootstrap complete"
)

type Bootstrap struct {
	deviceName        string
	executer          executer.Executer
	deviceReadWriter  fileio.ReadWriter
	specManager       spec.Manager
	statusManager     status.Manager
	hookManager       hook.Manager
	systemInfoManager systeminfo.Manager
	podmanClient      *client.Podman
	systemdClient     *client.Systemd

	lifecycle lifecycle.Initializer

	managementServiceConfig   *baseclient.Config
	managementClient          client.Management
	managementMetricsCallback client.RPCMetricsCallback
	identityProvider          identity.Provider

	log *log.PrefixLogger
}

func NewBootstrap(
	deviceName string,
	executer executer.Executer,
	deviceReadWriter fileio.ReadWriter,
	specManager spec.Manager,
	statusManager status.Manager,
	hookManager hook.Manager,
	lifecycleInitializer lifecycle.Initializer,
	managementServiceConfig *baseclient.Config,
	systemInfoManager systeminfo.Manager,
	managementMetricsCallback client.RPCMetricsCallback,
	podmanClient *client.Podman,
	systemdClient *client.Systemd,
	identityProvider identity.Provider,
	log *log.PrefixLogger,
) *Bootstrap {
	return &Bootstrap{
		deviceName:                deviceName,
		executer:                  executer,
		deviceReadWriter:          deviceReadWriter,
		specManager:               specManager,
		statusManager:             statusManager,
		hookManager:               hookManager,
		lifecycle:                 lifecycleInitializer,
		managementServiceConfig:   managementServiceConfig,
		systemInfoManager:         systemInfoManager,
		managementMetricsCallback: managementMetricsCallback,
		podmanClient:              podmanClient,
		systemdClient:             systemdClient,
		identityProvider:          identityProvider,
		log:                       log,
	}
}

func (b *Bootstrap) Initialize(ctx context.Context) error {
	b.log.Infof("Bootstrapping device: %s", b.deviceName)

	var podmanStr string
	podmanVersion, err := b.podmanClient.Version(ctx)
	if err != nil {
		b.log.Error(err)
	} else {
		podmanStr = fmt.Sprintf(", podman-version=%d.%d", podmanVersion.Major, podmanVersion.Minor)
	}

	versionInfo := version.Get()
	b.log.Infof("System information: version=%s, go-version=%s, platform=%s, git-commit=%s%s",
		versionInfo.String(),
		versionInfo.GoVersion,
		versionInfo.Platform,
		versionInfo.GitCommit,
		podmanStr,
	)

	if err := b.ensureSpecFiles(ctx); err != nil {
		return err
	}

	if err := b.ensureEnrollment(ctx); err != nil {
		return err
	}

	if err := b.setManagementClient(); err != nil {
		return err
	}

	if err := b.ensureBootstrap(ctx); err != nil {
		infoMsg := fmt.Sprintf("Bootstrap failed: %v", err)
		_, updateErr := b.statusManager.Update(ctx, status.SetDeviceSummary(v1beta1.DeviceSummaryStatus{
			Status: v1beta1.DeviceSummaryStatusError,
			Info:   lo.ToPtr(infoMsg),
		}))
		if updateErr != nil {
			b.log.Warnf("Failed setting status: %v", updateErr)
		}
		b.log.Error(infoMsg)

		return err
	}

	b.updateStatus(ctx)

	// Report connectivity status to systemd.
	// This is visible via `systemctl status flightctl-agent` as StatusText.
	if b.systemdClient != nil {
		if err := b.systemdClient.SdNotify(ctx, "STATUS=Connected"); err != nil {
			b.log.Errorf("Failed to notify systemd of connectivity status: %v", err)
		}
	}

	// unset NOTIFY_SOCKET on successful bootstrap to prevent subprocesses from
	// using it.
	// ref: https://bugzilla.redhat.com/show_bug.cgi?id=1781506
	os.Unsetenv("NOTIFY_SOCKET")

	b.log.Info(BootstrapComplete)
	return nil
}

func (b *Bootstrap) ensureEnrollment(ctx context.Context) error {
	err := b.statusManager.Collect(ctx)
	if err != nil {
		b.log.Warnf("Collecting device status: %v", err)
	}

	status := b.statusManager.Get(ctx)
	if status == nil {
		b.log.Warn("Device status is nil, returning default status")
		status = &v1beta1.DeviceStatus{}
	}
	if err := b.lifecycle.Initialize(ctx, status); err != nil {
		return fmt.Errorf("failed to initialize lifecycle: %w", err)
	}

	return nil
}

func (b *Bootstrap) updateStatus(ctx context.Context) {
	updatingCondition := v1beta1.Condition{
		Type: v1beta1.ConditionTypeDeviceUpdating,
	}

	if b.specManager.IsUpgrading() {
		updatingCondition.Status = v1beta1.ConditionStatusTrue
		// TODO: only set rebooting in case where we are actually rebooting
		updatingCondition.Reason = string(v1beta1.UpdateStateRebooting)
	} else {
		updatingCondition.Status = v1beta1.ConditionStatusFalse
		updatingCondition.Reason = string(v1beta1.UpdateStateUpdated)
	}

	_, updateErr := b.statusManager.Update(ctx,
		status.SetConfig(v1beta1.DeviceConfigStatus{
			RenderedVersion: b.specManager.RenderedVersion(spec.Current),
		}),
		status.SetCondition(updatingCondition),
	)
	if updateErr != nil {
		b.log.Warnf("Failed setting status: %v", updateErr)
	}
}

func (b *Bootstrap) ensureSpecFiles(ctx context.Context) error {
	if err := b.specManager.Ensure(); err != nil {
		return fmt.Errorf("ensuring spec files: %w", err)
	}
	return nil
}

func (b *Bootstrap) ensureBootstrap(ctx context.Context) error {
	if err := b.ensureBootedOS(ctx); err != nil {
		return err
	}

	if b.systemInfoManager.IsRebooted() {
		if err := b.hookManager.OnAfterRebooting(ctx); err != nil {
			// TODO: rollback?
			b.log.Errorf("running after rebooting hook: %v", err)
		}
	}

	return nil
}

func (b *Bootstrap) ensureBootedOS(ctx context.Context) error {
	if !b.specManager.IsOSUpdate() {
		b.log.Info("No OS update in progress")
		// If not upgrading but rollback.json has a desired version, a rollback
		// already completed and the agent restarted. Mark the desired version
		// as failed to prevent re-applying it.
		if !b.specManager.IsUpgrading() {
			if desiredVersion := b.specManager.GetRollbackDesiredVersion(); desiredVersion != "" {
				b.log.Infof("Marking version %s as failed from previous rollback", desiredVersion)
				if err := b.specManager.SetUpgradeFailed(desiredVersion); err != nil {
					b.log.Errorf("Failed to mark version %s as failed: %v", desiredVersion, err)
				}
			}
		}
		return nil
	}

	return b.checkRollback(ctx)
}

func (b *Bootstrap) checkRollback(ctx context.Context) error {
	// check if the bootedOS image is expected
	bootedOS, reconciled, err := b.specManager.CheckOsReconciliation(ctx)
	if err != nil {
		return fmt.Errorf("checking if OS image is reconciled: %w", err)
	}

	if reconciled {
		b.log.Infof("Booted into desired OS image: %s", bootedOS)
		return nil
	}

	desiredOS := b.specManager.OSVersion(spec.Desired)
	// We rebooted without applying the new OS image - something potentially went wrong
	b.log.Warnf("Booted OS image (%s) does not match the desired OS image (%s)", bootedOS, desiredOS)

	_, updateErr := b.statusManager.Update(ctx, status.SetDeviceSummary(v1beta1.DeviceSummaryStatus{
		Status: v1beta1.DeviceSummaryStatusError,
		Info:   lo.ToPtr(fmt.Sprintf("Booted image %s, expected %s", bootedOS, desiredOS)),
	}))
	if updateErr != nil {
		b.log.Warnf("Failed setting status: %v", updateErr)
	}

	rollback, err := b.specManager.IsRollingBack(ctx)
	if err != nil {
		return fmt.Errorf("checking if rollback is in progress: %w", err)
	}

	if !rollback {
		// this is possible if device was rebooted before new image was applied
		b.log.Warn("No rollback in progress, continuing bootstrap to apply rollback spec")
		return nil
	}

	b.log.Warn("Starting spec rollback")
	// rollback and set the version to failed
	if err := b.specManager.Rollback(ctx, spec.WithSetFailed()); err != nil {
		return fmt.Errorf("failed spec rollback: %w", err)
	}
	b.log.Info("Spec rollback complete, resuming bootstrap")

	updateErr = b.statusManager.UpdateCondition(ctx, v1beta1.Condition{
		Type:    v1beta1.ConditionTypeDeviceUpdating,
		Status:  v1beta1.ConditionStatusTrue,
		Reason:  string(v1beta1.UpdateStateRollingBack),
		Message: fmt.Sprintf("Device is rolling back to template version: %s", b.specManager.RenderedVersion(spec.Desired)),
	})
	if updateErr != nil {
		b.log.Warnf("Failed setting status: %v", updateErr)
	}

	return nil
}

func (b *Bootstrap) setManagementClient() error {
	var err error
	b.managementClient, err = b.identityProvider.CreateManagementClient(b.managementServiceConfig, b.managementMetricsCallback)
	if err != nil {
		return fmt.Errorf("create management client: %w", err)
	}

	// initialize the management client for spec and status managers
	b.statusManager.SetClient(b.managementClient)
	b.specManager.SetClient(b.managementClient)
	return nil
}

// ManagementClient returns the management client for use by other components.
func (b *Bootstrap) ManagementClient() client.Management {
	return b.managementClient
}
