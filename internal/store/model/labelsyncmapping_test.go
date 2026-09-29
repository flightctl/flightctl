package model

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLabelSyncMappingToApiResourceVersion(t *testing.T) {
	mapping := LabelSyncMapping{Resource: Resource{Name: "architecture"}}
	item, err := mapping.ToApiResource()
	require.NoError(t, err)

	list, err := LabelSyncMappingsToApiResource([]LabelSyncMapping{mapping}, nil, nil)
	require.NoError(t, err)
	require.Len(t, list.Items, 1)
	require.Equal(t, LabelSyncMappingAPIVersion(), item.ApiVersion)
	require.Equal(t, item.ApiVersion, list.Items[0].ApiVersion)
}
