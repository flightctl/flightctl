package model

import (
	"context"
	"strings"
	"testing"

	"github.com/flightctl/flightctl/internal/store/selector"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestCatalogItemFleetSelector(t *testing.T) {
	orgID := uuid.New()
	resolver, err := selector.SelectorFieldResolver(&CatalogItem{OrgID: orgID})
	require.NoError(t, err)

	for _, testCase := range []struct {
		name    string
		input   string
		wantErr bool
	}{
		{name: "When two fleets are selected it should bind each name", input: "fleet in (fleet-a,fleet-b)"},
		{name: "When composed with another selector it should bind in order", input: "fleet in (fleet-a,fleet-b),spec.type=container"},
		{name: "When a name contains SQL syntax it should remain a bound value", input: "fleet in (fleet-a';drop)"},
		{name: "When an unsupported operator is supplied it should fail", input: "fleet=fleet-a", wantErr: true},
		{name: "When more than 100 names are supplied it should fail", input: "fleet in (" + strings.Join(makeFleetNames(101), ",") + ")", wantErr: true},
		{name: "When a name exceeds 253 characters it should fail", input: "fleet in (" + strings.Repeat("a", 254) + ")", wantErr: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			fieldSelector, err := selector.NewFieldSelector(testCase.input)
			if err != nil {
				require.True(t, testCase.wantErr)
				return
			}
			query, args, err := fieldSelector.Parse(context.Background(), resolver)
			if testCase.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Contains(t, query, "(catalog_items.catalog_name, catalog_items.app_name) IN (")
			require.Contains(t, query, "SELECT DISTINCT r.ref->>'catalog', r.ref->>'item'")
			require.Contains(t, query, "f.org_id = ?")
			require.Contains(t, query, "f.name IN (")
			require.NotContains(t, query, "{values}")
			require.NotContains(t, query[strings.Index(query, "SELECT DISTINCT"):], "catalog_items.")
			require.Equal(t, orgID, args[0])
			if strings.Contains(testCase.input, "spec.type") {
				require.Equal(t, []any{orgID, "fleet-a", "fleet-b", "container"}, args)
			}
			if strings.Contains(testCase.input, "drop") {
				require.NotContains(t, query, "drop")
			}
		})
	}
}

func makeFleetNames(count int) []string {
	names := make([]string, count)
	for index := range names {
		names[index] = "fleet-" + strings.Repeat("a", index%10) + strings.Repeat("b", index/10)
	}
	return names
}
