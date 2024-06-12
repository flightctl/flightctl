package bootimage

import (
	"github.com/flightctl/flightctl/api/v1alpha1"
)

// Most types ported from bootc
// https://github.com/containers/bootc/blob/main/lib/src/spec.rs

type Deployment interface {
	RemoveRollback() error
}

type BootcHost struct {
	APIVersion string     `json:"apiVersion"`
	Kind       string     `json:"kind"`
	Metadata   Metadata   `json:"metadata"`
	Spec       HostSpec   `json:"spec"`
	Status     HostStatus `json:"status"`
}

type Metadata struct {
	Name string `json:"name"`
}

// The core host definition
type HostSpec struct {
	Image ImageReference `json:"image"`
}

type ImageReference struct {
	// The container image reference
	Image string `json:"image"`
	// The container image transport
	Transport string `json:"transport"`
}

// The status of the host system
type HostStatus struct {
	// The staged image for the next boot
	Staged BootEntry `json:"staged,omitempty"`
	// The booted image; this will be unset if the host is not bootc compatible.
	Booted BootEntry `json:"booted,omitempty"`
	// The previously booted image
	Rollback BootEntry `json:"rollback,omitempty"`
	// Set to true if the rollback entry is queued for the next boot.
	RollbackQueued bool `json:"rollbackQueued"`
	// The detected type of system
	Type string `json:"type"`
}

type BootEntry struct {
	// The image reference
	Image ImageStatus `json:"image,omitempty"`
	// The last fetched cached update metadata
	CachedUpdate ImageStatus `json:"cachedUpdate,omitempty"`
	// Whether this boot entry is not compatible (has origin changes bootc does not understand)
	Incompatible bool `json:"incompatible"`
	// Whether this entry will be subject to garbage collection
	Pinned bool `json:"pinned"`
	// If this boot entry is ostree based, the corresponding state
	Ostree BootEntryOstree `json:"ostree,omitempty"`
}

type ImageStatus struct {
	/// The currently booted image
	Image ImageReference `json:"image"`
	// The version string, if any
	Version string `json:"version,omitempty"`
	// The build timestamp, if any
	Timestamp string `json:"timestamp,omitempty"`
	// The digest of the fetched image (e.g. sha256:a0...);
	ImageDigest string `json:"imageDigest"`
}

type BootEntryOstree struct {
	// The ostree commit checksum
	Checksum string `json:"checksum"`
	// The deployment serial
	DeploySerial int `json:"deploySerial"`
}

// StagedImage returns the staged image
func (s *HostStatus) StagedImage() string {
	return s.Staged.Image.Image.Image
}

// BootedImage returns the booted image
func (s *HostStatus) BootedImage() string {
	return s.Booted.Image.Image.Image
}

// RollbackImage returns the rollback image
func (s *HostStatus) RollbackImage() string {
	return s.Rollback.Image.Image.Image
}

// IsSupported checks if the status.Type is supported by the given manager
func (s *HostStatus) IsImageManagerSupported(manager ImageManager) bool {
	switch ImageManager(s.Type) {
	case ImageManagerBootc:
		return manager == ImageManagerBootc
	case ImageManagerRpmOsTree:
		return manager == ImageManagerRpmOsTree
	default:
		return false
	}
}

// IsBootedImageExpected returns true if the booted image as observed by the
// underlying image manager equals the desired spec os image.
func (s *HostStatus) IsBootedImageExpected(expected *v1alpha1.RenderedDeviceSpec) bool {
	return s.BootedImage() == expected.Os.Image
}
