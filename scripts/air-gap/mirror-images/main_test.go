package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResolveVariantGateway(t *testing.T) {
	t.Setenv("MIRROR_MANIFEST", "")
	manifest, err := loadBuildManifest()
	require.NoError(t, err)

	tests := []struct {
		variant string
		gateway string
		os      string
	}{
		{
			variant: "rhem-el9",
			gateway: "registry.redhat.io/rhel9/nginx-124:1-1792441608",
			os:      "rhel9",
		},
		{
			variant: "rhem-el10",
			gateway: "registry.redhat.io/rhel10/nginx-126:10.2-1792483994",
			os:      "rhel10",
		},
		{
			variant: "community-el9",
			gateway: "registry.access.redhat.com/ubi9/nginx-124:",
		},
		{
			variant: "community-el10",
			gateway: "registry.access.redhat.com/ubi10/nginx-126:",
		},
	}
	for _, test := range tests {
		t.Run("When overriding "+test.variant+" service tags it should preserve its gateway pin", func(t *testing.T) {
			pairs, _, err := resolveVariant(manifest, test.variant, "1.4.0", nil)
			require.NoError(t, err)
			var sources, gateways []string
			for _, pair := range pairs {
				sources = append(sources, pair.Source)
				if strings.Contains(pair.Source, "/nginx-") {
					gateways = append(gateways, pair.Source)
				}
			}
			require.Len(t, gateways, 1)
			if test.os == "" {
				require.True(t, strings.HasPrefix(gateways[0], test.gateway))
				return
			}
			require.Equal(t, test.gateway, gateways[0])
			for _, service := range []string{"pam-issuer", "userinfo-proxy"} {
				require.Contains(t, sources, "registry.redhat.io/rhem/flightctl-"+service+"-"+test.os+":1.4.0")
			}
		})
	}
}

// TestValidateFlagsIncludeOptional pins the flag combinations around
// --include-optional.
//
// --agent-only short-circuits into runAgentOnlyBundle before any image is
// resolved, so an opt-in image group passed alongside it is never mirrored.
// Accepting the combination would hand the operator a bundle that is missing
// exactly the images they asked for, and nothing would say so until the
// bundle was unpacked on the air-gapped host.
func TestValidateFlagsIncludeOptional(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		variant         string
		bundle          string
		agentOnly       bool
		includeOptional []string
		wantErr         string
	}{
		{
			name:            "When --agent-only is combined with --include-optional it should be rejected",
			bundle:          "/tmp/agent-bundle.tar.gz",
			agentOnly:       true,
			includeOptional: []string{"catalog-collector"},
			wantErr:         "--agent-only and --include-optional are mutually exclusive",
		},
		{
			name:            "When --agent-only is combined with several optional groups it should name them all",
			bundle:          "/tmp/agent-bundle.tar.gz",
			agentOnly:       true,
			includeOptional: []string{"catalog-collector", "observability"},
			wantErr:         "catalog-collector, observability",
		},
		{
			name:      "When --agent-only is used without --include-optional it should be accepted",
			bundle:    "/tmp/agent-bundle.tar.gz",
			agentOnly: true,
		},
		{
			name:            "When --include-optional is used with a variant it should be accepted",
			variant:         "community-el9",
			bundle:          "/tmp/bundle.tar.gz",
			includeOptional: []string{"catalog-collector"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := validateFlags(tt.variant, tt.bundle, false, false, false, false, tt.agentOnly, tt.includeOptional)
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			require.Contains(t, err.Error(), tt.wantErr)
		})
	}
}
