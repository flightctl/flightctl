package flightctl_test

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRouteExternalCertificate(t *testing.T) {
	routes := []struct {
		name   string
		source string
	}{
		{
			name:   "CLI artifacts",
			source: "flightctl/templates/cli-artifacts/flightctl-cli-artifacts-route.yaml",
		},
		{
			name:   "UI",
			source: "flightctl/templates/ui/flightctl-ui-route.yaml",
		},
	}

	tests := []struct {
		name                 string
		routeCertificate     string
		certificateMethod    string
		certManagerInstalled bool
		externalCert         bool
		expectedDocuments    int
	}{
		{
			name:                 "When auto mode detects cert-manager without an existing Secret it should omit externalCertificate",
			certificateMethod:    "auto",
			certManagerInstalled: true,
			expectedDocuments:    1,
		},
		{
			name:              "When auto mode falls back to builtin certificates it should include externalCertificate",
			certificateMethod: "auto",
			externalCert:      true,
			expectedDocuments: 3,
		},
		{
			name:              "When routeExternalCertificate is false it should omit externalCertificate",
			certificateMethod: "cert-manager",
			routeCertificate:  "false",
			expectedDocuments: 1,
		},
		{
			name:              "When routeExternalCertificate is true it should include externalCertificate",
			certificateMethod: "cert-manager",
			routeCertificate:  "true",
			externalCert:      true,
			expectedDocuments: 3,
		},
	}

	for _, route := range routes {
		route := route
		t.Run(route.name, func(t *testing.T) {
			for _, tt := range tests {
				tt := tt
				t.Run(tt.name, func(t *testing.T) {
					manifest := renderChart(t, tt.routeCertificate, tt.certificateMethod, tt.certManagerInstalled)
					documents := sourceDocuments(manifest, route.source)

					require.Len(t, documents, tt.expectedDocuments)
					if tt.externalCert {
						require.Contains(t, strings.Join(documents, "\n"), "externalCertificate:")
					} else {
						require.NotContains(t, strings.Join(documents, "\n"), "externalCertificate:")
					}
				})
			}
		})
	}
}

func renderChart(t *testing.T, routeCertificate, certificateMethod string, certManagerInstalled bool) string {
	t.Helper()

	_, filename, _, ok := runtime.Caller(0)
	require.True(t, ok, "failed to determine chart test path")

	args := []string{
		"template",
		"flightctl",
		filepath.Dir(filename),
		"--namespace",
		"flightctl",
		"--set-string",
		"global.enableOpenShiftExtensions=true",
		"--set-string",
		"global.exposeServicesMethod=route",
		"--set-string",
		"global.baseDomain=apps.example.com",
		"--set-string",
		"global.auth.type=none",
		"--set-string",
		"global.generateCertificates=" + certificateMethod,
		"--api-versions",
		"route.openshift.io/v1",
	}
	if certManagerInstalled {
		args = append(args, "--api-versions", "cert-manager.io/v1/Certificate")
	}
	if routeCertificate != "" {
		args = append(args, "--set-string", "global.routeExternalCertificate="+routeCertificate)
	}

	output, err := exec.Command("helm", args...).CombinedOutput()
	require.NoError(t, err, string(output))
	return string(output)
}

func sourceDocuments(manifest, source string) []string {
	marker := "# Source: " + source
	var documents []string
	for remaining := manifest; ; {
		index := strings.Index(remaining, marker)
		if index < 0 {
			return documents
		}

		document := remaining[index:]
		if separator := strings.Index(document, "\n---"); separator >= 0 {
			document = document[:separator]
		}
		documents = append(documents, document)
		remaining = remaining[index+len(marker):]
	}
}
