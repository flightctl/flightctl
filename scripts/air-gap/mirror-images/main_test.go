package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

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
