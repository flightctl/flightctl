package packagemodemigration

import (
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

const testOrgID = "0ddb4a3c-6b62-4f9a-9d4a-7b9d5e2f1a00"

func TestOptedIn(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  bool
	}{
		{name: "When the value is unset it should not opt in", value: "", want: false},
		{name: "When the value is whitespace it should not opt in", value: "   ", want: false},
		{name: "When the value is 1 it should opt in", value: "1", want: true},
		{name: "When the value is true it should opt in", value: "true", want: true},
		{name: "When the value is TRUE it should opt in", value: "TRUE", want: true},
		{name: "When the value is padded it should opt in", value: " yes ", want: true},
		{name: "When the value is 0 it should not opt in", value: "0", want: false},
		{name: "When the value is false it should not opt in", value: "false", want: false},
		{name: "When the value is misspelled it should not opt in", value: "ture", want: false},
		{name: "When the value is arbitrary it should not opt in", value: "maybe", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, OptedIn(tt.value))
		})
	}
}

func TestInventoryQuery(t *testing.T) {
	t.Run("When the organization is a UUID it should count across all organizations", func(t *testing.T) {
		query, err := InventoryQuery(" " + testOrgID + " ")
		require.NoError(t, err)

		// Live rows only, matching how the stores read them.
		require.Contains(t, query, "FROM fleets WHERE deleted_at IS NULL")
		require.Contains(t, query, "FROM devices WHERE deleted_at IS NULL")
		require.Contains(t, query, "FROM organizations")
		require.Contains(t, query, "id = '"+testOrgID+"'")

		// No org_id filter anywhere: a per-tenant census would not bound a
		// deployment-wide stop.
		require.NotContains(t, query, "org_id =")
		require.Equal(t, inventoryFields, strings.Count(query, "SELECT COUNT(*)"))
	})

	t.Run("When the organization is not a UUID it should refuse to build a query", func(t *testing.T) {
		for _, bad := range []string{"", "   ", "not-a-uuid", "'; DROP TABLE fleets; --"} {
			query, err := InventoryQuery(bad)
			require.Error(t, err, "input %q", bad)
			require.Empty(t, query)
		}
	})
}

func TestParseInventory(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    GlobalInventory
		wantErr string
	}{
		{
			name: "When the deployment is empty it should parse zeroes",
			raw:  "0|0|1|1",
			want: GlobalInventory{Fleets: 0, Devices: 0, Organizations: 1, TargetOrgRows: 1},
		},
		{
			name: "When the output is padded it should still parse",
			raw:  "  2|3|1|1\n",
			want: GlobalInventory{Fleets: 2, Devices: 3, Organizations: 1, TargetOrgRows: 1},
		},
		{name: "When the output is empty it should fail", raw: "", wantErr: "no output"},
		{name: "When the output is whitespace it should fail", raw: "  \n ", wantErr: "no output"},
		{name: "When a column is missing it should fail", raw: "0|0|1", wantErr: "3 columns"},
		{name: "When there is an extra column it should fail", raw: "0|0|1|1|0", wantErr: "5 columns"},
		{name: "When several rows come back it should fail", raw: "0|0|1|1\n0|0|1|1", wantErr: "2 rows"},
		{name: "When a column is not a number it should fail", raw: "0|x|1|1", wantErr: "not a number"},
		{name: "When a column is blank it should fail", raw: "0||1|1", wantErr: "not a number"},
		{name: "When psql reports an error it should fail", raw: "ERROR:  relation \"fleets\" does not exist", wantErr: "columns"},
		{name: "When a column is negative it should fail", raw: "0|-1|1|1", wantErr: "negative"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseInventory(tt.raw)
			if tt.wantErr == "" {
				require.NoError(t, err)
				require.Equal(t, tt.want, got)
				return
			}
			require.Error(t, err)
			require.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestEvaluateInventory(t *testing.T) {
	empty := GlobalInventory{Fleets: 0, Devices: 0, Organizations: 1, TargetOrgRows: 1}

	tests := []struct {
		name        string
		inv         GlobalInventory
		wantErr     bool
		wantContain []string
	}{
		{
			name: "When the deployment is empty and single-tenant it should allow the mutation",
			inv:  empty,
		},
		{
			name:        "When another organization owns a fleet it should refuse",
			inv:         GlobalInventory{Fleets: 1, Devices: 0, Organizations: 2, TargetOrgRows: 1},
			wantErr:     true,
			wantContain: []string{"1 fleet(s) exist across all organizations", "2 organizations"},
		},
		{
			name:        "When another organization owns a device it should refuse",
			inv:         GlobalInventory{Fleets: 0, Devices: 4, Organizations: 2, TargetOrgRows: 1},
			wantErr:     true,
			wantContain: []string{"4 device(s) exist across all organizations"},
		},
		{
			name:        "When a second organization exists but holds nothing it should still refuse",
			inv:         GlobalInventory{Fleets: 0, Devices: 0, Organizations: 2, TargetOrgRows: 1},
			wantErr:     true,
			wantContain: []string{"holds 2 organizations"},
		},
		{
			name:        "When the target organization is not in the database it should refuse",
			inv:         GlobalInventory{Fleets: 0, Devices: 0, Organizations: 1, TargetOrgRows: 0},
			wantErr:     true,
			wantContain: []string{"matched 0 rows"},
		},
		{
			name:        "When every condition fails it should report all of them",
			inv:         GlobalInventory{Fleets: 3, Devices: 7, Organizations: 5, TargetOrgRows: 0},
			wantErr:     true,
			wantContain: []string{"3 fleet(s)", "7 device(s)", "5 organizations", "matched 0 rows"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := EvaluateInventory(tt.inv, testOrgID)
			if !tt.wantErr {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			for _, want := range tt.wantContain {
				require.Contains(t, err.Error(), want)
			}
		})
	}
}

// The failure this gate exists to prevent: the caller's own organization looks
// empty through the API while another organization in the same deployment is
// in use. The census is global, so that case is refused.
func TestEvaluateInventoryRefusesCrossOrgResourcesWhenCurrentOrgIsEmpty(t *testing.T) {
	populatedElsewhere, err := ParseInventory("2|5|3|1")
	require.NoError(t, err)
	require.Error(t, EvaluateInventory(populatedElsewhere, testOrgID))
}

// An inventory that cannot be read is never evidence of an empty deployment.
func TestUnreadableInventoryIsNeverTreatedAsEmpty(t *testing.T) {
	for _, raw := range []string{"", "   ", "psql: could not connect to server", "0|0|1", "0|0|1|1\n0|0|1|1"} {
		inv, err := ParseInventory(raw)
		require.Error(t, err, "input %q must not parse", raw)
		require.Equal(t, GlobalInventory{}, inv)
	}
}

// The query is built from a parsed UUID, so an organization id that arrives as
// an injection attempt cannot reach the database.
func TestInventoryQueryRejectsNonUUIDBeforeReachingSQL(t *testing.T) {
	_, err := InventoryQuery("' OR 1=1 --")
	require.Error(t, err)

	valid := uuid.New()
	query, err := InventoryQuery(valid.String())
	require.NoError(t, err)
	require.Contains(t, query, fmt.Sprintf("id = '%s'", valid.String()))
}
