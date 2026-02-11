package v1beta1

import (
	apiv1beta1 "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/domain"
)

// DeviceConverter converts between v1beta1 API types and domain types for Device resources.
type DeviceConverter interface {
	// Core resource conversions
	ToDomain(apiv1beta1.Device) domain.Device
	FromDomain(*domain.Device) *apiv1beta1.Device
	ListFromDomain(*domain.DeviceList) *apiv1beta1.DeviceList

	// Operation types
	DecommissionToDomain(apiv1beta1.DeviceDecommission) domain.DeviceDecommission
	ResumeRequestToDomain(apiv1beta1.DeviceResumeRequest) domain.DeviceResumeRequest
	ResumeResponseFromDomain(domain.DeviceResumeResponse) apiv1beta1.DeviceResumeResponse
	LastSeenFromDomain(*domain.DeviceLastSeen) *apiv1beta1.DeviceLastSeen

	// Params conversions
	ListParamsToDomain(apiv1beta1.ListDevicesParams) domain.ListDevicesParams
	GetRenderedParamsToDomain(apiv1beta1.GetRenderedDeviceParams) domain.GetRenderedDeviceParams
}

type deviceConverter struct{}

// NewDeviceConverter creates a new DeviceConverter.
func NewDeviceConverter() DeviceConverter {
	return &deviceConverter{}
}

func (c *deviceConverter) ToDomain(d apiv1beta1.Device) domain.Device {
	return d
}

func (c *deviceConverter) FromDomain(d *domain.Device) *apiv1beta1.Device {
	return d
}

func (c *deviceConverter) ListFromDomain(l *domain.DeviceList) *apiv1beta1.DeviceList {
	return l
}

func (c *deviceConverter) DecommissionToDomain(d apiv1beta1.DeviceDecommission) domain.DeviceDecommission {
	return d
}

func (c *deviceConverter) ResumeRequestToDomain(r apiv1beta1.DeviceResumeRequest) domain.DeviceResumeRequest {
	return r
}

func (c *deviceConverter) ResumeResponseFromDomain(r domain.DeviceResumeResponse) apiv1beta1.DeviceResumeResponse {
	return r
}

func (c *deviceConverter) LastSeenFromDomain(l *domain.DeviceLastSeen) *apiv1beta1.DeviceLastSeen {
	return l
}

func (c *deviceConverter) ListParamsToDomain(p apiv1beta1.ListDevicesParams) domain.ListDevicesParams {
	return p
}

func (c *deviceConverter) GetRenderedParamsToDomain(p apiv1beta1.GetRenderedDeviceParams) domain.GetRenderedDeviceParams {
	return p
}
