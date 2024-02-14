package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/pkg/errors"
	"k8s.io/klog/v2"
)

type ContainerController struct {
	exec executer.Executer
}

func NewContainerController() *ContainerController {
	return &ContainerController{
		exec: &executer.CommonExecuter{},
	}
}

func NewContainerControllerWithExecuter(exec executer.Executer) *ContainerController {
	return &ContainerController{
		exec: exec,
	}
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

type PodmanList []PodmanListEntry
type PodmanListEntry struct {
	Names []string `json:"Names"`
	State string   `json:"State"`
	Image string   `json:"Image"`
	Id    string   `json:"Id"`
}

type Shell interface {
	Command(cmd string) (output []byte, err error)
}

func (c *ContainerController) SetStatus(r *api.Device) (bool, error) {
	if r == nil {
		return false, nil
	}

	execCtx, cancel := context.WithTimeout(context.TODO(), 2*time.Minute)
	defer cancel()
	out, errOut, exitCode := c.exec.ExecuteWithContext(execCtx, "/usr/bin/podman", "ps", "-a", "--format", "json")
	if exitCode != 0 {
		msg := fmt.Sprintf("listing podman containers failed with code %d: %s\n", exitCode, errOut)
		err := errors.Errorf(msg)
		klog.Errorf(msg)
		return false, err
	}

	var containers PodmanList
	if err := json.Unmarshal([]byte(out), &containers); err != nil {
		klog.Errorf("error unmarshalling podman list output: %s\n", err)
		return false, err
	}

	deviceContainerStatus := make([]api.ContainerStatus, len(containers))
	for i, c := range containers {
		deviceContainerStatus[i].Name = c.Names[0]
		deviceContainerStatus[i].Status = c.State
		deviceContainerStatus[i].Image = c.Image
		deviceContainerStatus[i].Id = c.Id
	}
	r.Status.Containers = &deviceContainerStatus

	return true, nil
}
