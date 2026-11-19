//go:build linux

package packagemodemigration_test

import (
	"testing"

	v1beta1 "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
)

func TestDesiredRenderedVersion(t *testing.T) {
	tests := []struct {
		name    string
		device  *v1beta1.Device
		want    int
		wantErr string
	}{
		{name: "nil device", device: nil, wantErr: "has no annotations"},
		{name: "nil annotations", device: &v1beta1.Device{Metadata: v1beta1.ObjectMeta{Annotations: nil}}, wantErr: "has no annotations"},
		{name: "missing annotation", device: &v1beta1.Device{Metadata: v1beta1.ObjectMeta{Annotations: lo.ToPtr(map[string]string{"other": "val"})}}, wantErr: v1beta1.DeviceAnnotationRenderedVersion},
		{name: "empty annotation", device: &v1beta1.Device{Metadata: v1beta1.ObjectMeta{Annotations: lo.ToPtr(map[string]string{v1beta1.DeviceAnnotationRenderedVersion: ""})}}, wantErr: v1beta1.DeviceAnnotationRenderedVersion},
		{name: "whitespace annotation", device: &v1beta1.Device{Metadata: v1beta1.ObjectMeta{Annotations: lo.ToPtr(map[string]string{v1beta1.DeviceAnnotationRenderedVersion: "   "})}}, wantErr: "invalid"},
		{name: "non-numeric annotation", device: &v1beta1.Device{Metadata: v1beta1.ObjectMeta{Annotations: lo.ToPtr(map[string]string{v1beta1.DeviceAnnotationRenderedVersion: "abc"})}}, wantErr: "invalid"},
		{name: "zero version", device: &v1beta1.Device{Metadata: v1beta1.ObjectMeta{Annotations: lo.ToPtr(map[string]string{v1beta1.DeviceAnnotationRenderedVersion: "0"})}}, wantErr: "non-positive"},
		{name: "negative version", device: &v1beta1.Device{Metadata: v1beta1.ObjectMeta{Annotations: lo.ToPtr(map[string]string{v1beta1.DeviceAnnotationRenderedVersion: "-5"})}}, wantErr: "non-positive"},
		{name: "valid version", device: &v1beta1.Device{Metadata: v1beta1.ObjectMeta{Annotations: lo.ToPtr(map[string]string{v1beta1.DeviceAnnotationRenderedVersion: "42"})}}, want: 42},
		{name: "padded version", device: &v1beta1.Device{Metadata: v1beta1.ObjectMeta{Annotations: lo.ToPtr(map[string]string{v1beta1.DeviceAnnotationRenderedVersion: " 7 "})}}, want: 7},
		{name: "overflow decimal", device: &v1beta1.Device{Metadata: v1beta1.ObjectMeta{Annotations: lo.ToPtr(map[string]string{v1beta1.DeviceAnnotationRenderedVersion: "99999999999999999999"})}}, wantErr: "invalid"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := desiredRenderedVersion(tt.device)
			if tt.wantErr != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestDesiredRenderedVersionIsNotReportedVersion(t *testing.T) {
	device := &v1beta1.Device{
		Metadata: v1beta1.ObjectMeta{
			Annotations: lo.ToPtr(map[string]string{v1beta1.DeviceAnnotationRenderedVersion: "5"}),
		},
		Status: &v1beta1.DeviceStatus{
			Updated: v1beta1.DeviceUpdatedStatus{Status: v1beta1.DeviceUpdatedStatusOutOfDate},
			Config:  v1beta1.DeviceConfigStatus{RenderedVersion: "3"},
		},
	}
	desired, err := desiredRenderedVersion(device)
	require.NoError(t, err)
	require.Equal(t, 5, desired, "desired version must be the annotation, not the reported status")
	require.NotEqual(t, device.Status.Config.RenderedVersion, "5",
		"test fixture: reported version must differ from desired to simulate OutOfDate")
}
