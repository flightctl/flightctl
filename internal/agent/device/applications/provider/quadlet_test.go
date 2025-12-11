package provider

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flightctl/flightctl/api/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/applications/lifecycle"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	testutil "github.com/flightctl/flightctl/test/util"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestNamespacedQuadlet(t *testing.T) {
	tests := []struct {
		name     string
		appID    string
		filename string
		expected string
	}{
		{
			name:     "container file",
			appID:    "myapp",
			filename: "web.container",
			expected: "myapp-web.container",
		},
		{
			name:     "volume file",
			appID:    "testapp",
			filename: "data.volume",
			expected: "testapp-data.volume",
		},
		{
			name:     "with dashes in name",
			appID:    "my-app",
			filename: "my-service.container",
			expected: "my-app-my-service.container",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := namespacedQuadlet(tt.appID, tt.filename)
			require.Equal(t, tt.expected, result)
		})
	}
}

func TestPrefixQuadletReference(t *testing.T) {
	tests := []struct {
		name     string
		value    string
		appID    string
		expected string
	}{
		{
			name:     "container not prefixed",
			value:    "web.container",
			appID:    "myapp",
			expected: "myapp-web.container",
		},
		{
			name:     "container already prefixed",
			value:    "myapp-web.container",
			appID:    "myapp",
			expected: "myapp-web.container",
		},
		{
			name:     "volume not prefixed",
			value:    "data.volume",
			appID:    "myapp",
			expected: "myapp-data.volume",
		},
		{
			name:     "network not prefixed",
			value:    "app-net.network",
			appID:    "myapp",
			expected: "myapp-app-net.network",
		},
		{
			name:     "image not prefixed",
			value:    "base.image",
			appID:    "myapp",
			expected: "myapp-base.image",
		},
		{
			name:     "pod not prefixed",
			value:    "services.pod",
			appID:    "myapp",
			expected: "myapp-services.pod",
		},
		{
			name:     "non-quadlet file",
			value:    "some-service.service",
			appID:    "myapp",
			expected: "some-service.service",
		},
		{
			name:     "regular string",
			value:    "nginx:latest",
			appID:    "myapp",
			expected: "nginx:latest",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := prefixQuadletReference(tt.value, tt.appID)
			require.Equal(t, tt.expected, result)
		})
	}
}

func TestDefaultServiceName(t *testing.T) {
	tests := []struct {
		name        string
		basename    string
		ext         string
		expected    string
		expectError bool
	}{
		{
			name:     "container extension",
			basename: "web",
			ext:      ".container",
			expected: "web.service",
		},
		{
			name:     "pod extension",
			basename: "services",
			ext:      ".pod",
			expected: "services-pod.service",
		},
		{
			name:     "volume extension",
			basename: "data",
			ext:      ".volume",
			expected: "data-volume.service",
		},
		{
			name:     "network extension",
			basename: "app-net",
			ext:      ".network",
			expected: "app-net-network.service",
		},
		{
			name:     "image extension",
			basename: "base",
			ext:      ".image",
			expected: "base-image.service",
		},
		{
			name:        "unknown extension",
			basename:    "foo",
			ext:         ".unknown",
			expected:    "",
			expectError: true,
		},
		{
			name:        "empty extension",
			basename:    "bar",
			ext:         "",
			expected:    "",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := defaultServiceName(tt.basename, tt.ext)
			if tt.expectError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, tt.expected, result)
		})
	}
}

func TestGetServiceName(t *testing.T) {
	tests := []struct {
		name        string
		files       map[string][]byte
		filePath    string
		ext         string
		defaultName string
		expected    string
		expectError bool
	}{
		{
			name: "non-pod quadlet returns default",
			files: map[string][]byte{
				"web.container": []byte(`[Container]
Image=nginx:latest
`),
			},
			filePath:    "web.container",
			ext:         ".container",
			defaultName: "web.service",
			expected:    "web.service",
		},
		{
			name: "pod without ServiceName returns default",
			files: map[string][]byte{
				"services.pod": []byte(`[Pod]
PodName=my-pod
Network=app-net
`),
			},
			filePath:    "services.pod",
			ext:         ".pod",
			defaultName: "services-pod.service",
			expected:    "services-pod.service",
		},
		{
			name: "pod with ServiceName returns custom name",
			files: map[string][]byte{
				"services.pod": []byte(`[Pod]
PodName=my-pod
ServiceName=my-custom-service.service
Network=app-net
`),
			},
			filePath:    "services.pod",
			ext:         ".pod",
			defaultName: "services-pod.service",
			expected:    "my-custom-service.service",
		},
		{
			name: "volume quadlet returns default",
			files: map[string][]byte{
				"data.volume": []byte(`[Volume]
`),
			},
			filePath:    "data.volume",
			ext:         ".volume",
			defaultName: "data-volume.service",
			expected:    "data-volume.service",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			rw := fileio.NewReadWriter()
			rw.SetRootdir(tmpDir)

			for filename, content := range tt.files {
				err := rw.WriteFile(filename, content, fileio.DefaultFilePermissions)
				require.NoError(t, err)
			}

			logger := log.NewPrefixLogger("test")
			q := &quadletInstaller{readWriter: rw, logger: logger}
			result, err := q.getServiceName(tt.filePath, tt.ext, tt.defaultName)
			if tt.expectError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				require.Equal(t, tt.expected, result)
			}
		})
	}
}

func TestUpdateSystemdReference(t *testing.T) {
	tests := []struct {
		name             string
		value            string
		appID            string
		quadletBasenames map[string]struct{}
		expected         string
	}{
		{
			name:  "service from our app",
			value: "web.service",
			appID: "myapp",
			quadletBasenames: map[string]struct{}{
				"web": {},
			},
			expected: "myapp-web.service",
		},
		{
			name:  "external service",
			value: "chronyd.service",
			appID: "myapp",
			quadletBasenames: map[string]struct{}{
				"web": {},
			},
			expected: "chronyd.service",
		},
		{
			name:  "direct quadlet reference",
			value: "db.container",
			appID: "myapp",
			quadletBasenames: map[string]struct{}{
				"web": {},
				"db":  {},
			},
			expected: "myapp-db.container",
		},
		{
			name:  "already prefixed service",
			value: "myapp-web.service",
			appID: "myapp",
			quadletBasenames: map[string]struct{}{
				"web": {},
			},
			expected: "myapp-web.service",
		},
		{
			name:  "volume reference",
			value: "data.volume",
			appID: "myapp",
			quadletBasenames: map[string]struct{}{
				"data": {},
			},
			expected: "myapp-data.volume",
		},
		{
			name:  "target from our app",
			value: "app-services.target",
			appID: "myapp",
			quadletBasenames: map[string]struct{}{
				"app-services": {},
			},
			expected: "myapp-app-services.target",
		},
		{
			name:  "system target",
			value: "multi-user.target",
			appID: "myapp",
			quadletBasenames: map[string]struct{}{
				"app-services": {},
			},
			expected: "multi-user.target",
		},
		{
			name:  "already prefixed target",
			value: "myapp-app-services.target",
			appID: "myapp",
			quadletBasenames: map[string]struct{}{
				"app-services": {},
			},
			expected: "myapp-app-services.target",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q := &quadletInstaller{appID: tt.appID}
			result := q.updateSystemdReference(tt.value, tt.quadletBasenames)
			require.Equal(t, tt.expected, result)
		})
	}
}

func TestUpdateSpaceSeparatedReferences(t *testing.T) {
	tests := []struct {
		name             string
		value            string
		appID            string
		quadletBasenames map[string]struct{}
		expected         string
	}{
		{
			name:  "single reference",
			value: "web.service",
			appID: "myapp",
			quadletBasenames: map[string]struct{}{
				"web": {},
			},
			expected: "myapp-web.service",
		},
		{
			name:  "multiple app services",
			value: "web.service db.service",
			appID: "myapp",
			quadletBasenames: map[string]struct{}{
				"web": {},
				"db":  {},
			},
			expected: "myapp-web.service myapp-db.service",
		},
		{
			name:  "mixed app and external services",
			value: "web.service chronyd.service db.service",
			appID: "myapp",
			quadletBasenames: map[string]struct{}{
				"web": {},
				"db":  {},
			},
			expected: "myapp-web.service chronyd.service myapp-db.service",
		},
		{
			name:  "quadlet references",
			value: "db.container data.volume",
			appID: "myapp",
			quadletBasenames: map[string]struct{}{
				"db":   {},
				"data": {},
			},
			expected: "myapp-db.container myapp-data.volume",
		},
		{
			name:  "empty string",
			value: "",
			appID: "myapp",
			quadletBasenames: map[string]struct{}{
				"web": {},
			},
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q := &quadletInstaller{appID: tt.appID}
			result := q.updateSpaceSeparatedReferences(tt.value, tt.quadletBasenames)
			require.Equal(t, tt.expected, result)
		})
	}
}

func TestUpdateMountValue(t *testing.T) {
	tests := []struct {
		name     string
		value    string
		appID    string
		expected string
	}{
		{
			name:     "volume mount",
			value:    "type=volume,source=data.volume,destination=/data",
			appID:    "myapp",
			expected: "type=volume,source=myapp-data.volume,destination=/data",
		},
		{
			name:     "image mount",
			value:    "type=image,source=config.image,destination=/config",
			appID:    "myapp",
			expected: "type=image,source=myapp-config.image,destination=/config",
		},
		{
			name:     "bind mount",
			value:    "type=bind,source=/host/path,destination=/container",
			appID:    "myapp",
			expected: "type=bind,source=/host/path,destination=/container",
		},
		{
			name:     "already prefixed volume",
			value:    "type=volume,source=myapp-data.volume,destination=/data",
			appID:    "myapp",
			expected: "type=volume,source=myapp-data.volume,destination=/data",
		},
		{
			name:     "volume mount with options",
			value:    "type=volume,source=data.volume,destination=/data,ro=true",
			appID:    "myapp",
			expected: "type=volume,source=myapp-data.volume,destination=/data,ro=true",
		},
		{
			name:     "source before type",
			value:    "source=data.volume,destination=/data,type=volume",
			appID:    "myapp",
			expected: "source=myapp-data.volume,destination=/data,type=volume",
		},
		{
			name:     "destination first with image",
			value:    "destination=/config,type=image,source=config.image",
			appID:    "myapp",
			expected: "destination=/config,type=image,source=myapp-config.image",
		},
		{
			name:     "no type specified defaults to volume",
			value:    "source=data.volume,destination=/data",
			appID:    "myapp",
			expected: "source=myapp-data.volume,destination=/data",
		},
		{
			name:     "using src instead of source",
			value:    "src=data.volume,destination=/data",
			appID:    "myapp",
			expected: "src=myapp-data.volume,destination=/data",
		},
		{
			name:     "named volume mount without extension",
			value:    "type=volume,source=my-data,destination=/data",
			appID:    "myapp",
			expected: "type=volume,source=myapp-my-data,destination=/data",
		},
		{
			name:     "named volume with src shorthand",
			value:    "type=volume,src=cache,dst=/cache",
			appID:    "myapp",
			expected: "type=volume,src=myapp-cache,dst=/cache",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := updateMountValue(tt.value, tt.appID)
			require.NoError(t, err)
			require.Equal(t, tt.expected, result)
		})
	}
}

func TestUpdateVolumeValue(t *testing.T) {
	tests := []struct {
		name     string
		value    string
		appID    string
		expected string
	}{
		{
			name:     "quadlet volume simple",
			value:    "data.volume:/data",
			appID:    "myapp",
			expected: "myapp-data.volume:/data",
		},
		{
			name:     "quadlet volume with options",
			value:    "data.volume:/data:ro",
			appID:    "myapp",
			expected: "myapp-data.volume:/data:ro",
		},
		{
			name:     "host path volume",
			value:    "/host/path:/container",
			appID:    "myapp",
			expected: "/host/path:/container",
		},
		{
			name:     "already prefixed",
			value:    "myapp-data.volume:/data",
			appID:    "myapp",
			expected: "myapp-data.volume:/data",
		},
		{
			name:     "volume only source",
			value:    "data.volume",
			appID:    "myapp",
			expected: "myapp-data.volume",
		},
		{
			name:     "named volume without extension",
			value:    "my-data:/data",
			appID:    "myapp",
			expected: "myapp-my-data:/data",
		},
		{
			name:     "named volume with ro option",
			value:    "cache:/cache:ro",
			appID:    "myapp",
			expected: "myapp-cache:/cache:ro",
		},
		{
			name:     "anonymous volume single path",
			value:    "/data",
			appID:    "myapp",
			expected: "/data",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := updateVolumeValue(tt.value, tt.appID)
			require.NoError(t, err)
			require.Equal(t, tt.expected, result)
		})
	}
}

func TestInstallQuadlet(t *testing.T) {
	tests := []struct {
		name              string
		files             map[string][]byte
		appID             string
		expectedFiles     []string
		expectedDropIns   map[string]bool
		checkFileContents map[string]func(*testing.T, []byte)
	}{
		{
			name: "simple container with no references",
			files: map[string][]byte{
				"web.container": []byte(`[Container]
Image=nginx:latest
`),
			},
			appID: "myapp",
			expectedFiles: []string{
				"myapp-web.container",
				"myapp-flightctl-quadlet-app.target",
				"myapp-.container.d/99-flightctl.conf",
			},
			expectedDropIns: map[string]bool{
				"myapp-.container.d": true,
			},
			checkFileContents: map[string]func(*testing.T, []byte){
				"myapp-web.container": func(t *testing.T, content []byte) {
					require.Contains(t, string(content), "[Container]")
					require.Contains(t, string(content), "nginx:latest")
				},
				"myapp-flightctl-quadlet-app.target": func(t *testing.T, content []byte) {
					contentStr := string(content)
					require.Contains(t, contentStr, "[Unit]")
					require.Contains(t, contentStr, "Wants=myapp-web.service")
					require.Contains(t, contentStr, "After=myapp-web.service")
				},
				"myapp-.container.d/99-flightctl.conf": func(t *testing.T, content []byte) {
					contentStr := string(content)
					require.Contains(t, contentStr, "[Unit]")
					require.Contains(t, contentStr, "PartOf=myapp-flightctl-quadlet-app.target")
					require.Contains(t, contentStr, "[Container]")
					require.Contains(t, contentStr, "io.flightctl.quadlet.project=myapp")
				},
			},
		},
		{
			name: "container with volume and network references",
			files: map[string][]byte{
				"web.container": []byte(`[Container]
Image=nginx:latest
Volume=data.volume:/data
Network=app-net.network
`),
				"data.volume": []byte(`[Volume]
`),
				"app-net.network": []byte(`[Network]
`),
			},
			appID: "myapp",
			expectedFiles: []string{
				"myapp-web.container",
				"myapp-data.volume",
				"myapp-app-net.network",
				"myapp-flightctl-quadlet-app.target",
				"myapp-.container.d/99-flightctl.conf",
				"myapp-.volume.d/99-flightctl.conf",
				"myapp-.network.d/99-flightctl.conf",
			},
			checkFileContents: map[string]func(*testing.T, []byte){
				"myapp-web.container": func(t *testing.T, content []byte) {
					require.Contains(t, string(content), "myapp-data.volume:/data")
					require.Contains(t, string(content), "myapp-app-net.network")
				},
				"myapp-flightctl-quadlet-app.target": func(t *testing.T, content []byte) {
					contentStr := string(content)
					require.Contains(t, contentStr, "Wants=myapp-web.service")
					require.Contains(t, contentStr, "Wants=myapp-data-volume.service")
					require.Contains(t, contentStr, "Wants=myapp-app-net-network.service")
				},
			},
		},
		{
			name: "container with .env file",
			files: map[string][]byte{
				"web.container": []byte(`[Container]
Image=nginx:latest
`),
				".env": []byte("ENV_VAR=value\n"),
			},
			appID: "myapp",
			expectedFiles: []string{
				"myapp-web.container",
				"myapp-flightctl-quadlet-app.target",
				".env",
				"myapp-.container.d/99-flightctl.conf",
			},
			checkFileContents: map[string]func(*testing.T, []byte){
				"myapp-.container.d/99-flightctl.conf": func(t *testing.T, content []byte) {
					contentStr := string(content)
					require.Contains(t, contentStr, "PartOf=myapp-flightctl-quadlet-app.target")
					require.Contains(t, contentStr, "EnvironmentFile")
					require.Contains(t, contentStr, ".env")
				},
			},
		},
		{
			name: "container with unit dependencies",
			files: map[string][]byte{
				"web.container": []byte(`[Unit]
After=db.container chronyd.service

[Container]
Image=nginx:latest
`),
				"db.container": []byte(`[Container]
Image=postgres:latest
`),
			},
			appID: "myapp",
			expectedFiles: []string{
				"myapp-web.container",
				"myapp-db.container",
				"myapp-flightctl-quadlet-app.target",
			},
			checkFileContents: map[string]func(*testing.T, []byte){
				"myapp-web.container": func(t *testing.T, content []byte) {
					require.Contains(t, string(content), "myapp-db.container chronyd.service")
				},
				"myapp-flightctl-quadlet-app.target": func(t *testing.T, content []byte) {
					contentStr := string(content)
					require.Contains(t, contentStr, "Wants=myapp-web.service")
					require.Contains(t, contentStr, "Wants=myapp-db.service")
				},
			},
		},
		{
			name: "already namespaced files",
			files: map[string][]byte{
				"myapp-web.container": []byte(`[Container]
Image=nginx:latest
Volume=myapp-data.volume:/data
`),
				"myapp-data.volume": []byte(`[Volume]
`),
			},
			appID: "myapp",
			expectedFiles: []string{
				"myapp-web.container",
				"myapp-data.volume",
				"myapp-flightctl-quadlet-app.target",
			},
			checkFileContents: map[string]func(*testing.T, []byte){
				"myapp-web.container": func(t *testing.T, content []byte) {
					require.Contains(t, string(content), "myapp-data.volume:/data")
				},
				"myapp-flightctl-quadlet-app.target": func(t *testing.T, content []byte) {
					contentStr := string(content)
					require.Contains(t, contentStr, "Wants=myapp-web.service")
					require.Contains(t, contentStr, "Wants=myapp-data-volume.service")
				},
			},
		},
		{
			name: "all reference types",
			files: map[string][]byte{
				"app.container": []byte(`[Unit]
After=db.service init.container chronyd.service
Requires=db.service network.target
Before=services.pod

[Install]
WantedBy=multi-user.target default.target

[Container]
Image=base.image
Network=app-net.network
Pod=services.pod
Volume=data.volume:/data
Volume=logs.volume:/logs:ro
Mount=type=volume,source=cache.volume,destination=/cache
Mount=type=image,source=config.image,destination=/config
Mount=type=bind,source=/host/path,destination=/bind
`),
				"db.container": []byte(`[Unit]
After=network.target

[Container]
Image=postgres:latest
Volume=data.volume:/var/lib/postgresql
`),
				"init.container": []byte(`[Container]
Image=alpine:latest
`),
				"services.pod": []byte(`[Unit]
After=app-net.network

[Pod]
Network=app-net.network
Volume=shared.volume:/shared
`),
				"data.volume": []byte(`[Volume]
`),
				"logs.volume": []byte(`[Volume]
`),
				"cache.volume": []byte(`[Volume]
`),
				"shared.volume": []byte(`[Volume]
`),
				"cache-vol.volume": []byte(`[Volume]
Image=vol-base.image
`),
				"base.image": []byte(`[Image]
`),
				"config.image": []byte(`[Image]
`),
				"vol-base.image": []byte(`[Image]
`),
				"app-net.network": []byte(`[Network]
`),
				".env": []byte("ENV_VAR=value\n"),
			},
			appID: "myapp",
			expectedFiles: []string{
				"myapp-app.container",
				"myapp-db.container",
				"myapp-init.container",
				"myapp-services.pod",
				"myapp-data.volume",
				"myapp-logs.volume",
				"myapp-cache.volume",
				"myapp-shared.volume",
				"myapp-cache-vol.volume",
				"myapp-base.image",
				"myapp-config.image",
				"myapp-vol-base.image",
				"myapp-app-net.network",
				"myapp-flightctl-quadlet-app.target",
				".env",
				"myapp-.container.d/99-flightctl.conf",
				"myapp-.pod.d/99-flightctl.conf",
				"myapp-.volume.d/99-flightctl.conf",
				"myapp-.image.d/99-flightctl.conf",
				"myapp-.network.d/99-flightctl.conf",
			},
			checkFileContents: map[string]func(*testing.T, []byte){
				"myapp-app.container": func(t *testing.T, content []byte) {
					contentStr := string(content)
					// Unit section - mix of quadlet services, direct quadlets, and external services
					require.Contains(t, contentStr, "myapp-db.service")
					require.Contains(t, contentStr, "myapp-init.container")
					require.Contains(t, contentStr, "chronyd.service")
					require.Contains(t, contentStr, "myapp-services.pod")
					// Install section
					require.Contains(t, contentStr, "multi-user.target")
					// Container section - all reference types
					require.Contains(t, contentStr, "myapp-base.image")
					require.Contains(t, contentStr, "myapp-app-net.network")
					require.Contains(t, contentStr, "myapp-services.pod")
					require.Contains(t, contentStr, "myapp-data.volume:/data")
					require.Contains(t, contentStr, "myapp-logs.volume:/logs:ro")
					require.Contains(t, contentStr, "source=myapp-cache.volume")
					require.Contains(t, contentStr, "source=myapp-config.image")
					// Bind mount should NOT be prefixed
					require.Contains(t, contentStr, "source=/host/path")
				},
				"myapp-db.container": func(t *testing.T, content []byte) {
					require.Contains(t, string(content), "myapp-data.volume:/var/lib/postgresql")
				},
				"myapp-services.pod": func(t *testing.T, content []byte) {
					contentStr := string(content)
					require.Contains(t, contentStr, "myapp-app-net.network")
					require.Contains(t, contentStr, "myapp-shared.volume:/shared")
				},
				"myapp-cache-vol.volume": func(t *testing.T, content []byte) {
					require.Contains(t, string(content), "myapp-vol-base.image")
				},
				"myapp-flightctl-quadlet-app.target": func(t *testing.T, content []byte) {
					contentStr := string(content)
					require.Contains(t, contentStr, "Wants=myapp-app.service")
					require.Contains(t, contentStr, "Wants=myapp-db.service")
					require.Contains(t, contentStr, "Wants=myapp-init.service")
					require.Contains(t, contentStr, "Wants=myapp-services-pod.service")
					require.Contains(t, contentStr, "Wants=myapp-data-volume.service")
					require.Contains(t, contentStr, "Wants=myapp-logs-volume.service")
					require.Contains(t, contentStr, "Wants=myapp-cache-volume.service")
					require.Contains(t, contentStr, "Wants=myapp-shared-volume.service")
					require.Contains(t, contentStr, "Wants=myapp-cache-vol-volume.service")
					require.Contains(t, contentStr, "Wants=myapp-base-image.service")
					require.Contains(t, contentStr, "Wants=myapp-config-image.service")
					require.Contains(t, contentStr, "Wants=myapp-vol-base-image.service")
					require.Contains(t, contentStr, "Wants=myapp-app-net-network.service")
				},
				"myapp-.container.d/99-flightctl.conf": func(t *testing.T, content []byte) {
					contentStr := string(content)
					require.Contains(t, contentStr, "PartOf=myapp-flightctl-quadlet-app.target")
					require.Contains(t, contentStr, "io.flightctl.quadlet.project=myapp")
					require.Contains(t, contentStr, "EnvironmentFile")
					require.Contains(t, contentStr, ".env")
				},
			},
		},
		{
			name: "simple drop-in",
			files: map[string][]byte{
				"web.container": []byte(`[Container]
Image=nginx:latest
`),
				"web.container.d/10-custom.conf": []byte(`[Container]
Network=backend.network
`),
				"backend.network": []byte(`[Network]
`),
			},
			appID: "myapp",
			expectedFiles: []string{
				"myapp-web.container",
				"myapp-backend.network",
				"myapp-flightctl-quadlet-app.target",
				"myapp-web.container.d/10-custom.conf",
				"myapp-.container.d/99-flightctl.conf",
				"myapp-.network.d/99-flightctl.conf",
			},
			checkFileContents: map[string]func(*testing.T, []byte){
				"myapp-web.container.d/10-custom.conf": func(t *testing.T, content []byte) {
					require.Contains(t, string(content), "myapp-backend.network")
				},
				"myapp-flightctl-quadlet-app.target": func(t *testing.T, content []byte) {
					contentStr := string(content)
					require.Contains(t, contentStr, "Wants=myapp-web.service")
					require.Contains(t, contentStr, "Wants=myapp-backend-network.service")
				},
			},
		},
		{
			name: "top-level drop-in",
			files: map[string][]byte{
				"web.container": []byte(`[Container]
Image=nginx:latest
`),
				"container.d/05-base.conf": []byte(`[Container]
Volume=logs.volume:/logs
`),
				"logs.volume": []byte(`[Volume]
`),
			},
			appID: "myapp",
			expectedFiles: []string{
				"myapp-web.container",
				"myapp-logs.volume",
				"myapp-flightctl-quadlet-app.target",
				"myapp-.container.d/05-base.conf",
				"myapp-.container.d/99-flightctl.conf",
				"myapp-.volume.d/99-flightctl.conf",
			},
			checkFileContents: map[string]func(*testing.T, []byte){
				"myapp-.container.d/05-base.conf": func(t *testing.T, content []byte) {
					require.Contains(t, string(content), "myapp-logs.volume:/logs")
				},
				"myapp-flightctl-quadlet-app.target": func(t *testing.T, content []byte) {
					contentStr := string(content)
					require.Contains(t, contentStr, "Wants=myapp-web.service")
					require.Contains(t, contentStr, "Wants=myapp-logs-volume.service")
				},
			},
		},
		{
			name: "hierarchical drop-ins",
			files: map[string][]byte{
				"foo-bar.container": []byte(`[Container]
Image=test:latest
`),
				"container.d/01-global.conf": []byte(`[Unit]
After=network.target
`),
				"foo-.container.d/02-foo.conf": []byte(`[Container]
Network=foo-net.network
`),
				"foo-bar.container.d/03-specific.conf": []byte(`[Container]
Volume=data.volume:/data
`),
				"foo-net.network": []byte(`[Network]
`),
				"data.volume": []byte(`[Volume]
`),
			},
			appID: "myapp",
			expectedFiles: []string{
				"myapp-foo-bar.container",
				"myapp-foo-net.network",
				"myapp-data.volume",
				"myapp-flightctl-quadlet-app.target",
				"myapp-.container.d/01-global.conf",
				"myapp-foo-.container.d/02-foo.conf",
				"myapp-foo-bar.container.d/03-specific.conf",
				"myapp-.container.d/99-flightctl.conf",
				"myapp-.network.d/99-flightctl.conf",
				"myapp-.volume.d/99-flightctl.conf",
			},
			checkFileContents: map[string]func(*testing.T, []byte){
				"myapp-.container.d/01-global.conf": func(t *testing.T, content []byte) {
					require.Contains(t, string(content), "[Unit]")
					require.Contains(t, string(content), "network.target")
				},
				"myapp-foo-.container.d/02-foo.conf": func(t *testing.T, content []byte) {
					require.Contains(t, string(content), "myapp-foo-net.network")
				},
				"myapp-foo-bar.container.d/03-specific.conf": func(t *testing.T, content []byte) {
					require.Contains(t, string(content), "myapp-data.volume:/data")
				},
				"myapp-flightctl-quadlet-app.target": func(t *testing.T, content []byte) {
					contentStr := string(content)
					require.Contains(t, contentStr, "Wants=myapp-foo-bar.service")
					require.Contains(t, contentStr, "Wants=myapp-foo-net-network.service")
					require.Contains(t, contentStr, "Wants=myapp-data-volume.service")
				},
				"myapp-.container.d/99-flightctl.conf": func(t *testing.T, content []byte) {
					contentStr := string(content)
					require.Contains(t, contentStr, "PartOf=myapp-flightctl-quadlet-app.target")
				},
			},
		},
		{
			name: "drop-in with multiple references",
			files: map[string][]byte{
				"app.container": []byte(`[Container]
Image=app:latest
`),
				"app.container.d/10-config.conf": []byte(`[Unit]
After=db.container init.container

[Container]
Network=app-net.network
Volume=data.volume:/data
Volume=logs.volume:/logs
Mount=type=volume,source=cache.volume,destination=/cache
`),
				"db.container": []byte(`[Container]
Image=db:latest
`),
				"init.container": []byte(`[Container]
Image=init:latest
`),
				"app-net.network": []byte(`[Network]
`),
				"data.volume": []byte(`[Volume]
`),
				"logs.volume": []byte(`[Volume]
`),
				"cache.volume": []byte(`[Volume]
`),
			},
			appID: "myapp",
			expectedFiles: []string{
				"myapp-app.container",
				"myapp-db.container",
				"myapp-init.container",
				"myapp-app-net.network",
				"myapp-data.volume",
				"myapp-logs.volume",
				"myapp-cache.volume",
				"myapp-flightctl-quadlet-app.target",
				"myapp-app.container.d/10-config.conf",
				"myapp-.container.d/99-flightctl.conf",
				"myapp-.network.d/99-flightctl.conf",
				"myapp-.volume.d/99-flightctl.conf",
			},
			checkFileContents: map[string]func(*testing.T, []byte){
				"myapp-app.container.d/10-config.conf": func(t *testing.T, content []byte) {
					contentStr := string(content)
					require.Contains(t, contentStr, "myapp-db.container")
					require.Contains(t, contentStr, "myapp-init.container")
					require.Contains(t, contentStr, "myapp-app-net.network")
					require.Contains(t, contentStr, "myapp-data.volume:/data")
					require.Contains(t, contentStr, "myapp-logs.volume:/logs")
					require.Contains(t, contentStr, "source=myapp-cache.volume")
				},
				"myapp-flightctl-quadlet-app.target": func(t *testing.T, content []byte) {
					contentStr := string(content)
					require.Contains(t, contentStr, "Wants=myapp-app.service")
					require.Contains(t, contentStr, "Wants=myapp-db.service")
					require.Contains(t, contentStr, "Wants=myapp-init.service")
					require.Contains(t, contentStr, "Wants=myapp-app-net-network.service")
					require.Contains(t, contentStr, "Wants=myapp-data-volume.service")
					require.Contains(t, contentStr, "Wants=myapp-logs-volume.service")
					require.Contains(t, contentStr, "Wants=myapp-cache-volume.service")
				},
				"myapp-.container.d/99-flightctl.conf": func(t *testing.T, content []byte) {
					require.Contains(t, string(content), "PartOf=myapp-flightctl-quadlet-app.target")
				},
			},
		},
		{
			name: "target file with quadlet service references",
			files: map[string][]byte{
				"web.container": []byte(`[Container]
Image=nginx:latest
`),
				"db.container": []byte(`[Container]
Image=postgres:latest
`),
				"app-services.target": []byte(`[Unit]
Description=Application services target
Requires=web.service db.service
After=web.service db.service network.target

[Install]
WantedBy=multi-user.target
`),
			},
			appID: "myapp",
			expectedFiles: []string{
				"myapp-web.container",
				"myapp-db.container",
				"myapp-app-services.target",
				"myapp-flightctl-quadlet-app.target",
				"myapp-.container.d/99-flightctl.conf",
			},
			checkFileContents: map[string]func(*testing.T, []byte){
				"myapp-app-services.target": func(t *testing.T, content []byte) {
					contentStr := string(content)
					// Quadlet services should be namespaced
					require.Contains(t, contentStr, "myapp-web.service")
					require.Contains(t, contentStr, "myapp-db.service")
					// External system targets should NOT be namespaced
					require.Contains(t, contentStr, "network.target")
					require.Contains(t, contentStr, "multi-user.target")
				},
				"myapp-flightctl-quadlet-app.target": func(t *testing.T, content []byte) {
					contentStr := string(content)
					require.Contains(t, contentStr, "Wants=myapp-web.service")
					require.Contains(t, contentStr, "Wants=myapp-db.service")
				},
				"myapp-.container.d/99-flightctl.conf": func(t *testing.T, content []byte) {
					require.Contains(t, string(content), "PartOf=myapp-flightctl-quadlet-app.target")
				},
			},
		},
		{
			name: "service with target references in Unit and Install sections",
			files: map[string][]byte{
				"web.container": []byte(`[Container]
Image=nginx:latest

[Unit]
After=app-services.target

[Install]
WantedBy=app-services.target
`),
				"app-services.target": []byte(`[Unit]
Description=Application services target

[Install]
WantedBy=multi-user.target
`),
			},
			appID: "myapp",
			expectedFiles: []string{
				"myapp-web.container",
				"myapp-app-services.target",
				"myapp-flightctl-quadlet-app.target",
				"myapp-.container.d/99-flightctl.conf",
			},
			checkFileContents: map[string]func(*testing.T, []byte){
				"myapp-web.container": func(t *testing.T, content []byte) {
					contentStr := string(content)
					// App-specific target should be namespaced
					require.Contains(t, contentStr, "After=myapp-app-services.target")
					require.Contains(t, contentStr, "WantedBy=myapp-app-services.target")
				},
				"myapp-app-services.target": func(t *testing.T, content []byte) {
					contentStr := string(content)
					// System target should NOT be namespaced
					require.Contains(t, contentStr, "WantedBy=multi-user.target")
				},
				"myapp-flightctl-quadlet-app.target": func(t *testing.T, content []byte) {
					contentStr := string(content)
					require.Contains(t, contentStr, "Wants=myapp-web.service")
				},
				"myapp-.container.d/99-flightctl.conf": func(t *testing.T, content []byte) {
					require.Contains(t, string(content), "PartOf=myapp-flightctl-quadlet-app.target")
				},
			},
		},
		{
			name: "container with named volumes and custom networks",
			files: map[string][]byte{
				"web.container": []byte(`[Container]
Image=nginx:latest
ContainerName=my-web-container
Volume=app-data:/data
Volume=cache:/cache:ro
Mount=type=volume,source=logs,destination=/logs
Network=backend
`),
				"db.container": []byte(`[Container]
Image=postgres:latest
ContainerName=database
Volume=db-data:/var/lib/postgresql
Network=backend
`),
				"backend.network": []byte(`[Network]
NetworkName=app-backend
`),
				"cache.volume": []byte(`[Volume]
VolumeName=app-cache
`),
			},
			appID: "myapp",
			expectedFiles: []string{
				"myapp-web.container",
				"myapp-db.container",
				"myapp-backend.network",
				"myapp-cache.volume",
				"myapp-flightctl-quadlet-app.target",
				"myapp-.container.d/99-flightctl.conf",
				"myapp-.network.d/99-flightctl.conf",
				"myapp-.volume.d/99-flightctl.conf",
			},
			checkFileContents: map[string]func(*testing.T, []byte){
				"myapp-web.container": func(t *testing.T, content []byte) {
					contentStr := string(content)
					require.Contains(t, contentStr, "ContainerName=myapp-my-web-container")
					require.Contains(t, contentStr, "Volume=myapp-app-data:/data")
					require.Contains(t, contentStr, "Volume=myapp-cache:/cache:ro")
					require.Contains(t, contentStr, "source=myapp-logs")
					require.Contains(t, contentStr, "Network=myapp-backend")
				},
				"myapp-db.container": func(t *testing.T, content []byte) {
					contentStr := string(content)
					require.Contains(t, contentStr, "ContainerName=myapp-database")
					require.Contains(t, contentStr, "Volume=myapp-db-data:/var/lib/postgresql")
					require.Contains(t, contentStr, "Network=myapp-backend")
				},
				"myapp-backend.network": func(t *testing.T, content []byte) {
					require.Contains(t, string(content), "NetworkName=myapp-app-backend")
				},
				"myapp-cache.volume": func(t *testing.T, content []byte) {
					require.Contains(t, string(content), "VolumeName=myapp-app-cache")
				},
				"myapp-flightctl-quadlet-app.target": func(t *testing.T, content []byte) {
					contentStr := string(content)
					require.Contains(t, contentStr, "Wants=myapp-web.service")
					require.Contains(t, contentStr, "Wants=myapp-db.service")
					require.Contains(t, contentStr, "Wants=myapp-backend-network.service")
					require.Contains(t, contentStr, "Wants=myapp-cache-volume.service")
				},
				"myapp-.container.d/99-flightctl.conf": func(t *testing.T, content []byte) {
					require.Contains(t, string(content), "PartOf=myapp-flightctl-quadlet-app.target")
				},
			},
		},
		{
			name: "container with built-in network modes",
			files: map[string][]byte{
				"web.container": []byte(`[Container]
Image=nginx:latest
Network=host
`),
				"app.container": []byte(`[Container]
Image=app:latest
Network=bridge
`),
				"test.container": []byte(`[Container]
Image=test:latest
Network=none
`),
			},
			appID: "myapp",
			expectedFiles: []string{
				"myapp-web.container",
				"myapp-app.container",
				"myapp-test.container",
				"myapp-flightctl-quadlet-app.target",
				"myapp-.container.d/99-flightctl.conf",
			},
			checkFileContents: map[string]func(*testing.T, []byte){
				"myapp-web.container": func(t *testing.T, content []byte) {
					require.Contains(t, string(content), "Network=host")
					require.NotContains(t, string(content), "myapp-host")
				},
				"myapp-app.container": func(t *testing.T, content []byte) {
					require.Contains(t, string(content), "Network=bridge")
					require.NotContains(t, string(content), "myapp-bridge")
				},
				"myapp-test.container": func(t *testing.T, content []byte) {
					require.Contains(t, string(content), "Network=none")
					require.NotContains(t, string(content), "myapp-none")
				},
				"myapp-flightctl-quadlet-app.target": func(t *testing.T, content []byte) {
					contentStr := string(content)
					require.Contains(t, contentStr, "Wants=myapp-web.service")
					require.Contains(t, contentStr, "Wants=myapp-app.service")
					require.Contains(t, contentStr, "Wants=myapp-test.service")
				},
				"myapp-.container.d/99-flightctl.conf": func(t *testing.T, content []byte) {
					require.Contains(t, string(content), "PartOf=myapp-flightctl-quadlet-app.target")
				},
			},
		},
		{
			name: "pod with custom name and resources",
			files: map[string][]byte{
				"services.pod": []byte(`[Pod]
PodName=app-services-pod
Network=app-net
Volume=shared:/shared
`),
				"app-net.network": []byte(`[Network]
NetworkName=application-network
`),
			},
			appID: "myapp",
			expectedFiles: []string{
				"myapp-services.pod",
				"myapp-app-net.network",
				"myapp-flightctl-quadlet-app.target",
				"myapp-.pod.d/99-flightctl.conf",
				"myapp-.network.d/99-flightctl.conf",
			},
			checkFileContents: map[string]func(*testing.T, []byte){
				"myapp-services.pod": func(t *testing.T, content []byte) {
					contentStr := string(content)
					require.Contains(t, contentStr, "PodName=myapp-app-services-pod")
					require.Contains(t, contentStr, "Network=myapp-app-net")
					require.Contains(t, contentStr, "Volume=myapp-shared:/shared")
				},
				"myapp-app-net.network": func(t *testing.T, content []byte) {
					require.Contains(t, string(content), "NetworkName=myapp-application-network")
				},
				"myapp-flightctl-quadlet-app.target": func(t *testing.T, content []byte) {
					contentStr := string(content)
					require.Contains(t, contentStr, "Wants=myapp-services-pod.service")
					require.Contains(t, contentStr, "Wants=myapp-app-net-network.service")
				},
				"myapp-.pod.d/99-flightctl.conf": func(t *testing.T, content []byte) {
					require.Contains(t, string(content), "PartOf=myapp-flightctl-quadlet-app.target")
				},
			},
		},
		{
			name: "pod with custom ServiceName",
			files: map[string][]byte{
				"services.pod": []byte(`[Pod]
PodName=app-services-pod
ServiceName=my-custom-service.service
Network=app-net
`),
				"app-net.network": []byte(`[Network]
`),
			},
			appID: "myapp",
			expectedFiles: []string{
				"myapp-services.pod",
				"myapp-app-net.network",
				"myapp-flightctl-quadlet-app.target",
				"myapp-.pod.d/99-flightctl.conf",
				"myapp-.network.d/99-flightctl.conf",
			},
			checkFileContents: map[string]func(*testing.T, []byte){
				"myapp-services.pod": func(t *testing.T, content []byte) {
					contentStr := string(content)
					require.Contains(t, contentStr, "ServiceName=myapp-my-custom-service.service")
				},
				"myapp-flightctl-quadlet-app.target": func(t *testing.T, content []byte) {
					contentStr := string(content)
					require.Contains(t, contentStr, "Wants=myapp-my-custom-service.service")
					require.Contains(t, contentStr, "After=myapp-my-custom-service.service")
					require.Contains(t, contentStr, "Wants=myapp-app-net-network.service")
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			rw := fileio.NewReadWriter()
			rw.SetRootdir(tmpDir)

			// Create the systemd unit directory for target file copying
			err := rw.MkdirAll(lifecycle.QuadletTargetPath, fileio.DefaultDirectoryPermissions)
			require.NoError(t, err)

			for filename, content := range tt.files {
				// Create parent directory if file is in a subdirectory
				dir := filepath.Dir(filename)
				if dir != "." && dir != "/" {
					err := rw.MkdirAll(dir, fileio.DefaultDirectoryPermissions)
					require.NoError(t, err)
				}
				err := rw.WriteFile(filename, content, fileio.DefaultFilePermissions)
				require.NoError(t, err)
			}

			logger := log.NewPrefixLogger("test")
			err = installQuadlet(rw, logger, "/", tt.appID)
			require.NoError(t, err)

			for _, expectedFile := range tt.expectedFiles {
				content, err := rw.ReadFile(expectedFile)
				require.NoError(t, err, "expected file %s to exist", expectedFile)
				require.NotEmpty(t, content, "expected file %s to have content", expectedFile)

				if checkFn, ok := tt.checkFileContents[expectedFile]; ok {
					checkFn(t, content)
				}
			}

			err = installQuadlet(rw, logger, "/", tt.appID)
			require.NoError(t, err, "second call to installQuadlet should succeed (idempotency)")

			for _, expectedFile := range tt.expectedFiles {
				content, err := rw.ReadFile(expectedFile)
				require.NoError(t, err, "expected file %s to still exist after second run", expectedFile)
				require.NotEmpty(t, content, "expected file %s to still have content after second run", expectedFile)

				if checkFn, ok := tt.checkFileContents[expectedFile]; ok {
					checkFn(t, content)
				}
			}
		})
	}
}

func TestNamespaceVolumeName(t *testing.T) {
	tests := []struct {
		name     string
		value    string
		appID    string
		expected string
	}{
		{
			name:     "named volume simple",
			value:    "my-data:/data",
			appID:    "myapp",
			expected: "myapp-my-data:/data",
		},
		{
			name:     "named volume with options",
			value:    "my-data:/data:ro",
			appID:    "myapp",
			expected: "myapp-my-data:/data:ro",
		},
		{
			name:     "host path volume",
			value:    "/host/path:/container",
			appID:    "myapp",
			expected: "/host/path:/container",
		},
		{
			name:     "anonymous volume",
			value:    "/data",
			appID:    "myapp",
			expected: "/data",
		},
		{
			name:     "already prefixed named volume",
			value:    "myapp-my-data:/data",
			appID:    "myapp",
			expected: "myapp-my-data:/data",
		},
		{
			name:     "volume name only",
			value:    "my-data",
			appID:    "myapp",
			expected: "myapp-my-data",
		},
		{
			name:     "quadlet volume reference",
			value:    "data.volume:/data",
			appID:    "myapp",
			expected: "myapp-data.volume:/data",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := namespaceVolumeName(tt.value, tt.appID)
			require.Equal(t, tt.expected, result)
		})
	}
}

func TestNamespaceNetworkName(t *testing.T) {
	tests := []struct {
		name     string
		value    string
		appID    string
		expected string
	}{
		{
			name:     "custom network name",
			value:    "my-net",
			appID:    "myapp",
			expected: "myapp-my-net",
		},
		{
			name:     "quadlet network reference",
			value:    "my-net.network",
			appID:    "myapp",
			expected: "myapp-my-net.network",
		},
		{
			name:     "already prefixed network",
			value:    "myapp-my-net",
			appID:    "myapp",
			expected: "myapp-my-net",
		},
		{
			name:     "bridge mode",
			value:    "bridge",
			appID:    "myapp",
			expected: "bridge",
		},
		{
			name:     "host mode",
			value:    "host",
			appID:    "myapp",
			expected: "host",
		},
		{
			name:     "none mode",
			value:    "none",
			appID:    "myapp",
			expected: "none",
		},
		{
			name:     "private mode",
			value:    "private",
			appID:    "myapp",
			expected: "private",
		},
		{
			name:     "slirp4netns mode",
			value:    "slirp4netns",
			appID:    "myapp",
			expected: "slirp4netns",
		},
		{
			name:     "pasta mode",
			value:    "pasta",
			appID:    "myapp",
			expected: "pasta",
		},
		{
			name:     "bridge with options",
			value:    "bridge:ip=10.0.0.1",
			appID:    "myapp",
			expected: "bridge:ip=10.0.0.1",
		},
		{
			name:     "container network reference",
			value:    "container:mycontainer",
			appID:    "myapp",
			expected: "container:myapp-mycontainer",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := namespaceNetworkName(tt.value, tt.appID)
			require.Equal(t, tt.expected, result)
		})
	}
}

func TestEnsureQuadlet(t *testing.T) {
	require := require.New(t)

	tests := []struct {
		name          string
		files         map[string]string
		expectError   bool
		errorContains string
	}{
		{
			name: "valid top-level quadlet files",
			files: map[string]string{
				"app.container": `[Container]
Image=quay.io/test/image:v1
`,
				"db.container": `[Container]
Image=quay.io/test/db:v1
`,
			},
			expectError: false,
		},
		{
			name: "duplicate container names detected",
			files: map[string]string{
				"app.container": `[Container]
Image=quay.io/test/app:v1
ContainerName=shared
`,
				"db.container": `[Container]
Image=quay.io/test/db:v1
ContainerName=shared
`,
			},
			expectError:   true,
			errorContains: "duplicate ContainerName",
		},
		{
			name: "quadlet files in subdirectory (invalid)",
			files: map[string]string{
				"app1/app.container": `[Container]
Image=quay.io/test/image:v1
`,
				"app2/app.container": `[Container]
Image=quay.io/test/image:v2
`,
			},
			expectError:   true,
			errorContains: "must reside at the top level",
		},
		{
			name: "invalid no quadlet files",
			files: map[string]string{
				"README.md": "# My App",
				"LICENSE":   "MIT",
			},
			expectError:   true,
			errorContains: "no valid quadlet files",
		},
		{
			name: "mixed top-level and subdirectory (valid)",
			files: map[string]string{
				"app.container": `[Container]
Image=quay.io/test/image:v1
`,
				"docs/README.md": "# Documentation",
			},
			expectError: false,
		},
		{
			name: "valid with dropin configs",
			files: map[string]string{
				"app.container": `[Container]
Image=quay.io/test/image:v1
`,
				"app.container.d/10-override.conf": `[Container]
Environment=FOO=bar
`,
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			rw := fileio.NewReadWriter()
			rw.SetRootdir(tmpDir)

			// Create test files
			for path, content := range tt.files {
				dir := filepath.Dir(path)
				if dir != "." {
					err := rw.MkdirAll(dir, fileio.DefaultDirectoryPermissions)
					require.NoError(err)
				}
				err := rw.WriteFile(path, []byte(content), fileio.DefaultFilePermissions)
				require.NoError(err)
			}

			err := ensureQuadlet(rw, ".")

			if tt.expectError {
				require.Error(err)
				if tt.errorContains != "" {
					require.Contains(err.Error(), tt.errorContains)
				}
			} else {
				require.NoError(err)
			}
		})
	}
}

func TestHasQuadletFiles(t *testing.T) {
	require := require.New(t)

	tests := []struct {
		name        string
		files       []string
		expectFound bool
	}{
		{
			name:        "directory with container file",
			files:       []string{"app.container"},
			expectFound: true,
		},
		{
			name:        "directory with volume file",
			files:       []string{"data.volume"},
			expectFound: true,
		},
		{
			name:        "directory with network file",
			files:       []string{"mynet.network"},
			expectFound: true,
		},
		{
			name:        "directory with no quadlet files",
			files:       []string{"README.md", "LICENSE"},
			expectFound: false,
		},
		{
			name:        "empty directory",
			files:       []string{},
			expectFound: false,
		},
		{
			name:        "mixed files",
			files:       []string{"app.container", "README.md"},
			expectFound: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			rw := fileio.NewReadWriter()
			rw.SetRootdir(tmpDir)

			testDir := filepath.Join(tmpDir, "test")
			err := rw.MkdirAll(testDir, fileio.DefaultDirectoryPermissions)
			require.NoError(err)

			// Create test files
			for _, filename := range tt.files {
				fullPath := filepath.Join(testDir, filename)
				err = rw.WriteFile(fullPath, []byte("test content"), fileio.DefaultFilePermissions)
				require.NoError(err)
			}

			found, err := hasQuadletFiles(rw, testDir)
			require.NoError(err)
			require.Equal(tt.expectFound, found)
		})
	}
}

func makeMountVolume(name, path string) v1beta1.ApplicationVolume {
	vol := v1beta1.ApplicationVolume{Name: name}
	_ = vol.FromMountVolumeProviderSpec(v1beta1.MountVolumeProviderSpec{
		Mount: v1beta1.VolumeMount{Path: path},
	})
	return vol
}

func makeImageMountVolume(name, imageRef, path string) v1beta1.ApplicationVolume {
	vol := v1beta1.ApplicationVolume{Name: name}
	_ = vol.FromImageMountVolumeProviderSpec(v1beta1.ImageMountVolumeProviderSpec{
		Image: v1beta1.ImageVolumeSource{Reference: imageRef},
		Mount: v1beta1.VolumeMount{Path: path},
	})
	return vol
}

func TestGenerateQuadlet(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	cpuLimit := "2"
	memoryLimit := "512m"

	tests := []struct {
		name              string
		spec              *v1beta1.ImageApplicationProviderSpec
		setupMocks        func(*executer.MockExecuter)
		checkFileContents func(*testing.T, []byte)
		expectedFiles     []string
	}{
		{
			name: "simple image only",
			spec: &v1beta1.ImageApplicationProviderSpec{
				Image: "nginx:latest",
			},
			checkFileContents: func(t *testing.T, content []byte) {
				contentStr := string(content)
				require.Contains(t, contentStr, "[Container]")
				require.Contains(t, contentStr, "Image=nginx:latest")
				require.NotContains(t, contentStr, "PodmanArgs")
				require.NotContains(t, contentStr, "PublishPort")
			},
		},
		{
			name: "with CPU limit only",
			spec: &v1beta1.ImageApplicationProviderSpec{
				Image: "nginx:latest",
				Resources: &v1beta1.ApplicationResources{
					Limits: &v1beta1.ApplicationResourceLimits{
						Cpu: &cpuLimit,
					},
				},
			},
			checkFileContents: func(t *testing.T, content []byte) {
				contentStr := string(content)
				require.Contains(t, contentStr, "[Container]")
				require.Contains(t, contentStr, "Image=nginx:latest")
				require.Contains(t, contentStr, "PodmanArgs=--cpus 2")
				require.NotContains(t, contentStr, "--memory")
			},
		},
		{
			name: "with memory limit only",
			spec: &v1beta1.ImageApplicationProviderSpec{
				Image: "postgres:latest",
				Resources: &v1beta1.ApplicationResources{
					Limits: &v1beta1.ApplicationResourceLimits{
						Memory: &memoryLimit,
					},
				},
			},
			checkFileContents: func(t *testing.T, content []byte) {
				contentStr := string(content)
				require.Contains(t, contentStr, "[Container]")
				require.Contains(t, contentStr, "Image=postgres:latest")
				require.Contains(t, contentStr, "PodmanArgs=--memory 512m")
				require.NotContains(t, contentStr, "--cpus")
			},
		},
		{
			name: "with both CPU and memory limits",
			spec: &v1beta1.ImageApplicationProviderSpec{
				Image: "redis:latest",
				Resources: &v1beta1.ApplicationResources{
					Limits: &v1beta1.ApplicationResourceLimits{
						Cpu:    &cpuLimit,
						Memory: &memoryLimit,
					},
				},
			},
			checkFileContents: func(t *testing.T, content []byte) {
				contentStr := string(content)
				require.Contains(t, contentStr, "[Container]")
				require.Contains(t, contentStr, "Image=redis:latest")
				require.Contains(t, contentStr, "PodmanArgs=--cpus 2")
				require.Contains(t, contentStr, "PodmanArgs=--memory 512m")
			},
		},
		{
			name: "with single port",
			spec: &v1beta1.ImageApplicationProviderSpec{
				Image: "nginx:latest",
				Ports: &[]v1beta1.ApplicationPort{"8080:80"},
			},
			checkFileContents: func(t *testing.T, content []byte) {
				contentStr := string(content)
				require.Contains(t, contentStr, "[Container]")
				require.Contains(t, contentStr, "Image=nginx:latest")
				require.Contains(t, contentStr, "PublishPort=8080:80")
			},
		},
		{
			name: "with multiple ports",
			spec: &v1beta1.ImageApplicationProviderSpec{
				Image: "webapp:latest",
				Ports: &[]v1beta1.ApplicationPort{
					"8080:80",
					"8443:443",
					"9090:9090",
				},
			},
			checkFileContents: func(t *testing.T, content []byte) {
				contentStr := string(content)
				require.Contains(t, contentStr, "[Container]")
				require.Contains(t, contentStr, "Image=webapp:latest")
				require.Contains(t, contentStr, "PublishPort=8080:80")
				require.Contains(t, contentStr, "PublishPort=8443:443")
				require.Contains(t, contentStr, "PublishPort=9090:9090")
			},
		},
		{
			name: "complete spec with resources and ports",
			spec: &v1beta1.ImageApplicationProviderSpec{
				Image: "myapp:v1.0",
				Resources: &v1beta1.ApplicationResources{
					Limits: &v1beta1.ApplicationResourceLimits{
						Cpu:    &cpuLimit,
						Memory: &memoryLimit,
					},
				},
				Ports: &[]v1beta1.ApplicationPort{
					"3000:3000",
					"3001:3001",
				},
			},
			checkFileContents: func(t *testing.T, content []byte) {
				contentStr := string(content)
				require.Contains(t, contentStr, "[Container]")
				require.Contains(t, contentStr, "Image=myapp:v1.0")
				require.Contains(t, contentStr, "PodmanArgs=--cpus 2")
				require.Contains(t, contentStr, "PodmanArgs=--memory 512m")
				require.Contains(t, contentStr, "PublishPort=3000:3000")
				require.Contains(t, contentStr, "PublishPort=3001:3001")
			},
		},
		{
			name: "nil resources",
			spec: &v1beta1.ImageApplicationProviderSpec{
				Image:     "alpine:latest",
				Resources: nil,
			},
			checkFileContents: func(t *testing.T, content []byte) {
				contentStr := string(content)
				require.Contains(t, contentStr, "[Container]")
				require.Contains(t, contentStr, "Image=alpine:latest")
				require.NotContains(t, contentStr, "PodmanArgs")
			},
		},
		{
			name: "nil limits",
			spec: &v1beta1.ImageApplicationProviderSpec{
				Image: "ubuntu:latest",
				Resources: &v1beta1.ApplicationResources{
					Limits: nil,
				},
			},
			checkFileContents: func(t *testing.T, content []byte) {
				contentStr := string(content)
				require.Contains(t, contentStr, "[Container]")
				require.Contains(t, contentStr, "Image=ubuntu:latest")
				require.NotContains(t, contentStr, "PodmanArgs")
			},
		},
		{
			name: "nil ports",
			spec: &v1beta1.ImageApplicationProviderSpec{
				Image: "busybox:latest",
				Ports: nil,
			},
			checkFileContents: func(t *testing.T, content []byte) {
				contentStr := string(content)
				require.Contains(t, contentStr, "[Container]")
				require.Contains(t, contentStr, "Image=busybox:latest")
				require.NotContains(t, contentStr, "PublishPort")
			},
		},
		{
			name: "empty ports slice",
			spec: &v1beta1.ImageApplicationProviderSpec{
				Image: "centos:latest",
				Ports: &[]v1beta1.ApplicationPort{},
			},
			checkFileContents: func(t *testing.T, content []byte) {
				contentStr := string(content)
				require.Contains(t, contentStr, "[Container]")
				require.Contains(t, contentStr, "Image=centos:latest")
				require.NotContains(t, contentStr, "PublishPort")
			},
		},
		{
			name: "with mount volume",
			spec: &v1beta1.ImageApplicationProviderSpec{
				Image: "nginx:latest",
				Volumes: &[]v1beta1.ApplicationVolume{
					makeMountVolume("app-data", "/var/lib/app"),
				},
			},
			checkFileContents: func(t *testing.T, content []byte) {
				contentStr := string(content)
				require.Contains(t, contentStr, "[Container]")
				require.Contains(t, contentStr, "Image=nginx:latest")
				require.Contains(t, contentStr, "Volume=app-data:/var/lib/app")
			},
		},
		{
			name: "with image mount volume - image exists",
			spec: &v1beta1.ImageApplicationProviderSpec{
				Image: "nginx:latest",
				Volumes: &[]v1beta1.ApplicationVolume{
					makeImageMountVolume("app-config", "quay.io/config:v1", "/etc/app/config"),
				},
			},
			setupMocks: func(mockExec *executer.MockExecuter) {
				// Return success (0) for image exists command
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", gomock.Any()).Return("", "", 0).AnyTimes()
			},
			expectedFiles: []string{"app.container", "app-config.volume"},
			checkFileContents: func(t *testing.T, content []byte) {
				contentStr := string(content)
				require.Contains(t, contentStr, "[Container]")
				require.Contains(t, contentStr, "Image=nginx:latest")
				require.Contains(t, contentStr, "Volume=app-config.volume:/etc/app/config")
			},
		},
		{
			name: "with image mount volume - artifact (image does not exist)",
			spec: &v1beta1.ImageApplicationProviderSpec{
				Image: "nginx:latest",
				Volumes: &[]v1beta1.ApplicationVolume{
					makeImageMountVolume("app-artifact", "quay.io/artifact:v1", "/data/artifact"),
				},
			},
			setupMocks: func(mockExec *executer.MockExecuter) {
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", gomock.Any()).Return("", "", 1).AnyTimes()
			},
			checkFileContents: func(t *testing.T, content []byte) {
				contentStr := string(content)
				require.Contains(t, contentStr, "[Container]")
				require.Contains(t, contentStr, "Image=nginx:latest")
				require.Contains(t, contentStr, "Volume=app-artifact:/data/artifact")
			},
		},
		{
			name: "with multiple volumes - mixed types",
			spec: &v1beta1.ImageApplicationProviderSpec{
				Image: "webapp:latest",
				Volumes: &[]v1beta1.ApplicationVolume{
					makeMountVolume("data", "/var/data"),
					makeImageMountVolume("config", "quay.io/config:latest", "/etc/config"),
					makeImageMountVolume("cache", "quay.io/artifact:v1", "/cache"),
				},
			},
			setupMocks: func(mockExec *executer.MockExecuter) {
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", gomock.Any()).DoAndReturn(
					func(ctx context.Context, command string, args ...string) (string, string, int) {
						if len(args) >= 3 && args[0] == "image" && args[1] == "exists" && args[2] == "quay.io/config:latest" {
							return "", "", 0
						}
						return "", "", 1
					},
				).AnyTimes()
			},
			expectedFiles: []string{"app.container", "config.volume"},
			checkFileContents: func(t *testing.T, content []byte) {
				contentStr := string(content)
				require.Contains(t, contentStr, "[Container]")
				require.Contains(t, contentStr, "Image=webapp:latest")
				require.Contains(t, contentStr, "Volume=data:/var/data")
				require.Contains(t, contentStr, "Volume=config.volume:/etc/config")
				require.Contains(t, contentStr, "Volume=cache:/cache")
			},
		},
		{
			name: "with volumes and ports and resources",
			spec: &v1beta1.ImageApplicationProviderSpec{
				Image: "fullapp:latest",
				Resources: &v1beta1.ApplicationResources{
					Limits: &v1beta1.ApplicationResourceLimits{
						Cpu:    &cpuLimit,
						Memory: &memoryLimit,
					},
				},
				Ports: &[]v1beta1.ApplicationPort{
					"8080:80",
					"8443:443",
				},
				Volumes: &[]v1beta1.ApplicationVolume{
					makeMountVolume("data", "/data"),
					makeImageMountVolume("config", "quay.io/config:v1", "/config"),
				},
			},
			setupMocks: func(mockExec *executer.MockExecuter) {
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", gomock.Any()).DoAndReturn(
					func(ctx context.Context, command string, args ...string) (string, string, int) {
						if len(args) >= 3 && args[0] == "image" && args[1] == "exists" && args[2] == "quay.io/config:v1" {
							return "", "", 0
						}
						return "", "", 1
					},
				).AnyTimes()
			},
			expectedFiles: []string{"app.container", "config.volume"},
			checkFileContents: func(t *testing.T, content []byte) {
				contentStr := string(content)
				require.Contains(t, contentStr, "[Container]")
				require.Contains(t, contentStr, "Image=fullapp:latest")
				require.Contains(t, contentStr, "PodmanArgs=--cpus 2")
				require.Contains(t, contentStr, "PodmanArgs=--memory 512m")
				require.Contains(t, contentStr, "PublishPort=8080:80")
				require.Contains(t, contentStr, "PublishPort=8443:443")
				require.Contains(t, contentStr, "Volume=data:/data")
				require.Contains(t, contentStr, "Volume=config.volume:/config")
				require.Contains(t, contentStr, "[Service]")
				require.Contains(t, contentStr, "Restart=on-failure")
				require.Contains(t, contentStr, "RestartSec=60")
				require.Contains(t, contentStr, "[Install]")
				require.Contains(t, contentStr, "WantedBy=multi-user.target default.target")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			rw := fileio.NewReadWriter()
			rw.SetRootdir(tmpDir)

			mockExec := executer.NewMockExecuter(ctrl)
			if tt.setupMocks != nil {
				tt.setupMocks(mockExec)
			}
			podman := client.NewPodman(log.NewPrefixLogger("test"), mockExec, rw, testutil.NewPollConfig())

			ctx := context.Background()
			err := generateQuadlet(ctx, podman, rw, "/", tt.spec)
			require.NoError(t, err)

			expectedFiles := tt.expectedFiles
			if expectedFiles == nil {
				expectedFiles = []string{"app.container"}
			}

			for _, file := range expectedFiles {
				content, err := rw.ReadFile(file)
				require.NoError(t, err, "expected file %s to exist", file)
				require.NotEmpty(t, content, "expected file %s to have content", file)

				if strings.HasSuffix(file, ".volume") {
					contentStr := string(content)
					require.Contains(t, contentStr, "[Volume]")
					require.Contains(t, contentStr, "Image=")
					require.Contains(t, contentStr, "Driver=image")
				}
			}

			content, err := rw.ReadFile("app.container")
			require.NoError(t, err, "expected app.container file to exist")
			require.NotEmpty(t, content, "expected app.container to have content")

			if tt.checkFileContents != nil {
				tt.checkFileContents(t, content)
			}
		})
	}
}
