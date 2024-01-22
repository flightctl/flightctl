package controller

import (
	"context"
	"fmt"
	"os/user"
	"time"

	"github.com/containers/podman/v4/pkg/bindings"
	"github.com/containers/podman/v4/pkg/bindings/containers"
	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent"
	"k8s.io/klog/v2"
)

type ContainerController struct {
	agent *agent.DeviceAgent
}

func NewContainerController() *ContainerController {
	return &ContainerController{}
}

func (c *ContainerController) SetDeviceAgent(a *agent.DeviceAgent) {
	c.agent = a
}

func (c *ContainerController) NeedsUpdate(r *api.Device) bool {
	return false // this controller only updates status
}

func (c *ContainerController) StageUpdate(r *api.Device) (bool, error) {
	return true, nil // this controller only updates status
}

func (c *ContainerController) ApplyUpdate(r *api.Device) (bool, error) {
	return true, nil // this controller only updates status
}

func (c *ContainerController) FinalizeUpdate(r *api.Device) (bool, error) {
	return true, nil // this controller only updates status
}

func (c *ContainerController) SetStatus(r *api.Device) (bool, error) {
	if r == nil {
		return false, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	// Get the current user.
	currentUser, err := user.Current()
	if err != nil {
		klog.Errorf("Cannot get current user: %v", err)
		return false, err
	}

	// Set the socket path based on the user ID.
	var socketPath string
	if currentUser.Uid == "0" {
		socketPath = "unix:///run/podman/podman.sock"
	} else {
		socketPath = fmt.Sprintf("unix:///run/user/%s/podman/podman.sock", currentUser.Uid)
	}

	conn, err := bindings.NewConnection(ctx, socketPath)
	if err != nil {
		klog.Warningf("Warning: connection cannot be created: %v", err)
		return false, err
	} else {
		// Get a list of all containers
		all := true
		containerOptions := &containers.ListOptions{
			All: &all,
		}

		containers, err := containers.List(conn, containerOptions)
		if err != nil {
			klog.Errorf("Error getting containers: %v", err)
			return false, err
		}

		deviceContainerStatus := make([]api.ContainerStatus, len(containers))
		for i, c := range containers {
			deviceContainerStatus[i].Name = c.Names[0]
			deviceContainerStatus[i].Status = c.State
			deviceContainerStatus[i].Image = c.Image
			deviceContainerStatus[i].Id = c.ID
		}
		r.Status.Containers = &deviceContainerStatus

		return true, nil
	}
}
