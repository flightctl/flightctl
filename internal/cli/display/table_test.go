package display

import (
	"bytes"
	"strings"
	"testing"
	"text/tabwriter"

	apiv1alpha1 "github.com/flightctl/flightctl/api/core/v1alpha1"
	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/stretchr/testify/require"
)

// lineContaining returns the first line of output that contains the given
// substring, or "" if none does. Used to assert on a specific row of a
// multi-section tabwriter table without depending on exact column padding.
func lineContaining(output, substr string) string {
	for _, line := range strings.Split(output, "\n") {
		if strings.Contains(line, substr) {
			return line
		}
	}
	return ""
}

// newTestWriter creates a tabwriter backed by a bytes.Buffer for test output
// capture. Space padchar produces output where byte positions match display
// positions, making column-level cell extraction straightforward.
func newTestWriter(buf *bytes.Buffer) *tabwriter.Writer {
	return tabwriter.NewWriter(buf, 0, 8, 1, ' ', 0)
}

// extractColumnCells parses tabwriter-formatted table output and returns the
// trimmed cell value of the named column for each data row. Column boundaries
// are determined from the header line so assertions bind to a specific column
// position, not just any substring in the row.
func extractColumnCells(t *testing.T, output, column string) []string {
	t.Helper()

	lines := strings.Split(strings.TrimRight(output, "\n"), "\n")
	require.GreaterOrEqual(t, len(lines), 2, "table output must have header + data rows")

	header := lines[0]
	colStart := strings.Index(header, column)
	require.NotEqual(t, -1, colStart, "column %q not found in header", column)

	// Determine the end of this column's cell region: skip past the column
	// name in the header then past any padding until the next header word,
	// which marks the start of the next column. If nothing follows, the
	// column extends to end of line.
	rest := header[colStart+len(column):]
	colEnd := len(header)
	for i, ch := range rest {
		if ch != '\t' && ch != ' ' {
			colEnd = colStart + len(column) + i
			break
		}
	}

	var cells []string
	for _, line := range lines[1:] {
		if strings.TrimSpace(line) == "" {
			continue
		}
		start := colStart
		end := colEnd
		if start >= len(line) {
			cells = append(cells, "")
			continue
		}
		if end > len(line) {
			end = len(line)
		}
		cells = append(cells, strings.TrimSpace(line[start:end]))
	}
	return cells
}

func TestPrintDevicesTableSystemInfoColumn(t *testing.T) {
	deviceName := "test-device-001"

	tests := []struct {
		name           string
		wide           bool
		device         api.Device
		wantSystemInfo string
	}{
		{
			name: "When device has systemInfoStatus it should show system info status in SYSTEM INFO column",
			device: api.Device{
				Metadata: api.ObjectMeta{Name: &deviceName},
				Status: &api.DeviceStatus{
					Summary:             api.DeviceSummaryStatus{Status: "Online"},
					Updated:             api.DeviceUpdatedStatus{Status: "UpToDate"},
					ApplicationsSummary: api.DeviceApplicationsSummaryStatus{Status: "Healthy"},
					SystemInfoStatus: &api.DeviceSystemInfoStatus{
						Summary: api.DeviceSystemInfoSummaryStatus{
							Status: api.SystemInfoSummaryStatusHealthy,
						},
						Statuses: api.DeviceSystemInfoStatuses{
							SystemInfo: map[string]api.SystemInfoSourceStatus{},
							CustomInfo: map[string]api.SystemInfoSourceStatus{},
						},
					},
				},
			},
			wantSystemInfo: "Healthy",
		},
		{
			name: "When device has nil systemInfoStatus it should show Unknown for SYSTEM INFO column",
			device: api.Device{
				Metadata: api.ObjectMeta{Name: &deviceName},
				Status: &api.DeviceStatus{
					Summary:             api.DeviceSummaryStatus{Status: "Online"},
					Updated:             api.DeviceUpdatedStatus{Status: "UpToDate"},
					ApplicationsSummary: api.DeviceApplicationsSummaryStatus{Status: "Healthy"},
				},
			},
			wantSystemInfo: "Unknown",
		},
		{
			name: "When device status is nil it should show Unknown for SYSTEM INFO column",
			device: api.Device{
				Metadata: api.ObjectMeta{Name: &deviceName},
			},
			wantSystemInfo: "Unknown",
		},
		{
			name: "When systemInfoStatus summary is Degraded it should show Degraded in SYSTEM INFO column",
			device: api.Device{
				Metadata: api.ObjectMeta{Name: &deviceName},
				Status: &api.DeviceStatus{
					Summary:             api.DeviceSummaryStatus{Status: "Online"},
					Updated:             api.DeviceUpdatedStatus{Status: "UpToDate"},
					ApplicationsSummary: api.DeviceApplicationsSummaryStatus{Status: "Healthy"},
					SystemInfoStatus: &api.DeviceSystemInfoStatus{
						Summary: api.DeviceSystemInfoSummaryStatus{
							Status: api.SystemInfoSummaryStatusDegraded,
						},
						Statuses: api.DeviceSystemInfoStatuses{
							SystemInfo: map[string]api.SystemInfoSourceStatus{},
							CustomInfo: map[string]api.SystemInfoSourceStatus{},
						},
					},
				},
			},
			wantSystemInfo: "Degraded",
		},
		{
			name: "When systemInfoStatus summary is Error it should show Error in SYSTEM INFO column",
			device: api.Device{
				Metadata: api.ObjectMeta{Name: &deviceName},
				Status: &api.DeviceStatus{
					Summary:             api.DeviceSummaryStatus{Status: "Online"},
					Updated:             api.DeviceUpdatedStatus{Status: "UpToDate"},
					ApplicationsSummary: api.DeviceApplicationsSummaryStatus{Status: "Healthy"},
					SystemInfoStatus: &api.DeviceSystemInfoStatus{
						Summary: api.DeviceSystemInfoSummaryStatus{
							Status: api.SystemInfoSummaryStatusError,
						},
						Statuses: api.DeviceSystemInfoStatuses{
							SystemInfo: map[string]api.SystemInfoSourceStatus{},
							CustomInfo: map[string]api.SystemInfoSourceStatus{},
						},
					},
				},
			},
			wantSystemInfo: "Error",
		},
		{
			name: "When wide mode is enabled it should include SYSTEM INFO column alongside LABELS",
			wide: true,
			device: api.Device{
				Metadata: api.ObjectMeta{
					Name:   &deviceName,
					Labels: &map[string]string{"alias": "dev1", "env": "prod"},
				},
				Status: &api.DeviceStatus{
					Summary:             api.DeviceSummaryStatus{Status: "Online"},
					Updated:             api.DeviceUpdatedStatus{Status: "UpToDate"},
					ApplicationsSummary: api.DeviceApplicationsSummaryStatus{Status: "Healthy"},
					SystemInfoStatus: &api.DeviceSystemInfoStatus{
						Summary: api.DeviceSystemInfoSummaryStatus{
							Status: api.SystemInfoSummaryStatusHealthy,
						},
						Statuses: api.DeviceSystemInfoStatuses{
							SystemInfo: map[string]api.SystemInfoSourceStatus{},
							CustomInfo: map[string]api.SystemInfoSourceStatus{},
						},
					},
				},
			},
			wantSystemInfo: "Healthy",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			w := newTestWriter(&buf)
			f := &TableFormatter{}

			err := f.printDevicesTable(w, tt.wide, tt.device)
			require.NoError(t, err)

			err = w.Flush()
			require.NoError(t, err)

			output := buf.String()

			// Verify the SYSTEM INFO header exists.
			require.Contains(t, output, "SYSTEM INFO")

			// Extract and verify the SYSTEM INFO column cell value
			// specifically, ensuring the assertion binds to this column
			// and not another (e.g. APPLICATIONS) that might share the
			// same value.
			cells := extractColumnCells(t, output, "SYSTEM INFO")
			require.Len(t, cells, 1, "expected exactly one data row")
			require.Equal(t, tt.wantSystemInfo, cells[0],
				"SYSTEM INFO column should contain %q", tt.wantSystemInfo)

			if tt.wide {
				require.Contains(t, output, "LABELS",
					"wide mode should include LABELS header")
			}
		})
	}
}

func TestPrintDevicesTableMultipleDevicesSystemInfo(t *testing.T) {
	deviceA := "device-healthy"
	deviceB := "device-degraded"

	devA := api.Device{
		Metadata: api.ObjectMeta{Name: &deviceA},
		Status: &api.DeviceStatus{
			Summary:             api.DeviceSummaryStatus{Status: "Online"},
			Updated:             api.DeviceUpdatedStatus{Status: "UpToDate"},
			ApplicationsSummary: api.DeviceApplicationsSummaryStatus{Status: "Healthy"},
			SystemInfoStatus: &api.DeviceSystemInfoStatus{
				Summary: api.DeviceSystemInfoSummaryStatus{
					Status: api.SystemInfoSummaryStatusHealthy,
				},
				Statuses: api.DeviceSystemInfoStatuses{
					SystemInfo: map[string]api.SystemInfoSourceStatus{},
					CustomInfo: map[string]api.SystemInfoSourceStatus{},
				},
			},
		},
	}

	devB := api.Device{
		Metadata: api.ObjectMeta{Name: &deviceB},
		Status: &api.DeviceStatus{
			Summary:             api.DeviceSummaryStatus{Status: "Online"},
			Updated:             api.DeviceUpdatedStatus{Status: "UpToDate"},
			ApplicationsSummary: api.DeviceApplicationsSummaryStatus{Status: "Healthy"},
			SystemInfoStatus: &api.DeviceSystemInfoStatus{
				Summary: api.DeviceSystemInfoSummaryStatus{
					Status: api.SystemInfoSummaryStatusDegraded,
				},
				Statuses: api.DeviceSystemInfoStatuses{
					SystemInfo: map[string]api.SystemInfoSourceStatus{},
					CustomInfo: map[string]api.SystemInfoSourceStatus{},
				},
			},
		},
	}

	var buf bytes.Buffer
	w := newTestWriter(&buf)
	f := &TableFormatter{}

	err := f.printDevicesTable(w, false, devA, devB)
	require.NoError(t, err)

	err = w.Flush()
	require.NoError(t, err)

	output := buf.String()

	// Verify expected headers are present and no internal field names leak.
	for _, h := range []string{"NAME", "ALIAS", "OWNER", "SYSTEM", "UPDATED", "APPLICATIONS", "SYSTEM INFO"} {
		require.Contains(t, output, h, "header %q should be present", h)
	}
	for _, absent := range []string{"SYSTEM INFO STATUS", "SYSTEM INFO SOURCES", "CUSTOM INFO SOURCES"} {
		require.NotContains(t, output, absent,
			"internal field name %q should not appear", absent)
	}

	// Verify each row's SYSTEM INFO cell independently.
	cells := extractColumnCells(t, output, "SYSTEM INFO")
	require.Len(t, cells, 2, "expected two data rows")
	require.Equal(t, "Healthy", cells[0],
		"first device SYSTEM INFO should be Healthy")
	require.Equal(t, "Degraded", cells[1],
		"second device SYSTEM INFO should be Degraded")
}

func TestPrintDevicesTableSystemInfoColumnHeaderPosition(t *testing.T) {
	// Verify SYSTEM INFO header appears after APPLICATIONS in the header row,
	// matching the pattern of other status columns.
	var buf bytes.Buffer
	w := newTestWriter(&buf)
	f := &TableFormatter{}

	deviceName := "pos-check"
	device := api.Device{
		Metadata: api.ObjectMeta{Name: &deviceName},
		Status: &api.DeviceStatus{
			Summary:             api.DeviceSummaryStatus{Status: "Online"},
			Updated:             api.DeviceUpdatedStatus{Status: "UpToDate"},
			ApplicationsSummary: api.DeviceApplicationsSummaryStatus{Status: "Healthy"},
			SystemInfoStatus: &api.DeviceSystemInfoStatus{
				Summary: api.DeviceSystemInfoSummaryStatus{
					Status: api.SystemInfoSummaryStatusHealthy,
				},
				Statuses: api.DeviceSystemInfoStatuses{
					SystemInfo: map[string]api.SystemInfoSourceStatus{},
					CustomInfo: map[string]api.SystemInfoSourceStatus{},
				},
			},
		},
	}

	err := f.printDevicesTable(w, false, device)
	require.NoError(t, err)

	err = w.Flush()
	require.NoError(t, err)

	output := buf.String()
	lines := strings.Split(output, "\n")
	require.NotEmpty(t, lines)

	header := lines[0]
	appsIdx := strings.Index(header, "APPLICATIONS")
	systemInfoIdx := strings.Index(header, "SYSTEM INFO")

	require.Greater(t, appsIdx, -1, "APPLICATIONS header should be present")
	require.Greater(t, systemInfoIdx, -1, "SYSTEM INFO header should be present")
	require.Greater(t, systemInfoIdx, appsIdx, "SYSTEM INFO should appear after APPLICATIONS")
}

// catalogItemWithFeatures builds a catalog item with a mix of versions: some
// declaring device feature requirements and one declaring none. Used by the
// detail-view tests below.
func catalogItemWithFeatures() apiv1alpha1.CatalogItem {
	name := "redis"
	gpuTrue := apiv1alpha1.DeviceFeatureBooleanTrue
	kvmFalse := apiv1alpha1.DeviceFeatureBooleanFalse
	osImage := api.OsModeImage

	return apiv1alpha1.CatalogItem{
		Metadata: apiv1alpha1.CatalogItemMeta{Name: &name, Catalog: "default"},
		Spec: apiv1alpha1.CatalogItemSpec{
			Type: "application",
			Versions: []apiv1alpha1.CatalogItemVersion{
				// Multiple requirements must collapse onto one row, joined with
				// "; " in the canonical feature order.
				{
					Version: "1.0.0",
					DeviceFeatures: &apiv1alpha1.DeviceFeatures{
						GpuPresent: &gpuTrue,
						OsMode:     &osImage,
					},
				},
				// Known features come first, then additional properties sorted
				// by name.
				{
					Version: "2.0.0",
					DeviceFeatures: &apiv1alpha1.DeviceFeatures{
						KvmEnabled:           &kvmFalse,
						AdditionalProperties: map[string]interface{}{"zzz.custom": "x"},
					},
				},
				// A version with no requirements must still appear, with "<none>".
				{Version: "3.0.0"},
			},
		},
	}
}

func TestPrintCatalogItemsTableDetailShowsFeatureRequirements(t *testing.T) {
	var buf bytes.Buffer
	w := newTestWriter(&buf)
	f := &TableFormatter{}

	// detail=true mirrors the single-item (get catalogitem <name> --catalog ...)
	// path.
	err := f.printCatalogItemsTable(w, false, true, catalogItemWithFeatures())
	require.NoError(t, err)
	require.NoError(t, w.Flush())

	output := buf.String()

	// The dedicated feature-requirements section has NAME / VERSION / FEATURES
	// columns. The NAME column keeps the requirements attributable to a specific
	// catalog item when several are shown.
	require.Contains(t, output, "VERSION", "VERSION header should be present")
	require.Contains(t, output, "FEATURES", "FEATURES header should be present")
	featureHeader := lineContaining(output, "FEATURES")
	require.NotEmpty(t, featureHeader, "feature requirements header row should be present")
	require.Contains(t, featureHeader, "NAME",
		"NAME header should be present in the feature requirements section")

	// One row per version: a version with multiple requirements collapses onto a
	// single line, semicolon-delimited in canonical order. Every row is prefixed
	// with the catalog item name.
	v1 := lineContaining(output, "1.0.0")
	require.NotEmpty(t, v1, "version 1.0.0 row should be present")
	require.Contains(t, v1, "redis", "version 1.0.0 row should include the catalog item name")
	require.Contains(t, v1, "gpu.present=true; os.mode=image",
		"version 1.0.0 should render both requirements on one row, semicolon-delimited")

	// Known features precede additional properties (sorted by name).
	v2 := lineContaining(output, "2.0.0")
	require.NotEmpty(t, v2, "version 2.0.0 row should be present")
	require.Contains(t, v2, "redis", "version 2.0.0 row should include the catalog item name")
	require.Contains(t, v2, "kvm.enabled=false; zzz.custom=x",
		"version 2.0.0 should list known features before additional properties")

	// A version without requirements still appears, showing "<none>" and no "=".
	v3 := lineContaining(output, "3.0.0")
	require.NotEmpty(t, v3, "version 3.0.0 row should be present even without requirements")
	require.Contains(t, v3, "redis", "version 3.0.0 row should include the catalog item name")
	require.Contains(t, v3, "<none>", "version 3.0.0 features cell should be \"<none>\"")
	require.NotContains(t, v3, "=", "version 3.0.0 should declare no key=value requirements")

	// Internal Go field names must not leak into user-facing output.
	for _, absent := range []string{"GpuPresent", "KvmEnabled", "OsMode", "DeviceFeatures"} {
		require.NotContains(t, output, absent, "internal field name %q should not appear", absent)
	}
}

func TestPrintCatalogItemsTableListOmitsFeatureRequirements(t *testing.T) {
	var buf bytes.Buffer
	w := newTestWriter(&buf)
	f := &TableFormatter{}

	// detail=false mirrors the list path (get catalogitems), which must not be
	// cluttered with per-version requirements.
	err := f.printCatalogItemsTable(w, false, false, catalogItemWithFeatures())
	require.NoError(t, err)
	require.NoError(t, w.Flush())

	output := buf.String()
	require.NotContains(t, output, "FEATURES",
		"list view should not render the feature requirements section")
	require.NotContains(t, output, "gpu.present",
		"list view should not render per-version feature requirements")
}

func TestPrintCatalogItemsTableDetailNoFeatureRequirements(t *testing.T) {
	var buf bytes.Buffer
	w := newTestWriter(&buf)
	f := &TableFormatter{}

	name := "nginx"
	item := apiv1alpha1.CatalogItem{
		Metadata: apiv1alpha1.CatalogItemMeta{Name: &name, Catalog: "default"},
		Spec: apiv1alpha1.CatalogItemSpec{
			Type: "application",
			Versions: []apiv1alpha1.CatalogItemVersion{
				{Version: "1.0.0"},
				{Version: "2.0.0"},
			},
		},
	}

	err := f.printCatalogItemsTable(w, false, true, item)
	require.NoError(t, err)
	require.NoError(t, w.Flush())

	output := buf.String()
	require.NotContains(t, output, "FEATURES",
		"when no version declares requirements the section should be omitted entirely")
}
