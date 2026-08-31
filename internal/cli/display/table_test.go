package display

import (
	"bytes"
	"strings"
	"testing"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	apiclient "github.com/flightctl/flightctl/internal/api/client"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
)

const unsetUpToDateTable = "NAME\tALIAS\tOWNER\tSYSTEM\tUPDATED\t\tAPPLICATIONS\ndev-1\t\t<none>\tUnknown\tUpToDate\tUnknown\n"

const emptyCapabilitiesSummary = "DEVICES\n1\n\nSTATUS TYPE\tSTATUS\t\tCOUNT\nSYSTEM\t\tOnline\t\t1\nUPDATED\t\tUpToDate\t1\nAPPLICATIONS\tHealthy\t\t1\n"

func upToDateDevice(name string) api.Device {
	status := api.NewDeviceStatus()
	status.Updated.Status = api.DeviceUpdatedStatusUpToDate
	return api.Device{
		Metadata: api.ObjectMeta{Name: lo.ToPtr(name)},
		Status:   &status,
	}
}

func formatDeviceList(t *testing.T, f *TableFormatter, items []api.Device, summary *api.DevicesSummary, opts FormatOptions) string {
	t.Helper()
	var buf bytes.Buffer
	opts.Writer = &buf
	if opts.Kind == "" {
		opts.Kind = api.DeviceKind
	}
	require.NoError(t, f.Format(&apiclient.ListDevicesResponse{
		JSON200: &api.DeviceList{Items: items, Summary: summary},
	}, opts))
	return buf.String()
}

func formatDeviceGet(t *testing.T, f *TableFormatter, device api.Device) string {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, f.Format(&apiclient.GetDeviceResponse{JSON200: &device}, FormatOptions{
		Kind:   api.DeviceKind,
		Name:   *device.Metadata.Name,
		Writer: &buf,
	}))
	return buf.String()
}

func TestTableFormatter_WhenSizeAndFallbackAreSetItShouldShowThemInUpdatedCell(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		mutate       func(*api.DeviceStatus)
		wantContains []string
		wantAbsent   []string
		wantHeader   []string
	}{
		{
			name: "When size is populated it should include 45 MiB in list and single table",
			mutate: func(s *api.DeviceStatus) {
				s.Os.LastDelta = &api.DeviceDeltaApplyStatus{Size: lo.ToPtr("45 MiB")}
			},
			wantContains: []string{"45 MiB"},
			wantHeader:   []string{"NAME", "ALIAS", "OWNER", "SYSTEM", "UPDATED", "APPLICATIONS"},
		},
		{
			name: "When lastDelta.fallbackReason is set it should show (fallback) in list and single table",
			mutate: func(s *api.DeviceStatus) {
				s.Os.LastDelta = &api.DeviceDeltaApplyStatus{FallbackReason: lo.ToPtr("delta apply failed: apply error")}
			},
			wantContains: []string{"(fallback)"},
			wantAbsent:   []string{"delta apply failed"},
			wantHeader:   []string{"NAME", "ALIAS", "OWNER", "SYSTEM", "UPDATED", "APPLICATIONS"},
		},
		{
			name: "When size and fallback are both set it should print status size then (fallback)",
			mutate: func(s *api.DeviceStatus) {
				s.Os.LastDelta = &api.DeviceDeltaApplyStatus{
					Size:           lo.ToPtr("45 MiB"),
					FallbackReason: lo.ToPtr("delta apply failed: apply error"),
				}
			},
			wantContains: []string{"UpToDate 45 MiB (fallback)"},
			wantHeader:   []string{"NAME", "ALIAS", "OWNER", "SYSTEM", "UPDATED", "APPLICATIONS"},
		},
		{
			name: "When size is empty string it should treat it as unset",
			mutate: func(s *api.DeviceStatus) {
				s.Os.LastDelta = &api.DeviceDeltaApplyStatus{Size: lo.ToPtr("")}
			},
			wantAbsent: []string{"(fallback)"},
		},
		{
			name: "When fallback reason is empty string it should treat it as unset",
			mutate: func(s *api.DeviceStatus) {
				s.Os.LastDelta = &api.DeviceDeltaApplyStatus{FallbackReason: lo.ToPtr("")}
			},
			wantAbsent: []string{"(fallback)"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			device := upToDateDevice("dev-1")
			tt.mutate(device.Status)

			listOut := formatDeviceList(t, &TableFormatter{}, []api.Device{device}, nil, FormatOptions{})
			singleOut := formatDeviceGet(t, &TableFormatter{}, device)

			for _, out := range []string{listOut, singleOut} {
				for _, token := range tt.wantContains {
					require.Contains(t, out, token)
				}
				for _, token := range tt.wantAbsent {
					require.NotContains(t, out, token)
				}
				if len(tt.wantContains) == 0 {
					require.Equal(t, unsetUpToDateTable, out)
				}
				if len(tt.wantHeader) > 0 {
					header := strings.Fields(strings.Split(out, "\n")[0])
					require.Equal(t, tt.wantHeader, header)
					require.NotContains(t, header, "SIZE")
				}
			}
		})
	}
}

func TestTableFormatter_WhenSizeEligibilityAndFallbackAreUnsetItShouldKeepCurrentLayout(t *testing.T) {
	t.Parallel()

	device := upToDateDevice("dev-1")
	device.Status.Capabilities = &api.DeviceCapabilities{}

	listOut := formatDeviceList(t, &TableFormatter{}, []api.Device{device}, nil, FormatOptions{})
	singleOut := formatDeviceGet(t, &TableFormatter{}, device)

	require.Equal(t, unsetUpToDateTable, listOut)
	require.Equal(t, unsetUpToDateTable, singleOut)
}

func TestTableFormatter_WhenDeltaEligibleIsSetItShouldNotPrintItOnDeviceTable(t *testing.T) {
	t.Parallel()

	device := upToDateDevice("dev-1")
	device.Status.SystemInfo.DeltaEligible = lo.ToPtr(true)

	out := formatDeviceList(t, &TableFormatter{}, []api.Device{device}, nil, FormatOptions{})
	require.Equal(t, unsetUpToDateTable, out)
	require.NotContains(t, out, "DELTA ELIGIBLE")
}

func TestTableFormatter_WhenStatusIsNilItShouldPrintUnknownWithoutPanic(t *testing.T) {
	t.Parallel()

	device := api.Device{Metadata: api.ObjectMeta{Name: lo.ToPtr("dev-1")}}
	out := formatDeviceList(t, &TableFormatter{}, []api.Device{device}, nil, FormatOptions{})
	require.Contains(t, out, "Unknown")
	require.NotContains(t, out, "(fallback)")
	require.NotContains(t, out, "45 MiB")
}

func TestTableFormatter_WhenWideItShouldKeepLabelsLastAndPutSizeFallbackInUpdated(t *testing.T) {
	t.Parallel()

	device := upToDateDevice("dev-1")
	device.Status.Os.LastDelta = &api.DeviceDeltaApplyStatus{
		Size:           lo.ToPtr("45 MiB"),
		FallbackReason: lo.ToPtr("delta apply failed"),
	}
	labels := map[string]string{"alias": "edge-1", "env": "prod"}
	device.Metadata.Labels = &labels
	device.Metadata.Owner = lo.ToPtr("Fleet/f1")

	out := formatDeviceList(t, &TableFormatter{wide: true}, []api.Device{device}, nil, FormatOptions{})
	header := strings.Fields(strings.Split(out, "\n")[0])
	require.Equal(t, "LABELS", header[len(header)-1])
	require.Equal(t, []string{"NAME", "ALIAS", "OWNER", "SYSTEM", "UPDATED", "APPLICATIONS", "LABELS"}, header)
	require.Contains(t, out, "45 MiB")
	require.Contains(t, out, "(fallback)")
}

func TestTableFormatter_WhenSummaryHasDeltaEligibleItShouldPrintCapabilityCounts(t *testing.T) {
	t.Parallel()

	baseSummary := func() *api.DevicesSummary {
		return &api.DevicesSummary{
			Total:             2,
			SummaryStatus:     map[string]int64{"Online": 2},
			UpdateStatus:      map[string]int64{"UpToDate": 2},
			ApplicationStatus: map[string]int64{"Healthy": 2},
		}
	}

	tests := []struct {
		name         string
		caps         *api.DevicesSummaryCapabilities
		wantContains []string
		wantAbsent   []string
		checkOrder   bool
	}{
		{
			name: "When true and false counts are present it should print DELTA ELIGIBLE rows",
			caps: &api.DevicesSummaryCapabilities{
				DeltaEligible: &map[string]int64{"true": 1, "false": 1},
			},
			wantContains: []string{"CAPABILITY", "DELTA ELIGIBLE", "true", "false"},
			wantAbsent:   []string{"OS MODE"},
		},
		{
			name: "When only OsMode is present it should print OS MODE and not DELTA ELIGIBLE",
			caps: &api.DevicesSummaryCapabilities{
				OsMode: &map[string]int64{"image": 2},
			},
			wantContains: []string{"CAPABILITY", "OS MODE", "image"},
			wantAbsent:   []string{"DELTA ELIGIBLE"},
		},
		{
			name: "When both maps are present it should print OS MODE then DELTA ELIGIBLE",
			caps: &api.DevicesSummaryCapabilities{
				OsMode:        &map[string]int64{"image": 1, "package": 1},
				DeltaEligible: &map[string]int64{"true": 1, "false": 1},
			},
			wantContains: []string{"OS MODE", "DELTA ELIGIBLE", "image", "package", "true", "false"},
			checkOrder:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			summary := baseSummary()
			summary.Capabilities = tt.caps
			out := formatDeviceList(t, &TableFormatter{}, nil, summary, FormatOptions{SummaryOnly: true})
			for _, token := range tt.wantContains {
				require.Contains(t, out, token)
			}
			for _, token := range tt.wantAbsent {
				require.NotContains(t, out, token)
			}
			if tt.checkOrder {
				require.Greater(t, strings.Index(out, "DELTA ELIGIBLE"), strings.Index(out, "OS MODE"))
			}
		})
	}
}

func TestTableFormatter_WhenCapabilitiesAreEmptyItShouldOmitCapabilitySection(t *testing.T) {
	t.Parallel()

	summary := &api.DevicesSummary{
		Total:             1,
		SummaryStatus:     map[string]int64{"Online": 1},
		UpdateStatus:      map[string]int64{"UpToDate": 1},
		ApplicationStatus: map[string]int64{"Healthy": 1},
	}
	out := formatDeviceList(t, &TableFormatter{}, nil, summary, FormatOptions{SummaryOnly: true})
	require.Equal(t, emptyCapabilitiesSummary, out)
	require.NotContains(t, out, "CAPABILITY")
	require.NotContains(t, out, "DELTA ELIGIBLE")
}
