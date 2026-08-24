package device

import (
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/store"
	devicestore "github.com/flightctl/flightctl/internal/store/device"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/samber/lo"
)

// applyRenderedUpdate mutates m for a successful render write, or returns
// store.ErrMutateSkipWrite when the rendered version should not advance.
func applyRenderedUpdate(
	m *devicestore.DeviceMutation,
	renderedConfig, renderedApplications, specHash, osImage string,
	configFingerprints []domain.DependencySyncConfigRefStatus,
	forceUpdate bool,
	osHints *RenderedOSHints,
) (renderedVersion string, err error) {
	device := m.Device
	ann := util.EnsureMap(lo.FromPtr(device.Metadata.Annotations))
	var deviceStatus *domain.DeviceStatus
	if device.Status != nil {
		deviceStatus = device.Status
	}
	next, err := domain.GetNextDeviceRenderedVersion(ann, deviceStatus)
	if err != nil {
		return "", err
	}
	specUnchanged := lo.HasKey(ann, domain.DeviceAnnotationRenderedSpecHash) &&
		ann[domain.DeviceAnnotationRenderedSpecHash] == specHash
	if specUnchanged && len(configFingerprints) == 0 && !forceUpdate {
		return "", store.ErrMutateSkipWrite
	}
	ann[domain.DeviceAnnotationRenderedVersion] = next
	if lo.HasKey(ann, domain.DeviceAnnotationTemplateVersion) {
		ann[domain.DeviceAnnotationRenderedTemplateVersion] = ann[domain.DeviceAnnotationTemplateVersion]
	}
	ann[domain.DeviceAnnotationRenderedSpecHash] = specHash
	device.Metadata.Annotations = &ann

	applyDependencySyncFingerprints(device, configFingerprints)
	if device.Status == nil {
		status := domain.NewDeviceStatus()
		device.Status = &status
	}
	domain.SetStatusCondition(&device.Status.Conditions, domain.Condition{
		Type:   domain.ConditionTypeDeviceSpecValid,
		Status: domain.ConditionStatusTrue,
		Reason: "Valid",
	})

	m.Rendered = &devicestore.DeviceRendered{
		Config:       renderedConfig,
		Applications: renderedApplications,
		OsImage:      osImage,
	}
	if osHints != nil {
		m.Rendered.OsDeltaImage = osHints.DeltaImage
		if osHints.UpdatedSize != nil {
			device.Status.Updated.Size = osHints.UpdatedSize
		}
	}
	return next, nil
}

func applyDependencySyncFingerprints(device *domain.Device, fingerprints []domain.DependencySyncConfigRefStatus) {
	if len(fingerprints) == 0 {
		return
	}
	now := time.Now()
	prevByName := map[string]domain.DependencySyncConfigRefStatus{}
	if device.Status != nil && device.Status.DependencySync != nil && device.Status.DependencySync.ConfigRefs != nil {
		for _, ref := range *device.Status.DependencySync.ConfigRefs {
			prevByName[ref.ConfigProviderName] = ref
		}
	}
	refs := make([]domain.DependencySyncConfigRefStatus, 0, len(fingerprints))
	for _, fp := range fingerprints {
		ref := domain.DependencySyncConfigRefStatus{
			ConfigProviderName: fp.ConfigProviderName,
			Fingerprint:        fp.Fingerprint,
		}
		if prev, ok := prevByName[fp.ConfigProviderName]; ok && prev.Fingerprint != nil && *prev.Fingerprint == lo.FromPtr(fp.Fingerprint) {
			ref.LastUpdatedAt = prev.LastUpdatedAt
		} else {
			ref.LastUpdatedAt = &now
		}
		refs = append(refs, ref)
	}
	if device.Status == nil {
		status := domain.NewDeviceStatus()
		device.Status = &status
	}
	device.Status.DependencySync = &domain.DependencySyncStatus{ConfigRefs: &refs}
}
