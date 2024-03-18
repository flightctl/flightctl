package container

import (
	"context"
	"fmt"

	"github.com/flightctl/flightctl/pkg/executer"
	"gopkg.in/yaml.v3"
)

const (
	CmdBootc = "bootc"
)

type BootcCmd struct {
	executer executer.Executer
}

type BootcHost struct {
	APIVersion string   `json:"apiVersion"`
	Kind       string   `json:"kind"`
	Metadata   Metadata `json:"metadata"`
	Spec       Spec     `json:"spec"`
	Status     Status   `json:"status"`
}

type Metadata struct {
	Name string `json:"name"`
}

type Spec struct {
	Image ImageSpec `json:"image"`
}

type ImageSpec struct {
	Image     string `json:"image"`
	Transport string `json:"transport"`
}

type Status struct {
	Staged   ImageStatus `json:"staged"`
	Booted   ImageStatus `json:"booted"`
	Rollback ImageStatus `json:"rollback"`
	Type     string      `json:"type"`
}

type ImageStatus struct {
	Image        ImageDetails  `json:"image"`
	CachedUpdate *bool         `json:"cachedUpdate"`
	Incompatible bool          `json:"incompatible"`
	Pinned       bool          `json:"pinned"`
	Ostree       OstreeDetails `json:"ostree"`
}

type ImageDetails struct {
	Image       ImageSpec `json:"image"`
	Version     string    `json:"version"`
	Timestamp   string    `json:"timestamp"`
	ImageDigest string    `json:"imageDigest"`
}

type OstreeDetails struct {
	Checksum     string `json:"checksum"`
	DeploySerial int    `json:"deploySerial"`
}

// NewBootcCmd creates a new bootc command.
func NewBootcCmd(executer executer.Executer) *BootcCmd {
	return &BootcCmd{
		executer: executer,
	}
}

// Status returns the current bootc host status.
func (b *BootcCmd) Status(ctx context.Context) (*BootcHost, error) {
	args := []string{"status", "--json"}
	stdout, stderr, exitCode := b.executer.ExecuteWithContext(ctx, CmdBootc, args...)
	if exitCode != 0 {
		return nil, fmt.Errorf("get bootc status: %s", stderr)
	}

	var bootcHost BootcHost
	if err := yaml.Unmarshal([]byte(stdout), &bootcHost); err != nil {
		return nil, fmt.Errorf("unmarshalling config file: %w", err)
	}

	return &bootcHost, nil
}

// Switch pulls the specified image and stages it for the next boot while retaining a copy of the most recently booted image.
func (b *BootcCmd) Switch(ctx context.Context, image string) error {
	args := []string{"switch", "--retain", image}
	_, stderr, exitCode := b.executer.ExecuteWithContext(ctx, CmdBootc, args...)
	if exitCode != 0 {
		return fmt.Errorf("stage image: %s", stderr)
	}
	return nil
}

// Apply restart or reboot into the new target image.
func (b *BootcCmd) Apply(ctx context.Context) error {
	args := []string{"upgrade", "--apply"}
	_, stderr, exitCode := b.executer.ExecuteWithContext(ctx, CmdBootc, args...)
	if exitCode != 0 {
		return fmt.Errorf("apply image: %s", stderr)
	}
	return nil
}

// UsrOverlay adds a transient writable overlayfs on `/usr` that will be discarded on reboot.
func (b *BootcCmd) UsrOverlay(ctx context.Context) error {
	args := []string{"usr-overlay"}
	_, stderr, exitCode := b.executer.ExecuteWithContext(ctx, CmdBootc, args...)
	if exitCode != 0 {
		return fmt.Errorf("overlay image: %s", stderr)
	}
	return nil
}
