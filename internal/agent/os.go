// Copyright (c) 2023 Red Hat, Inc.

package agent

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/coreos/go-systemd/v22/dbus"
	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/container"
	"github.com/flightctl/flightctl/internal/quadlet"
	"github.com/flightctl/flightctl/pkg/log"
	"golang.org/x/exp/maps"
)

type OS struct {
	podman  container.Podman
	systemd *dbus.Conn
}

func NewOS(podman container.Podman, systemd *dbus.Conn) *OS {
	return &OS{
		podman:  podman,
		systemd: systemd,
	}
}

func (o *OS) RunningContainers() (map[string]container.Container, error) {
	list, err := o.podman.ListContainers(context.Background(), nil)
	if err != nil {
		return nil, err
	}
	m := make(map[string]container.Container)
	for _, c := range list {
		m[c.Names[0]] = c
	}
	return m, nil
}

func (o *OS) Reboot() error {
	return exec.Command("systemctl", "reboot").Run()
}

func (o *OS) Poweroff() error {
	return exec.Command("systemctl", "poweroff").Run()
}

// ExitedContainerStatus returns the exit code of an exited container application.
// It checks the status of all containers in the application and returns an aggregated exit code.
// If any container has not exited, it returns an error.
func (o *OS) ExitedContainerStatus(ctx context.Context, appName string, containers map[string]v1alpha1.ContainerSpec) (int, error) {
	log := log.WithFields(log.Fields{"app": appName})
	containerNames := maps.Keys(containers)

	statuses, err := o.podman.AllContainersStatus(ctx, appName, containerNames)
	if err != nil {
		return 0, err
	}

	for _, c := range statuses {
		if c.State != "exited" {
			return 0, fmt.Errorf("container %s is in state %s", c.Name, c.State)
		}
		if c.ExitCode != 0 {
			log.Infof("Container %s of application %s exited with non-zero status code: %d", c.Name, appName, c.ExitCode)
			// Return the first non-zero exit code
			return c.ExitCode, nil
		}

		// All containers exited with code 0. Check if this is an error (e.g. manual stop of a service).
		unitName := quadlet.GetUnitName(appName, c.Name)
		restartProp, err := o.systemd.GetUnitProperty(unitName, "Restart")
		if err != nil {
			log.Errorf("Failed to get Restart property for unit %s: %v. Assuming error.", unitName, err)
			return 1, nil // Non-zero exit code for error
		}

		restartPolicy, ok := restartProp.Value.Value().(string)
		if !ok {
			log.Errorf("Failed to parse Restart property for unit %s. Assuming error.", unitName)
			return 1, nil
		}

		if restartPolicy == "always" || restartPolicy == "on-failure" {
			log.Warnf("Container %s of application %s has Restart policy '%s' but is not running. This might indicate a manual stop.", c.Name, appName, restartPolicy)
			return 1, nil // Non-zero exit code for what we consider an error state
		}
	}

	return 0, nil
}

func (o *OS) SetNeedsReboot() error {
	f, err := os.Create("/run/flightctl-needs-reboot")
	if err != nil {
		return err
	}
	return f.Close()
}

func (o *OS) NeedsReboot() (bool, error) {
	if _, err := os.Stat("/run/flightctl-needs-reboot"); err == nil {
		return true, nil
	} else if os.IsNotExist(err) {
		return false, nil
	} else {
		return false, err
	}
}

func (o *OS) GetImageState(ctx context.Context, osImage string) (*container.ImageState, error) {
	return o.podman.ImageState(ctx, osImage)
}

func (o *OS) PullImage(ctx context.Context, osImage string, fetcher container.ImageFetcher) error {
	imageState, err := o.GetImageState(ctx, osImage)
	if err != nil {
		return fmt.Errorf("getting image state: %w", err)
	}

	if imageState.Present {
		log.Infof("OS image %s already exists", osImage)
		return nil
	}
	return fetcher.Fetch(ctx, osImage, imageState.Digest, time.Minute*10, nil)
}

func (o *OS) StageImage(ctx context.Context, osImage string) error {
	return exec.Command("rpm-ostree", "rebase", "--experimental", fmt.Sprintf("ostree-unverified-registry:%s", osImage)).Run()
}
