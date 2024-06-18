package action

import (
	"context"
	"fmt"
	"testing"

	"github.com/fsnotify/fsnotify"
	"github.com/stretchr/testify/require"
)

func TestActions(t *testing.T) {
	require := require.New(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	manager, err := NewManager()
	require.NoError(err)

	// register the systemd app and actions implantation.
	systemd := NewSystemD(nil)
	manager.RegisterApp(AppSystemd, systemd)

	// define a default event handler for microShift which watches for new files
	// added to the manifest directory /etc/microshift/manifests then reloads
	// the microshift service
	microShiftConfigHandlerFn := func(event fsnotify.Event) error {
		switch event.Op {
		case fsnotify.Create, fsnotify.Write, fsnotify.Rename, fsnotify.Remove:
			// reload of we observe new files, writes or renames
			err := systemd.Reload(ctx, "microshift.service")
			if err != nil {
				return fmt.Errorf("failed to reload microshift: %w", err)
			}
		}
		return nil
	}

	// register a default handler for microShift
	microShiftSystemdHandler := NewHandler(AppSystemd, microShiftConfigHandlerFn)
	err = manager.RegisterHandler("/etc/microshift/manifests", microShiftSystemdHandler)
	require.NoError(err)

	// register the podman service manager
	podman := NewPodman(nil)
	manager.RegisterApp(AppPodman, podman)

	// register a default handler for podman to start/stop pods if the pod
	// spec is created, removed, written or renamed to the manifest
	// directory /etc/podman/manifests and starts/stops the pod.
	podmanPodHandlerFn := func(event fsnotify.Event) error {
		switch event.Op {
		case fsnotify.Create:
			// start
			err := podman.Start(ctx, event.Name)
			if err != nil {
				return fmt.Errorf("failed to start microshift: %w", err)
			}
		case fsnotify.Write, fsnotify.Rename:
			// stop and start
			err := podman.Stop(ctx, event.Name)
			if err != nil {
				return fmt.Errorf("failed to reload microshift: %w", err)
			}
			err = podman.Start(ctx, event.Name)
			if err != nil {
				return fmt.Errorf("failed to reload microshift: %w", err)
			}
		case fsnotify.Remove:
			// stop
			err := podman.Stop(ctx, event.Name)
			if err != nil {
				return fmt.Errorf("failed to stop microshift: %w", err)
			}

		}
		return nil
	}

	// register a default handler for podman to start/stop pods if the pod
	// spec is created, removed, written or renamed to the manifest
	// directory
	podmanHandler := NewHandler(AppPodman, podmanPodHandlerFn)
	err = manager.RegisterHandler("/etc/podman/manifests", podmanHandler)
	require.NoError(err)

	// register the sysctl service manager
	sysctl := NewSysctl(nil)
	manager.RegisterApp(AppSysctl, sysctl)

	// define a custom event handler for kernel args passed as config files to
	// sysctl in /etc/sysctl.d/ and reboots the node.
	kernelArgsConfigHandlerFn := func(event fsnotify.Event) error {
		switch event.Op {
		case fsnotify.Create, fsnotify.Write, fsnotify.Remove, fsnotify.Rename:
			err := manager.Reboot(ctx, "kernel args changed")
			if err != nil {
				return err
			}
		}
		return nil
	}

	// register a default handler for sysctl to reboot the node when kernel args change
	kernelArgRebootHandler := NewHandler(AppSysctl, kernelArgsConfigHandlerFn)
	err = manager.RegisterHandler("/etc/sysctl.d/", kernelArgRebootHandler)
	require.NoError(err)

	// define a custom event handler for some.service this would be consumed from the agent.Config

	// example of a custom systemd post action hook
	actionConfig := Config{ // Type to embed in the agent.Config
		Target:    "some.service",     // Target service to act on
		WatchPath: "/etc/some/config", // Path to watch for changes
		App:       AppSystemd,         // App to use for the action
		Events: []Event{
			{
				Actions: []Action{
					ActionReload,
				},
				Op: Create,
			},
			{
				Actions: []Action{
					ActionReload,
				},
				Op: Write,
			},
			{
				Actions: []Action{
					ActionReload,
				},
				Op: Remove,
			},
			{
				Actions: []Action{
					ActionReload,
				},
				Op: Rename,
			},
		},
	}

	customHandler := manager.CreateHandlerFromConfig(actionConfig)
	err = manager.RegisterHandler(actionConfig.WatchPath, customHandler)
	require.NoError(err)

	manager.Run(ctx)
}
