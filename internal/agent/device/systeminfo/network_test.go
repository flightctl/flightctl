package systeminfo

import (
	_ "embed"
	"testing"

	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

// Test data for IPv4 routing table
var ipv4RouteNormal = `Iface	Destination	Gateway 	Flags	RefCnt	Use	Metric	Mask		MTU	Window	IRTT                                                       
eth0	00000000	0102030A	0003	0	0	0	00000000	0	0	0                                                                               
eth0	0000030A	00000000	0001	0	0	0	00FFFFFF	0	0	0                                                                               
`

var ipv4RouteLoopbackFirst = `Iface	Destination	Gateway 	Flags	RefCnt	Use	Metric	Mask		MTU	Window	IRTT                                                       
lo	00000000	00000000	0003	0	0	0	00000000	0	0	0                                                                               
eth0	00000000	0102030A	0003	0	0	0	00000000	0	0	0                                                                               
eth0	0000030A	00000000	0001	0	0	0	00FFFFFF	0	0	0                                                                               
`

var ipv4RouteOnlyLoopback = `Iface	Destination	Gateway 	Flags	RefCnt	Use	Metric	Mask		MTU	Window	IRTT                                                       
lo	00000000	00000000	0003	0	0	0	00000000	0	0	0                                                                               
lo	0100007F	00000000	0001	0	0	0	00FFFFFF	0	0	0                                                                               
`

var ipv4RouteEmpty = `Iface	Destination	Gateway 	Flags	RefCnt	Use	Metric	Mask		MTU	Window	IRTT                                                       
`

var ipv4RouteMalformed = `Iface	Destination
eth0	00000000
`

// Test data for IPv6 routing table
var ipv6RouteNormal = `00000000000000000000000000000000 00 00000000000000000000000000000000 00 00000000000000000000000000000000 00000064 00000000 00000000 00000003 eth0
20010db8000000000000000000000000 40 00000000000000000000000000000000 00 00000000000000000000000000000000 00000100 00000000 00000000 00000001 eth0
`

var ipv6RouteLoopbackFirst = `00000000000000000000000000000000 00 00000000000000000000000000000000 00 00000000000000000000000000000000 00000064 00000000 00000000 00000003 lo
00000000000000000000000000000000 00 00000000000000000000000000000000 00 00000000000000000000000000000000 00000064 00000000 00000000 00000003 eth0
20010db8000000000000000000000000 40 00000000000000000000000000000000 00 00000000000000000000000000000000 00000100 00000000 00000000 00000001 eth0
`

var ipv6RouteOnlyLoopback = `00000000000000000000000000000000 00 00000000000000000000000000000000 00 00000000000000000000000000000000 00000064 00000000 00000000 00000003 lo
00000000000000000000000000000001 80 00000000000000000000000000000000 00 00000000000000000000000000000000 00000100 00000000 00000000 00000001 lo
`

var ipv6RouteEmpty = ``

var ipv6RouteMalformed = `00000000000000000000000000000000 00 00000000000000000000000000000000
eth0
`

func TestGetDefaultRouteIPv4(t *testing.T) {
	tests := []struct {
		name        string
		routeData   string
		expectError bool
		expected    *DefaultRoute
	}{
		{
			name:        "normal IPv4 route",
			routeData:   ipv4RouteNormal,
			expectError: false,
			expected: &DefaultRoute{
				Interface: "eth0",
				Gateway:   "10.3.2.1", // 0102030A in hex = 10.3.2.1
				Family:    "ipv4",
			},
		},
		{
			name:        "loopback first, should skip to valid route",
			routeData:   ipv4RouteLoopbackFirst,
			expectError: false,
			expected: &DefaultRoute{
				Interface: "eth0",
				Gateway:   "10.3.2.1",
				Family:    "ipv4",
			},
		},
		{
			name:        "only loopback routes",
			routeData:   ipv4RouteOnlyLoopback,
			expectError: true,
			expected:    nil,
		},
		{
			name:        "empty route table",
			routeData:   ipv4RouteEmpty,
			expectError: true,
			expected:    nil,
		},
		{
			name:        "malformed route data",
			routeData:   ipv4RouteMalformed,
			expectError: true,
			expected:    nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			tmpDir := t.TempDir()
			rw := fileio.NewReadWriter()
			rw.SetRootdir(tmpDir)

			logger := log.NewPrefixLogger("test")
			logger.SetLevel(logrus.TraceLevel)

			// Create directory structure
			err := rw.MkdirAll("proc/net", fileio.DefaultDirectoryPermissions)
			require.NoError(err)

			// Write test data
			err = rw.WriteFile(ipv4RoutePath, []byte(tt.routeData), fileio.DefaultFilePermissions)
			require.NoError(err)

			// Test the function
			result, err := getDefaultRouteIPv4(logger, rw)

			if tt.expectError {
				require.Error(err)
				require.Nil(result)
			} else {
				require.NoError(err)
				require.NotNil(result)
				require.Equal(tt.expected.Interface, result.Interface)
				require.Equal(tt.expected.Gateway, result.Gateway)
				require.Equal(tt.expected.Family, result.Family)
			}
		})
	}
}

func TestGetDefaultRouteIPv6(t *testing.T) {
	tests := []struct {
		name        string
		routeData   string
		expectError bool
		expected    *DefaultRoute
	}{
		{
			name:        "normal IPv6 route",
			routeData:   ipv6RouteNormal,
			expectError: false,
			expected: &DefaultRoute{
				Interface: "eth0",
				Gateway:   "::",
				Family:    "ipv6",
			},
		},
		{
			name:        "loopback first, should skip to valid route",
			routeData:   ipv6RouteLoopbackFirst,
			expectError: false,
			expected: &DefaultRoute{
				Interface: "eth0",
				Gateway:   "::",
				Family:    "ipv6",
			},
		},
		{
			name:        "only loopback routes",
			routeData:   ipv6RouteOnlyLoopback,
			expectError: true,
			expected:    nil,
		},
		{
			name:        "empty route table",
			routeData:   ipv6RouteEmpty,
			expectError: true,
			expected:    nil,
		},
		{
			name:        "malformed route data",
			routeData:   ipv6RouteMalformed,
			expectError: true,
			expected:    nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			tmpDir := t.TempDir()
			rw := fileio.NewReadWriter()
			rw.SetRootdir(tmpDir)

			logger := log.NewPrefixLogger("test")
			logger.SetLevel(logrus.TraceLevel)

			// Create directory structure
			err := rw.MkdirAll("proc/net", fileio.DefaultDirectoryPermissions)
			require.NoError(err)

			// Write test data
			err = rw.WriteFile(ipv6RoutePath, []byte(tt.routeData), fileio.DefaultFilePermissions)
			require.NoError(err)

			// Test the function
			result, err := getDefaultRouteIPv6(logger, rw)

			if tt.expectError {
				require.Error(err)
				require.Nil(result)
			} else {
				require.NoError(err)
				require.NotNil(result)
				require.Equal(tt.expected.Interface, result.Interface)
				require.Equal(tt.expected.Gateway, result.Gateway)
				require.Equal(tt.expected.Family, result.Family)
			}
		})
	}
}

func TestGetDefaultRoute(t *testing.T) {
	tests := []struct {
		name         string
		ipv4Data     string
		ipv6Data     string
		expectError  bool
		expectedType string // "ipv4" or "ipv6"
	}{
		{
			name:         "IPv4 route available",
			ipv4Data:     ipv4RouteNormal,
			ipv6Data:     ipv6RouteEmpty,
			expectError:  false,
			expectedType: "ipv4",
		},
		{
			name:         "IPv4 fails, IPv6 available",
			ipv4Data:     ipv4RouteOnlyLoopback,
			ipv6Data:     ipv6RouteNormal,
			expectError:  false,
			expectedType: "ipv6",
		},
		{
			name:         "both available, should prefer IPv4",
			ipv4Data:     ipv4RouteNormal,
			ipv6Data:     ipv6RouteNormal,
			expectError:  false,
			expectedType: "ipv4",
		},
		{
			name:        "both fail",
			ipv4Data:    ipv4RouteOnlyLoopback,
			ipv6Data:    ipv6RouteOnlyLoopback,
			expectError: true,
		},
		{
			name:        "both empty",
			ipv4Data:    ipv4RouteEmpty,
			ipv6Data:    ipv6RouteEmpty,
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			tmpDir := t.TempDir()
			rw := fileio.NewReadWriter()
			rw.SetRootdir(tmpDir)

			logger := log.NewPrefixLogger("test")
			logger.SetLevel(logrus.TraceLevel)

			// Create directory structure
			err := rw.MkdirAll("proc/net", fileio.DefaultDirectoryPermissions)
			require.NoError(err)

			// Write test data
			err = rw.WriteFile(ipv4RoutePath, []byte(tt.ipv4Data), fileio.DefaultFilePermissions)
			require.NoError(err)
			err = rw.WriteFile(ipv6RoutePath, []byte(tt.ipv6Data), fileio.DefaultFilePermissions)
			require.NoError(err)

			// Test the function
			result, err := getDefaultRoute(logger, rw)

			if tt.expectError {
				require.Error(err)
				require.Nil(result)
			} else {
				require.NoError(err)
				require.NotNil(result)
				require.Equal(tt.expectedType, result.Family)
			}
		})
	}
}

func TestIsLoopback(t *testing.T) {
	tests := []struct {
		name          string
		interfaceName string
		expected      bool
	}{
		{
			name:          "lo interface",
			interfaceName: "lo",
			expected:      true,
		},
		{
			name:          "lo0 interface",
			interfaceName: "lo0",
			expected:      true,
		},
		{
			name:          "eth0 interface",
			interfaceName: "eth0",
			expected:      false,
		},
		{
			name:          "wlan0 interface",
			interfaceName: "wlan0",
			expected:      false,
		},
		{
			name:          "enp0s3 interface",
			interfaceName: "enp0s3",
			expected:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			result := isLoopback(tt.interfaceName)
			require.Equal(tt.expected, result)
		})
	}
}
