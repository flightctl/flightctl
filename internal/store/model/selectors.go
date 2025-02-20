package model

import (
	"fmt"
	"strings"

	"github.com/flightctl/flightctl/internal/store/selector"
)

// Define additional custom selectors for various resources,
// including Device, Fleet, EnrollmentRequest, ResourceSync, Repository,
// and CertificateSigningRequest. Each resource is equipped with a set of selectors
// that map field paths to their corresponding types.
type selectorToTypeMap map[selector.SelectorName]selector.SelectorType

var (
	deviceStatusSelectors = selectorToTypeMap{
		selector.NewSelectorName("status.summary.status"):             selector.String,
		selector.NewSelectorName("status.applicationsSummary.status"): selector.String,
		selector.NewSelectorName("status.updated.status"):             selector.String,
		selector.NewSelectorName("status.lastSeen"):                   selector.Timestamp,
		selector.NewSelectorName("status.lifecycle.status"):           selector.String,
	}
	fleetSpecSelectors = selectorToTypeMap{
		selector.NewSelectorName("spec.template.spec.os.image"): selector.String,
	}
	enrollmentRequestStatusSelectors = selectorToTypeMap{
		selector.NewSelectorName("status.approval.approved"): selector.Bool,
		selector.NewSelectorName("status.certificate"):       selector.String,
	}
	resourceSyncSpecSelectors = selectorToTypeMap{
		selector.NewSelectorName("spec.repository"): selector.String,
	}
	repositorySpecSelectors = selectorToTypeMap{
		selector.NewSelectorName("spec.type"): selector.String,
		selector.NewSelectorName("spec.url"):  selector.String,
	}
	certificateSigningRequestStatusSelectors = selectorToTypeMap{
		selector.NewSelectorName("status.certificate"): selector.String,
	}
)

func (m *Device) MapSelectorName(name selector.SelectorName) []selector.SelectorName {
	if strings.EqualFold("metadata.nameOrAlias", name.String()) {
		return []selector.SelectorName{
			selector.NewSelectorName("metadata.name"),
			selector.NewSelectorName("metadata.alias"),
		}
	}
	return nil
}

func (m *Device) ResolveSelector(name selector.SelectorName) (*selector.SelectorField, error) {
	if typ, exists := deviceStatusSelectors[name]; exists {
		return &selector.SelectorField{
			Type:      typ,
			FieldName: name.String(),
			FieldType: "jsonb",
		}, nil
	}
	return nil, fmt.Errorf("unable to resolve selector for device")
}

func (m *Device) ListSelectors() selector.SelectorNameSet {
	keys := make([]selector.SelectorName, 0, len(deviceStatusSelectors))
	for sn := range deviceStatusSelectors {
		keys = append(keys, sn)
	}
	return selector.NewSelectorFieldNameSet().Add(selector.NewSelectorName("metadata.nameOrAlias")).Add(keys...)
}

func (m *DeviceLabel) MapSelectorName(name selector.SelectorName) []selector.SelectorName {
	if strings.EqualFold("metadata.label.keyOrValue", name.String()) {
		return []selector.SelectorName{
			selector.NewSelectorName("metadata.label.key"),
			selector.NewSelectorName("metadata.label.value"),
		}
	}
	return nil
}

func (m *DeviceLabel) ListSelectors() selector.SelectorNameSet {
	return selector.NewSelectorFieldNameSet().Add(selector.NewSelectorName("metadata.label.keyOrValue"))
}

func (m *Fleet) ResolveSelector(name selector.SelectorName) (*selector.SelectorField, error) {
	if typ, exists := fleetSpecSelectors[name]; exists {
		return &selector.SelectorField{
			Type:      typ,
			FieldName: name.String(),
			FieldType: "jsonb",
		}, nil
	}
	return nil, fmt.Errorf("unable to resolve selector for fleet")
}

func (m *Fleet) ListSelectors() selector.SelectorNameSet {
	keys := make([]selector.SelectorName, 0, len(fleetSpecSelectors))
	for sn := range fleetSpecSelectors {
		keys = append(keys, sn)
	}
	return selector.NewSelectorFieldNameSet().Add(keys...)
}

func (m *EnrollmentRequest) ResolveSelector(name selector.SelectorName) (*selector.SelectorField, error) {
	if typ, exists := enrollmentRequestStatusSelectors[name]; exists {
		return &selector.SelectorField{
			Type:      typ,
			FieldName: name.String(),
			FieldType: "jsonb",
		}, nil
	}
	return nil, fmt.Errorf("unable to resolve selector for enrollment request")
}

func (m *EnrollmentRequest) ListSelectors() selector.SelectorNameSet {
	keys := make([]selector.SelectorName, 0, len(enrollmentRequestStatusSelectors))
	for sn := range enrollmentRequestStatusSelectors {
		keys = append(keys, sn)
	}
	return selector.NewSelectorFieldNameSet().Add(keys...)
}

func (m *ResourceSync) ResolveSelector(name selector.SelectorName) (*selector.SelectorField, error) {
	if typ, exists := resourceSyncSpecSelectors[name]; exists {
		return &selector.SelectorField{
			Type:      typ,
			FieldName: name.String(),
			FieldType: "jsonb",
		}, nil
	}
	return nil, fmt.Errorf("unable to resolve selector for resource sync")
}

func (m *ResourceSync) ListSelectors() selector.SelectorNameSet {
	keys := make([]selector.SelectorName, 0, len(resourceSyncSpecSelectors))
	for sn := range resourceSyncSpecSelectors {
		keys = append(keys, sn)
	}
	return selector.NewSelectorFieldNameSet().Add(keys...)
}

func (m *Repository) ResolveSelector(name selector.SelectorName) (*selector.SelectorField, error) {
	if typ, exists := repositorySpecSelectors[name]; exists {
		return &selector.SelectorField{
			Type:      typ,
			FieldName: name.String(),
			FieldType: "jsonb",
		}, nil
	}
	return nil, fmt.Errorf("unable to resolve selector for repository")
}

func (m *Repository) ListSelectors() selector.SelectorNameSet {
	keys := make([]selector.SelectorName, 0, len(repositorySpecSelectors))
	for sn := range repositorySpecSelectors {
		keys = append(keys, sn)
	}
	return selector.NewSelectorFieldNameSet().Add(keys...)
}

func (m *CertificateSigningRequest) ResolveSelector(name selector.SelectorName) (*selector.SelectorField, error) {
	if typ, exists := certificateSigningRequestStatusSelectors[name]; exists {
		return &selector.SelectorField{
			Type:      typ,
			FieldName: name.String(),
			FieldType: "jsonb",
		}, nil
	}
	return nil, fmt.Errorf("unable to resolve selector for certificate signing request")
}

func (m *CertificateSigningRequest) ListSelectors() selector.SelectorNameSet {
	keys := make([]selector.SelectorName, 0, len(certificateSigningRequestStatusSelectors))
	for sn := range certificateSigningRequestStatusSelectors {
		keys = append(keys, sn)
	}
	return selector.NewSelectorFieldNameSet().Add(keys...)
}
