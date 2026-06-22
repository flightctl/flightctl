package status

import (
	"context"
	"fmt"
	"testing"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestMergeContribution(t *testing.T) {
	testCases := []struct {
		name     string
		initial  func() *v1beta1.DeviceStatus
		contrib  *StatusContribution
		validate func(*testing.T, *v1beta1.DeviceStatus)
	}{
		{
			name: "When contribution is nil it should be a no-op",
			initial: func() *v1beta1.DeviceStatus {
				s := v1beta1.NewDeviceStatus()
				return &s
			},
			contrib: nil,
			validate: func(t *testing.T, s *v1beta1.DeviceStatus) {
				expected := v1beta1.NewDeviceStatus()
				require.Equal(t, &expected, s)
			},
		},
		{
			name: "When all fields are nil it should preserve existing values",
			initial: func() *v1beta1.DeviceStatus {
				s := v1beta1.NewDeviceStatus()
				s.Os.Image = "existing-image"
				s.Config.RenderedVersion = "v1"
				return &s
			},
			contrib: &StatusContribution{},
			validate: func(t *testing.T, s *v1beta1.DeviceStatus) {
				require.Equal(t, "existing-image", s.Os.Image)
				require.Equal(t, "v1", s.Config.RenderedVersion)
			},
		},
		{
			name: "When Applications is set it should append to existing",
			initial: func() *v1beta1.DeviceStatus {
				s := v1beta1.NewDeviceStatus()
				s.Applications = []v1beta1.DeviceApplicationStatus{{Name: "existing"}}
				return &s
			},
			contrib: &StatusContribution{
				Applications: []v1beta1.DeviceApplicationStatus{{Name: "new-app"}},
			},
			validate: func(t *testing.T, s *v1beta1.DeviceStatus) {
				require.Len(t, s.Applications, 2)
				require.Equal(t, "existing", s.Applications[0].Name)
				require.Equal(t, "new-app", s.Applications[1].Name)
			},
		},
		{
			name: "When ApplicationsSummary is set it should overwrite",
			initial: func() *v1beta1.DeviceStatus {
				s := v1beta1.NewDeviceStatus()
				return &s
			},
			contrib: &StatusContribution{
				ApplicationsSummary: &v1beta1.DeviceApplicationsSummaryStatus{
					Status: v1beta1.ApplicationsSummaryStatusHealthy,
				},
			},
			validate: func(t *testing.T, s *v1beta1.DeviceStatus) {
				require.Equal(t, v1beta1.ApplicationsSummaryStatusHealthy, s.ApplicationsSummary.Status)
			},
		},
		{
			name: "When Systemd is set with non-nil slice it should set field",
			initial: func() *v1beta1.DeviceStatus {
				s := v1beta1.NewDeviceStatus()
				return &s
			},
			contrib: &StatusContribution{
				Systemd: lo.ToPtr([]v1beta1.SystemdUnitStatus{{Unit: "foo.service"}}),
			},
			validate: func(t *testing.T, s *v1beta1.DeviceStatus) {
				require.NotNil(t, s.Systemd)
				require.Len(t, *s.Systemd, 1)
				require.Equal(t, "foo.service", (*s.Systemd)[0].Unit)
			},
		},
		{
			name: "When Systemd points to nil slice it should clear field to nil",
			initial: func() *v1beta1.DeviceStatus {
				s := v1beta1.NewDeviceStatus()
				s.Systemd = lo.ToPtr([]v1beta1.SystemdUnitStatus{{Unit: "old.service"}})
				return &s
			},
			contrib: func() *StatusContribution {
				var nilSlice []v1beta1.SystemdUnitStatus
				return &StatusContribution{Systemd: &nilSlice}
			}(),
			validate: func(t *testing.T, s *v1beta1.DeviceStatus) {
				require.Nil(t, s.Systemd)
			},
		},
		{
			name: "When Resources is set it should overwrite",
			initial: func() *v1beta1.DeviceStatus {
				s := v1beta1.NewDeviceStatus()
				return &s
			},
			contrib: &StatusContribution{
				Resources: &v1beta1.DeviceResourceStatus{
					Cpu:    v1beta1.DeviceResourceStatusWarning,
					Disk:   v1beta1.DeviceResourceStatusHealthy,
					Memory: v1beta1.DeviceResourceStatusHealthy,
				},
			},
			validate: func(t *testing.T, s *v1beta1.DeviceStatus) {
				require.Equal(t, v1beta1.DeviceResourceStatusWarning, s.Resources.Cpu)
			},
		},
		{
			name: "When Os is set it should overwrite",
			initial: func() *v1beta1.DeviceStatus {
				s := v1beta1.NewDeviceStatus()
				s.Os.Image = "old-image"
				return &s
			},
			contrib: &StatusContribution{
				Os: &v1beta1.DeviceOsStatus{
					Image:       "new-image",
					ImageDigest: "sha256:abc",
				},
			},
			validate: func(t *testing.T, s *v1beta1.DeviceStatus) {
				require.Equal(t, "new-image", s.Os.Image)
				require.Equal(t, "sha256:abc", s.Os.ImageDigest)
			},
		},
		{
			name: "When Config is set it should overwrite",
			initial: func() *v1beta1.DeviceStatus {
				s := v1beta1.NewDeviceStatus()
				return &s
			},
			contrib: &StatusContribution{
				Config: &v1beta1.DeviceConfigStatus{RenderedVersion: "v5"},
			},
			validate: func(t *testing.T, s *v1beta1.DeviceStatus) {
				require.Equal(t, "v5", s.Config.RenderedVersion)
			},
		},
		{
			name: "When SystemInfo is set it should overwrite",
			initial: func() *v1beta1.DeviceStatus {
				s := v1beta1.NewDeviceStatus()
				return &s
			},
			contrib: &StatusContribution{
				SystemInfo: &v1beta1.DeviceSystemInfo{
					Architecture: "amd64",
					BootID:       "boot-123",
				},
			},
			validate: func(t *testing.T, s *v1beta1.DeviceStatus) {
				require.Equal(t, "amd64", s.SystemInfo.Architecture)
				require.Equal(t, "boot-123", s.SystemInfo.BootID)
			},
		},
	}

	for _, tt := range testCases {
		t.Run(tt.name, func(t *testing.T) {
			s := tt.initial()
			mergeContribution(s, tt.contrib)
			tt.validate(t, s)
		})
	}
}

func TestCalculateSummary(t *testing.T) {
	testCases := []struct {
		name           string
		contributions  []*SummaryContribution
		configWarnings []string
		expected       v1beta1.DeviceSummaryStatus
	}{
		{
			name:          "When no contributions and no warnings it should default to Online",
			contributions: nil,
			expected: v1beta1.DeviceSummaryStatus{
				Status: v1beta1.DeviceSummaryStatusOnline,
			},
		},
		{
			name: "When single Online contribution it should return Online",
			contributions: []*SummaryContribution{
				{Status: v1beta1.DeviceSummaryStatusOnline},
			},
			expected: v1beta1.DeviceSummaryStatus{
				Status: v1beta1.DeviceSummaryStatusOnline,
			},
		},
		{
			name: "When Error and Degraded it should pick Error (highest severity)",
			contributions: []*SummaryContribution{
				{Status: v1beta1.DeviceSummaryStatusDegraded, Info: lo.ToPtr("degraded reason")},
				{Status: v1beta1.DeviceSummaryStatusError, Info: lo.ToPtr("error reason")},
			},
			expected: v1beta1.DeviceSummaryStatus{
				Status: v1beta1.DeviceSummaryStatusError,
				Info:   lo.ToPtr("error reason"),
			},
		},
		{
			name: "When Rebooting and Online it should pick Rebooting",
			contributions: []*SummaryContribution{
				{Status: v1beta1.DeviceSummaryStatusOnline},
				{Status: v1beta1.DeviceSummaryStatusRebooting, Info: lo.ToPtr("rebooting")},
			},
			expected: v1beta1.DeviceSummaryStatus{
				Status: v1beta1.DeviceSummaryStatusRebooting,
				Info:   lo.ToPtr("rebooting"),
			},
		},
		{
			name: "When tied severity it should pick the last one",
			contributions: []*SummaryContribution{
				{Status: v1beta1.DeviceSummaryStatusDegraded, Info: lo.ToPtr("first")},
				{Status: v1beta1.DeviceSummaryStatusDegraded, Info: lo.ToPtr("second")},
			},
			expected: v1beta1.DeviceSummaryStatus{
				Status: v1beta1.DeviceSummaryStatusDegraded,
				Info:   lo.ToPtr("second"),
			},
		},
		{
			name:           "When config warnings present and no higher severity it should return Degraded",
			contributions:  []*SummaryContribution{{Status: v1beta1.DeviceSummaryStatusOnline}},
			configWarnings: []string{"bad config", "another warning"},
			expected: v1beta1.DeviceSummaryStatus{
				Status: v1beta1.DeviceSummaryStatusDegraded,
				Info:   lo.ToPtr("bad config; another warning"),
			},
		},
		{
			name: "When config warnings present but Error severity exists it should keep Error",
			contributions: []*SummaryContribution{
				{Status: v1beta1.DeviceSummaryStatusError, Info: lo.ToPtr("critical")},
			},
			configWarnings: []string{"config issue"},
			expected: v1beta1.DeviceSummaryStatus{
				Status: v1beta1.DeviceSummaryStatusError,
				Info:   lo.ToPtr("critical"),
			},
		},
		{
			name:           "When config warnings and no contributions it should return Degraded",
			contributions:  nil,
			configWarnings: []string{"some warning"},
			expected: v1beta1.DeviceSummaryStatus{
				Status: v1beta1.DeviceSummaryStatusDegraded,
				Info:   lo.ToPtr("some warning"),
			},
		},
		{
			name: "When nil contribution entries it should ignore them",
			contributions: []*SummaryContribution{
				nil,
				{Status: v1beta1.DeviceSummaryStatusDegraded, Info: lo.ToPtr("valid")},
				nil,
			},
			expected: v1beta1.DeviceSummaryStatus{
				Status: v1beta1.DeviceSummaryStatusDegraded,
				Info:   lo.ToPtr("valid"),
			},
		},
	}

	for _, tt := range testCases {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			result := calculateSummary(tt.contributions, tt.configWarnings)
			require.Equal(tt.expected.Status, result.Status)
			if tt.expected.Info == nil {
				require.Nil(result.Info)
			} else {
				require.NotNil(result.Info)
				require.Equal(*tt.expected.Info, *result.Info)
			}
		})
	}
}

func TestCollect(t *testing.T) {
	testCases := []struct {
		name       string
		setupMocks func(*MockExporter, *MockExporter)
		validate   func(*testing.T, *v1beta1.DeviceStatus, error)
	}{
		{
			name: "When multiple exporters succeed it should merge all contributions",
			setupMocks: func(exp1, exp2 *MockExporter) {
				exp1.EXPECT().Status(gomock.Any()).Return(&StatusContribution{
					Os: &v1beta1.DeviceOsStatus{Image: "image-1"},
				}, nil)
				exp2.EXPECT().Status(gomock.Any()).Return(&StatusContribution{
					Config: &v1beta1.DeviceConfigStatus{RenderedVersion: "v3"},
				}, nil)
			},
			validate: func(t *testing.T, s *v1beta1.DeviceStatus, err error) {
				require := require.New(t)
				require.NoError(err)
				require.Equal("image-1", s.Os.Image)
				require.Equal("v3", s.Config.RenderedVersion)
			},
		},
		{
			name: "When exporter returns contribution and error it should merge contribution and aggregate error",
			setupMocks: func(exp1, exp2 *MockExporter) {
				exp1.EXPECT().Status(gomock.Any()).Return(&StatusContribution{
					Os: &v1beta1.DeviceOsStatus{Image: "partial-image"},
				}, fmt.Errorf("partial failure"))
				exp2.EXPECT().Status(gomock.Any()).Return(&StatusContribution{
					Config: &v1beta1.DeviceConfigStatus{RenderedVersion: "v2"},
				}, nil)
			},
			validate: func(t *testing.T, s *v1beta1.DeviceStatus, err error) {
				require := require.New(t)
				require.Error(err)
				require.Contains(err.Error(), "partial failure")
				require.Equal("partial-image", s.Os.Image)
				require.Equal("v2", s.Config.RenderedVersion)
			},
		},
		{
			name: "When exporter returns nil contribution and error it should still continue",
			setupMocks: func(exp1, exp2 *MockExporter) {
				exp1.EXPECT().Status(gomock.Any()).Return(nil, fmt.Errorf("total failure"))
				exp2.EXPECT().Status(gomock.Any()).Return(&StatusContribution{
					Config: &v1beta1.DeviceConfigStatus{RenderedVersion: "v4"},
				}, nil)
			},
			validate: func(t *testing.T, s *v1beta1.DeviceStatus, err error) {
				require := require.New(t)
				require.Error(err)
				require.Contains(err.Error(), "total failure")
				require.Equal("v4", s.Config.RenderedVersion)
			},
		},
		{
			name: "When collect is called it should carry forward prior status fields",
			setupMocks: func(exp1, exp2 *MockExporter) {
				exp1.EXPECT().Status(gomock.Any()).Return(&StatusContribution{}, nil)
				exp2.EXPECT().Status(gomock.Any()).Return(&StatusContribution{}, nil)
			},
			validate: func(t *testing.T, s *v1beta1.DeviceStatus, err error) {
				require := require.New(t)
				require.NoError(err)
				require.Equal("pre-existing-image", s.Os.Image)
			},
		},
		{
			name: "When collect is called it should reset Applications to empty",
			setupMocks: func(exp1, exp2 *MockExporter) {
				exp1.EXPECT().Status(gomock.Any()).Return(&StatusContribution{}, nil)
				exp2.EXPECT().Status(gomock.Any()).Return(&StatusContribution{}, nil)
			},
			validate: func(t *testing.T, s *v1beta1.DeviceStatus, err error) {
				require := require.New(t)
				require.NoError(err)
				require.Empty(s.Applications)
			},
		},
		{
			name: "When exporter provides SummaryContribution it should calculate summary",
			setupMocks: func(exp1, exp2 *MockExporter) {
				exp1.EXPECT().Status(gomock.Any()).Return(&StatusContribution{
					SummaryContribution: &SummaryContribution{
						Status: v1beta1.DeviceSummaryStatusError,
						Info:   lo.ToPtr("resource critical"),
					},
				}, nil)
				exp2.EXPECT().Status(gomock.Any()).Return(&StatusContribution{
					SummaryContribution: &SummaryContribution{
						Status: v1beta1.DeviceSummaryStatusOnline,
					},
				}, nil)
			},
			validate: func(t *testing.T, s *v1beta1.DeviceStatus, err error) {
				require := require.New(t)
				require.NoError(err)
				require.Equal(v1beta1.DeviceSummaryStatusError, s.Summary.Status)
				require.NotNil(s.Summary.Info)
				require.Equal("resource critical", *s.Summary.Info)
			},
		},
	}

	for _, tt := range testCases {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			exp1 := NewMockExporter(ctrl)
			exp2 := NewMockExporter(ctrl)
			tt.setupMocks(exp1, exp2)

			mgr := NewManager("test-device", log.NewPrefixLogger(""), []Exporter{exp1, exp2}, nil)
			// Set pre-existing status to verify carry-forward
			mgr.device.Status.Os.Image = "pre-existing-image"
			mgr.device.Status.Applications = []v1beta1.DeviceApplicationStatus{{Name: "old-app"}}

			err := mgr.Collect(context.Background())
			tt.validate(t, mgr.device.Status, err)
		})
	}
}

func TestInvalidateLastStatus_CausesNextSyncToPush(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	deviceName := "test-device"
	ctx := context.Background()
	mockClient := client.NewMockManagement(ctrl)
	mockExporter := NewMockExporter(ctrl)

	mgr := NewManager(deviceName, log.NewPrefixLogger(""), []Exporter{mockExporter}, nil)
	mgr.SetClient(mockClient)

	mockExporter.EXPECT().Status(gomock.Any()).Return(&StatusContribution{}, nil).Times(2)
	mockClient.EXPECT().UpdateDeviceStatus(gomock.Any(), deviceName, gomock.Any()).Return(nil).Times(2)

	require.NoError(mgr.Sync(ctx))
	mgr.InvalidateLastStatus()
	require.NoError(mgr.Sync(ctx))
}
