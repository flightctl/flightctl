package conversion

import (
	"encoding/json"

	v1 "github.com/flightctl/flightctl/api/v1"
	"github.com/flightctl/flightctl/api/v1beta1"
)

// DeviceV1ToV1Beta1 converts a v1 Device to a v1beta1 Device.
// Since the schemas are structurally identical, we use JSON marshaling.
func DeviceV1ToV1Beta1(device v1.Device) (v1beta1.Device, error) {
	data, err := json.Marshal(device)
	if err != nil {
		return v1beta1.Device{}, err
	}
	var result v1beta1.Device
	if err := json.Unmarshal(data, &result); err != nil {
		return v1beta1.Device{}, err
	}
	return result, nil
}

// DeviceV1Beta1ToV1 converts a v1beta1 Device to a v1 Device.
func DeviceV1Beta1ToV1(device v1beta1.Device) (v1.Device, error) {
	data, err := json.Marshal(device)
	if err != nil {
		return v1.Device{}, err
	}
	var result v1.Device
	if err := json.Unmarshal(data, &result); err != nil {
		return v1.Device{}, err
	}
	return result, nil
}

// DeviceListV1Beta1ToV1 converts a v1beta1 DeviceList to a v1 DeviceList.
func DeviceListV1Beta1ToV1(list v1beta1.DeviceList) (v1.DeviceList, error) {
	data, err := json.Marshal(list)
	if err != nil {
		return v1.DeviceList{}, err
	}
	var result v1.DeviceList
	if err := json.Unmarshal(data, &result); err != nil {
		return v1.DeviceList{}, err
	}
	return result, nil
}

// ListDevicesParamsV1ToV1Beta1 converts v1 ListDevicesParams to v1beta1 ListDevicesParams.
func ListDevicesParamsV1ToV1Beta1(params v1.ListDevicesParams) v1beta1.ListDevicesParams {
	return v1beta1.ListDevicesParams{
		Continue:      params.Continue,
		LabelSelector: params.LabelSelector,
		FieldSelector: params.FieldSelector,
		Limit:         params.Limit,
		SummaryOnly:   params.SummaryOnly,
	}
}

// StatusV1Beta1ToV1 converts a v1beta1 Status to a v1 Status.
func StatusV1Beta1ToV1(status v1beta1.Status) v1.Status {
	code := status.Code
	message := status.Message
	reason := status.Reason
	apiVersion := status.ApiVersion
	kind := status.Kind
	return v1.Status{
		Code:       &code,
		Message:    &message,
		Reason:     &reason,
		ApiVersion: &apiVersion,
		Kind:       &kind,
	}
}
