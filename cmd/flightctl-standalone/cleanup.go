package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/flightctl/flightctl/internal/quadlet"
	"github.com/flightctl/flightctl/internal/quadlet/renderer"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/podman"
	"github.com/spf13/cobra"
)

const (
	// Timeout for waiting for containers to be removed by systemd
	containerRemoveTimeout = 60 * time.Second
	// Polling interval for checking container status
	containerPollInterval = 2 * time.Second
)

type CleanupOptions struct {
	AcceptPrompt bool
}

func NewCleanupCommand() *cobra.Command {
	opts := &CleanupOptions{}

	cmd := &cobra.Command{
		Use:   "cleanup",
		Short: "Clean up Flight Control services and artifacts",
		Long: `Stop and disable Flight Control services, then remove all associated
podman artifacts (containers, images, volumes, networks, secrets).

This operation is destructive - use with caution.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return opts.Run()
		},
	}

	cmd.Flags().BoolVarP(&opts.AcceptPrompt, "yes", "y", false, "Skip confirmation prompt")

	return cmd
}

func (o *CleanupOptions) Run() error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("cleanup requires root privileges, please run with sudo")
	}

	if !o.AcceptPrompt {
		if !confirmCleanup() {
			fmt.Println("Cleanup cancelled.")
			return nil
		}
	}

	fmt.Println("Starting Flight Control cleanup...")

	exec := &executer.CommonExecuter{}
	podmanClient := podman.NewClient(exec)
	ctx := context.Background()

	if err := stopServices(ctx, exec); err != nil {
		fmt.Printf("Warning: Failed to stop services: %v\n", err)
	}

	if err := disableTarget(ctx, exec); err != nil {
		fmt.Printf("Warning: Failed to disable target: %v\n", err)
	}

	if err := waitForContainersToRemove(ctx, podmanClient); err != nil {
		fmt.Printf("Warning: Failed waiting for containers to stop: %v\n", err)
	}

	if err := removeImages(ctx, podmanClient); err != nil {
		fmt.Printf("Warning: Failed to remove images: %v\n", err)
	}

	if err := removeVolumes(ctx, podmanClient); err != nil {
		fmt.Printf("Warning: Failed to remove volumes: %v\n", err)
	}

	if err := removeSecrets(ctx, podmanClient); err != nil {
		fmt.Printf("Warning: Failed to remove secrets: %v\n", err)
	}

	if err := removeNetwork(ctx, podmanClient); err != nil {
		fmt.Printf("Warning: Failed to remove network: %v\n", err)
	}

	fmt.Println("Cleanup completed.")
	return nil
}

func confirmCleanup() bool {
	fmt.Println("WARNING: This will remove all Flight Control services and data.")
	fmt.Println("This operation cannot be undone.")
	fmt.Print("Are you sure you want to continue? [y/N]: ")

	reader := bufio.NewReader(os.Stdin)
	response, err := reader.ReadString('\n')
	if err != nil {
		return false
	}

	response = strings.TrimSpace(strings.ToLower(response))
	return response == "y" || response == "yes"
}

func stopServices(ctx context.Context, exec executer.Executer) error {
	fmt.Println("Stopping Flight Control services...")

	_, stdout, exitCode := exec.ExecuteWithContext(ctx, "systemctl", "stop", renderer.SystemdTargetName)
	if exitCode != 0 {
		return fmt.Errorf("systemctl stop %s: %s\n", renderer.SystemdTargetName, stdout)
	}

	fmt.Println("Services stopped")
	return nil
}

func disableTarget(ctx context.Context, exec executer.Executer) error {
	fmt.Println("Disabling Flight Control target...")

	_, stdout, exitCode := exec.ExecuteWithContext(ctx, "systemctl", "disable", renderer.SystemdTargetName)
	if exitCode != 0 {
		return fmt.Errorf("systemctl disable %s: %s\n", renderer.SystemdTargetName, stdout)
	}

	fmt.Println("Target disabled")
	return nil
}

func waitForContainersToRemove(ctx context.Context, client *podman.Client) error {
	fmt.Println("Waiting for Flight Control containers to be removed...")

	deadline := time.Now().Add(containerRemoveTimeout)
	for {
		containers, err := client.ListContainers(ctx, podman.ListContainersOptions{
			All:    true,
			Filter: "name=flightctl-",
		})
		if err != nil {
			return fmt.Errorf("failed to list containers: %w", err)
		}

		if len(containers) == 0 {
			fmt.Println("All containers removed")
			return nil
		}

		if time.Now().After(deadline) {
			return fmt.Errorf("timeout waiting for containers to be removed: %v", containers)
		}

		time.Sleep(containerPollInterval)
	}
}

func removeImages(ctx context.Context, client *podman.Client) error {
	fmt.Println("Removing Flight Control images...")

	// Find all .container files in the quadlet directory
	pattern := filepath.Join(renderer.DefaultQuadletDir, "flightctl*.container")
	containerFiles, err := filepath.Glob(pattern)
	if err != nil {
		return fmt.Errorf("failed to glob container files: %w", err)
	}

	// Track unique images to avoid duplicate removal attempts
	imageSet := make(map[string]struct{})

	for _, containerFile := range containerFiles {
		content, err := os.ReadFile(containerFile)
		if err != nil {
			fmt.Printf("Warning: Failed to read container file %s: %v\n", containerFile, err)
			continue
		}

		unit, err := quadlet.NewUnit(content)
		if err != nil {
			fmt.Printf("Warning: Failed to parse container file %s: %v\n", containerFile, err)
			continue
		}

		image, err := unit.GetImage()
		if err != nil {
			continue
		}

		imageSet[image] = struct{}{}
	}

	// Remove each unique image using the podman client
	for image := range imageSet {
		fmt.Printf("Removing image: %s\n", image)
		err := client.RemoveImage(ctx, image)
		if err != nil {
			if errors.Is(err, podman.ErrImageDoesNotExist) {
				continue
			}
			fmt.Printf("Warning: Failed to remove image %s (may be in use): %v\n", image, err)
		}
	}

	return nil
}

func removeVolumes(ctx context.Context, client *podman.Client) error {
	fmt.Println("Removing Flight Control volumes...")

	for _, volume := range renderer.KnownVolumes {
		fmt.Printf("Removing volume: %s\n", volume)
		err := client.RemoveVolume(ctx, volume)
		if err != nil {
			if errors.Is(err, podman.ErrVolumeDoesNotExist) {
				continue
			}
			fmt.Printf("Warning: Failed to remove volume %s: %v\n", volume, err)
		}
	}

	return nil
}

func removeNetwork(ctx context.Context, client *podman.Client) error {
	fmt.Println("Removing Flight Control network...")

	fmt.Printf("Removing network: %s\n", renderer.PodmanNetworkName)
	err := client.RemoveNetwork(ctx, renderer.PodmanNetworkName)
	if err != nil {
		if errors.Is(err, podman.ErrNetworkDoesNotExist) {
			return nil
		}
		fmt.Printf("Warning: Failed to remove network %s: %v\n", renderer.PodmanNetworkName, err)
		return err
	}

	return nil
}

func removeSecrets(ctx context.Context, client *podman.Client) error {
	fmt.Println("Removing Flight Control secrets...")

	for _, secret := range renderer.KnownSecrets {
		fmt.Printf("Removing secret: %s\n", secret)
		err := client.RemoveSecret(ctx, secret, podman.RemoveSecretOptions{
			Ignore: true,
		})
		if err != nil {
			fmt.Printf("Warning: Failed to remove secret %s: %v\n", secret, err)
		}
	}

	return nil
}
