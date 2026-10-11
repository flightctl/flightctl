package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	deviceerrors "github.com/flightctl/flightctl/internal/agent/device/errors"
)

type podmanImageMount struct {
	ID         string `json:"id"`
	Mountpoint string `json:"mountpoint"`
}

type podmanOCIMount struct {
	Destination string   `json:"destination"`
	Source      string   `json:"source"`
	Options     []string `json:"options"`
}

// ContainerImageVolumes observes image-backed mounts of a running container.
// Image identity is anchored to the mounted image or backing storage container;
// inspecting a mutable volume reference could report a prepared update instead.
func (p *Podman) ContainerImageVolumes(ctx context.Context, containerID string) ([]v1beta1.ApplicationVolumeStatus, error) {
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	stdout, stderr, code := p.exec.ExecuteWithContext(ctx, podmanCmd, "container", "inspect", "--format", "{{json .}}", containerID)
	if code != 0 {
		return nil, fmt.Errorf("inspect container volumes: %w", deviceerrors.FromStderr(stderr, code))
	}
	var container struct {
		OCIConfigPath string
		State         struct {
			Running bool
			Pid     int
		}
		Mounts []struct{ Type, Name, Source, Destination, Driver string }
	}
	if err := json.Unmarshal([]byte(stdout), &container); err != nil {
		return nil, fmt.Errorf("parse container volumes: %w", err)
	}
	if !container.State.Running {
		return nil, nil
	}
	var volumes []v1beta1.ApplicationVolumeStatus
	var errs []error
	var mountedImages []podmanImageMount
	var runtimeMounts []podmanOCIMount
	for _, mount := range container.Mounts {
		if mount.Type == "image" && container.OCIConfigPath != "" {
			data, err := p.readWriter.ReadFile(container.OCIConfigPath)
			if err == nil {
				var config struct {
					Mounts []podmanOCIMount `json:"mounts"`
				}
				err = json.Unmarshal(data, &config)
				runtimeMounts = config.Mounts
			}
			if err != nil {
				errs = append(errs, fmt.Errorf("read effective container mounts: %w", err))
			} else {
				mountedImages, err = p.currentImageMounts(ctx)
				if err != nil {
					errs = append(errs, err)
				}
			}
			break
		}
	}
	for _, mount := range container.Mounts {
		switch {
		case mount.Type == "image":
			volume := v1beta1.ApplicationVolumeStatus{Name: mount.Destination, Reference: mount.Source}
			imageID := mountedImageID(mount.Destination, runtimeMounts, mountedImages)
			if imageID == "" && container.State.Pid > 0 && len(mountedImages) > 0 {
				// Podman can mount an overlay itself before binding it into the
				// container. In that case the OCI spec exposes the overlay path;
				// the running process's mount table exposes its actual lowerdir.
				data, err := p.readWriter.ReadFile(fmt.Sprintf("/proc/%d/mountinfo", container.State.Pid))
				if err == nil {
					imageID = mountedImageID(mount.Destination, imageVolumeMountInfo(data), mountedImages)
				}
			}
			if imageID != "" {
				var err error
				volume.Digest, err = p.PreparedImageDigest(ctx, imageID)
				if err != nil {
					errs = append(errs, err)
				}
			}
			volumes = append(volumes, volume)
		case mount.Type == "volume" && mount.Driver == "image":
			volume, err := p.currentNamedImageVolume(ctx, mount.Name)
			if err != nil {
				errs = append(errs, err)
			}
			if volume.Reference != "" {
				volumes = append(volumes, volume)
			}
		}
	}
	return volumes, errors.Join(errs...)
}

func imageVolumeMountInfo(data []byte) []podmanOCIMount {
	var mounts []podmanOCIMount
	unescape := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`)
	for _, line := range strings.Split(string(data), "\n") {
		before, after, ok := strings.Cut(line, " - ")
		if !ok {
			continue
		}
		fields, filesystem := strings.Fields(before), strings.Fields(after)
		if len(fields) < 5 || len(filesystem) < 3 {
			continue
		}
		mounts = append(mounts, podmanOCIMount{
			Destination: unescape.Replace(fields[4]), Source: unescape.Replace(filesystem[1]),
			Options: strings.Split(unescape.Replace(filesystem[2]), ","),
		})
	}
	return mounts
}

func (p *Podman) currentNamedImageVolume(ctx context.Context, name string) (v1beta1.ApplicationVolumeStatus, error) {
	volume := v1beta1.ApplicationVolumeStatus{Name: name}
	stdout, stderr, code := p.exec.ExecuteWithContext(ctx, podmanCmd, "volume", "inspect", "--format", "{{json .}}", name)
	if code != 0 {
		return volume, fmt.Errorf("inspect image volume: %w", deviceerrors.FromStderr(stderr, code))
	}
	var observed struct {
		StorageID string
		Options   map[string]string
	}
	if err := json.Unmarshal([]byte(stdout), &observed); err != nil {
		return volume, err
	}
	volume.Reference = observed.Options["image"]
	if observed.StorageID == "" {
		return volume, nil
	}
	// The volume's backing c/storage container retains its original image ID
	// even when the image option's tag now points to different content.
	stdout, stderr, code = p.exec.ExecuteWithContext(ctx, podmanCmd, "ps", "--all", "--external", "--no-trunc", "--filter", "id="+observed.StorageID, "--format", "{{.ImageID}}")
	if code != 0 {
		return volume, fmt.Errorf("inspect volume backing image: %w", deviceerrors.FromStderr(stderr, code))
	}
	var err error
	volume.Digest, err = p.PreparedImageDigest(ctx, strings.TrimSpace(stdout))
	return volume, err
}

// With no image arguments, image mount lists existing mounts without mounting
// an image. Rootless overlay storage requires entering its existing user namespace.
func (p *Podman) currentImageMounts(ctx context.Context) ([]podmanImageMount, error) {
	stdout, stderr, code := p.exec.ExecuteWithContext(ctx, podmanCmd, "image", "mount", "--format", "json")
	if code != 0 {
		stdout, stderr, code = p.exec.ExecuteWithContext(ctx, podmanCmd, "unshare", podmanCmd, "image", "mount", "--format", "json")
	}
	if code != 0 {
		return nil, fmt.Errorf("list mounted images: %w", deviceerrors.FromStderr(stderr, code))
	}
	var mounts []podmanImageMount
	if err := json.Unmarshal([]byte(stdout), &mounts); err != nil {
		return nil, fmt.Errorf("parse mounted images: %w", err)
	}
	return mounts, nil
}

func mountedImageID(destination string, runtimeMounts []podmanOCIMount, images []podmanImageMount) string {
	var result string
	for _, mount := range runtimeMounts {
		if mount.Destination != destination {
			continue
		}
		paths := []string{mount.Source}
		for _, option := range mount.Options {
			if lower, ok := strings.CutPrefix(option, "lowerdir="); ok {
				paths = append(paths, strings.Split(lower, ":")...)
			}
		}
		for _, image := range images {
			if image.Mountpoint == "" {
				continue
			}
			for _, path := range paths {
				relative, err := filepath.Rel(image.Mountpoint, path)
				if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
					continue
				}
				if result != "" && result != image.ID {
					return ""
				}
				result = image.ID
			}
		}
	}
	return result
}
