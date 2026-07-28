package kvstore

import (
	"crypto/md5" //nolint: gosec
	"crypto/sha256"
	"fmt"

	"github.com/google/uuid"
)

type TemplateVersionKey struct {
	OrgID           uuid.UUID
	Fleet           string
	TemplateVersion string
}

func (k *TemplateVersionKey) ComposeKey() string {
	return fmt.Sprintf("v1/%s/%s/%s/", k.OrgID, k.Fleet, k.TemplateVersion)
}

type RepositoryUrlKey struct {
	OrgID           uuid.UUID
	Fleet           string
	TemplateVersion string
	Repository      string
}

func (k *RepositoryUrlKey) ComposeKey() string {
	return fmt.Sprintf("v1/%s/%s/%s/repo-url/%s", k.OrgID, k.Fleet, k.TemplateVersion, k.Repository)
}

type GitRevisionKey struct {
	OrgID           uuid.UUID
	Fleet           string
	TemplateVersion string
	Repository      string
	TargetRevision  string
}

func (k *GitRevisionKey) ComposeKey() string {
	return fmt.Sprintf("v1/%s/%s/%s/git-hash/%s/%s", k.OrgID, k.Fleet, k.TemplateVersion, k.Repository, k.TargetRevision)
}

type GitContentsKey struct {
	OrgID           uuid.UUID
	Fleet           string
	TemplateVersion string
	Repository      string
	TargetRevision  string
	Path            string
}

func (k *GitContentsKey) ComposeKey() string {
	return fmt.Sprintf("v1/%s/%s/%s/git-data/%s/%s/%s", k.OrgID, k.Fleet, k.TemplateVersion, k.Repository, k.TargetRevision, k.Path)
}

type K8sSecretKey struct {
	OrgID           uuid.UUID
	Fleet           string
	TemplateVersion string
	Namespace       string
	Name            string
}

func (k *K8sSecretKey) ComposeKey() string {
	return fmt.Sprintf("v1/%s/%s/%s/k8ssecret-data/%s/%s", k.OrgID, k.Fleet, k.TemplateVersion, k.Namespace, k.Name)
}

type HttpKey struct {
	OrgID           uuid.UUID
	Fleet           string
	TemplateVersion string
	URL             string
}

func (k *HttpKey) ComposeKey() string {
	md5sum := md5.Sum([]byte(k.URL)) //nolint: gosec
	return fmt.Sprintf("v1/%s/%s/%s/http-data/%x", k.OrgID, k.Fleet, k.TemplateVersion, md5sum)
}

// HttpFingerprintKey stores the ETag/Last-Modified fingerprint alongside the
// cached HTTP body so stale entries can be detected on DependencyChangeDetected
// without changing the HttpKey format.
type HttpFingerprintKey struct {
	OrgID           uuid.UUID
	Fleet           string
	TemplateVersion string
	URL             string
}

func (k *HttpFingerprintKey) ComposeKey() string {
	md5sum := md5.Sum([]byte(k.URL)) //nolint: gosec
	return fmt.Sprintf("v1/%s/%s/%s/http-fingerprint/%x", k.OrgID, k.Fleet, k.TemplateVersion, md5sum)
}

type DeviceKey struct {
	OrgID      uuid.UUID
	DeviceName string
}

func (d *DeviceKey) ComposeKey() string {
	return fmt.Sprintf("v1/%s/device/%s", d.OrgID, d.DeviceName)
}

type AwaitingReconnectionKey struct {
	OrgID      uuid.UUID
	DeviceName string
}

func (a *AwaitingReconnectionKey) ComposeKey() string {
	return fmt.Sprintf("v1/%s/device/%s/awaiting-reconnect", a.OrgID, a.DeviceName)
}

// VmQuadletFilesKey is a content-addressed KV key for caching the Quadlet unit
// files produced by vm-to-quadlet. The key is global (not scoped to
// org/fleet/templateVersion) because the conversion is deterministic: the same
// vm.yaml input and render options always produce the same Quadlet files. The
// cached value is a JSON-encoded map[string]string (filename → content).
type VmQuadletFilesKey struct{}

func (k *VmQuadletFilesKey) ComposeKey(vmYAML []byte, launcherImage string, passtWorkarounds bool) string {
	h := sha256.New()
	_, _ = h.Write(vmYAML)
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(launcherImage))
	_, _ = h.Write([]byte{0})
	if passtWorkarounds {
		_, _ = h.Write([]byte("passt=1"))
	} else {
		_, _ = h.Write([]byte("passt=0"))
	}
	return fmt.Sprintf("v1/vm-quadlet-files/%x", h.Sum(nil))
}
