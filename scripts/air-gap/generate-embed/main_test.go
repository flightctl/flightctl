package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	pkgmanifest "github.com/flightctl/flightctl/scripts/air-gap/pkg/manifest"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestResolveImagesGateway(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..", "..")
	helmData, err := os.ReadFile(filepath.Join(root, "deploy", "helm", "helm-chart-opts.yaml"))
	require.NoError(t, err)
	var opts helmChartOpts
	require.NoError(t, yaml.Unmarshal(helmData, &opts))

	tests := []struct {
		variant string
		os      string
		gateway pkgmanifest.Image
	}{
		{
			variant: "rhem-el9",
			os:      "rhel9",
			gateway: pkgmanifest.Image{Ref: "registry.redhat.io/rhel9/nginx-124", Tag: "1-1792441608"},
		},
		{
			variant: "rhem-el10",
			os:      "rhel10",
			gateway: pkgmanifest.Image{Ref: "registry.redhat.io/rhel10/nginx-126", Tag: "10.2-1792483994"},
		},
	}
	for _, test := range tests {
		t.Run("When resolving "+test.variant+" it should include only its pinned gateway", func(t *testing.T) {
			t.Parallel()

			data, err := os.ReadFile(filepath.Join(root, "packaging", "images", test.os, "images.yaml"))
			require.NoError(t, err)
			images, _ := resolveImages(opts[test.variant], data, test.variant)
			require.Contains(t, images, test.gateway)
			var gateways []pkgmanifest.Image
			for _, image := range images {
				if strings.Contains(image.Ref, "/nginx-") {
					gateways = append(gateways, image)
				}
			}
			require.Equal(t, []pkgmanifest.Image{test.gateway}, gateways)
			for _, service := range []string{"pam-issuer", "userinfo-proxy"} {
				require.Contains(t, images, pkgmanifest.Image{Ref: "registry.redhat.io/rhem/flightctl-" + service + "-" + test.os})
			}
		})
	}
}
