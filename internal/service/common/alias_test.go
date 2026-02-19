package common

import (
	"testing"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestComputeDefaultAlias(t *testing.T) {
	tests := []struct {
		name      string
		systemInfo domain.DeviceSystemInfo
		keys      []string
		want      string
	}{
		{
			name:      "empty keys",
			systemInfo: domain.DeviceSystemInfo{},
			keys:      nil,
			want:      "",
		},
		{
			name:      "empty keys slice",
			systemInfo: domain.DeviceSystemInfo{},
			keys:      []string{},
			want:      "",
		},
		{
			name: "empty systemInfo",
			systemInfo: domain.DeviceSystemInfo{},
			keys:      []string{"hostname", "architecture"},
			want:      "",
		},
		{
			name: "hostname only from additionalProperties",
			systemInfo: func() domain.DeviceSystemInfo {
				var si domain.DeviceSystemInfo
				si.Set("hostname", "my-device-01")
				return si
			}(),
			keys: []string{"hostname"},
			want: "my-device-01",
		},
		{
			name: "first non-empty wins - hostname then productSerial",
			systemInfo: func() domain.DeviceSystemInfo {
				var si domain.DeviceSystemInfo
				si.Set("hostname", "first")
				si.Set("productSerial", "SN123")
				return si
			}(),
			keys: []string{"hostname", "productSerial"},
			want: "first",
		},
		{
			name: "first non-empty wins - skip empty hostname",
			systemInfo: func() domain.DeviceSystemInfo {
				var si domain.DeviceSystemInfo
				si.Set("productSerial", "SN456")
				return si
			}(),
			keys: []string{"hostname", "productSerial"},
			want: "SN456",
		},
		{
			name: "fixed field architecture",
			systemInfo: domain.DeviceSystemInfo{
				Architecture: "amd64",
			},
			keys: []string{"architecture"},
			want: "amd64",
		},
		{
			name: "fixed field operatingSystem",
			systemInfo: domain.DeviceSystemInfo{
				OperatingSystem: "Linux",
			},
			keys: []string{"operatingSystem"},
			want: "Linux",
		},
		{
			name: "fixed field bootID and agentVersion",
			systemInfo: domain.DeviceSystemInfo{
				BootID:       "boot-uuid",
				AgentVersion: "1.2.3",
			},
			keys: []string{"bootID", "agentVersion"},
			want: "boot-uuid",
		},
		{
			name: "customInfo key",
			systemInfo: func() domain.DeviceSystemInfo {
				si := domain.DeviceSystemInfo{}
				si.CustomInfo = &domain.CustomDeviceInfo{"mykey": "custom-value"}
				return si
			}(),
			keys: []string{"customInfo.mykey"},
			want: "custom-value",
		},
		{
			name: "customInfo key with first non-empty",
			systemInfo: func() domain.DeviceSystemInfo {
				si := domain.DeviceSystemInfo{}
				si.CustomInfo = &domain.CustomDeviceInfo{"alias": "from-custom"}
				return si
			}(),
			keys: []string{"hostname", "customInfo.alias"},
			want: "from-custom",
		},
		{
			name: "sanitization strips invalid chars",
			systemInfo: func() domain.DeviceSystemInfo {
				var si domain.DeviceSystemInfo
				si.Set("hostname", "my@device#with$bad%chars")
				return si
			}(),
			keys: []string{"hostname"},
			want: "mydevicewithbadchars",
		},
		{
			name: "sanitization truncates to 63 chars",
			systemInfo: func() domain.DeviceSystemInfo {
				var si domain.DeviceSystemInfo
				long := ""
				for i := 0; i < 80; i++ {
					long += "a"
				}
				si.Set("hostname", long)
				return si
			}(),
			keys: []string{"hostname"},
			want: func() string {
				s := ""
				for i := 0; i < 63; i++ {
					s += "a"
				}
				return s
			}(),
		},
		{
			name: "trimmed leading/trailing invalid results in empty",
			systemInfo: func() domain.DeviceSystemInfo {
				var si domain.DeviceSystemInfo
				si.Set("hostname", "---...")
				return si
			}(),
			keys: []string{"hostname"},
			want: "",
		},
		{
			name: "whitespace trimmed from value",
			systemInfo: func() domain.DeviceSystemInfo {
				var si domain.DeviceSystemInfo
				si.Set("hostname", "  good-host  ")
				return si
			}(),
			keys: []string{"hostname"},
			want: "good-host",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ComputeDefaultAlias(tt.systemInfo, tt.keys)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestComputeDefaultAlias_SanitizeTruncate(t *testing.T) {
	// 63 chars is max; 64+ gets truncated
	var si domain.DeviceSystemInfo
	long := ""
	for i := 0; i < 70; i++ {
		long += "b"
	}
	si.Set("hostname", long)
	got := ComputeDefaultAlias(si, []string{"hostname"})
	require.Len(t, got, 63)
	assert.Equal(t, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", got)
}

func TestComputeDefaultAlias_EmptyAfterSanitize(t *testing.T) {
	var si domain.DeviceSystemInfo
	si.Set("hostname", "!!!@@@###")
	got := ComputeDefaultAlias(si, []string{"hostname"})
	assert.Empty(t, got)
}

func TestSanitizeLabelValue(t *testing.T) {
	// Exported for testing only via ComputeDefaultAlias; test via public API
	// Valid label values pass through
	var si domain.DeviceSystemInfo
	si.Set("x", "a1-b2_c3.d4")
	got := ComputeDefaultAlias(si, []string{"x"})
	assert.Equal(t, "a1-b2_c3.d4", got)
}
