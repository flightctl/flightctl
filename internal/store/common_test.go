package store

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCreateQueryFromFilterMap(t *testing.T) {
	require := require.New(t)
	tests := []struct {
		name          string
		fieldMap      map[string][]string
		expectedQuery []string
		expectedArgs  []interface{}
	}{
		{
			name:          "empty input",
			fieldMap:      map[string][]string{},
			expectedQuery: []string{""},
			expectedArgs:  []interface{}{},
		},
		{
			name:          "empty field",
			fieldMap:      map[string][]string{"": {"Online"}},
			expectedQuery: []string{""},
			expectedArgs:  []interface{}{},
		},
		{
			name:          "empty value",
			fieldMap:      map[string][]string{"status": {""}},
			expectedQuery: []string{""},
			expectedArgs:  []interface{}{},
		},
		{
			name:          "single field",
			fieldMap:      map[string][]string{"status.status": {"active"}},
			expectedQuery: []string{"status ->> 'status' = ?"},
			expectedArgs:  []interface{}{"active"},
		},
		{
			name:          "nested fields",
			fieldMap:      map[string][]string{"status.device.status": {"active"}},
			expectedQuery: []string{"status -> 'device' ->> 'status' = ?"},
			expectedArgs:  []interface{}{"active"},
		},
		{
			name: "nested fields multiple values",
			fieldMap: map[string][]string{
				"status.updated.status":             {"UpToDate", "OutOfDate"},
				"status.applicationsSummary.status": {"Degraded"},
			},
			expectedQuery: []string{
				"status -> 'updated' ->> 'status' = ?",
				"status -> 'updated' ->> 'status' = ?",
				"status -> 'applicationsSummary' ->> 'status' = ?",
			},
			expectedArgs: []interface{}{"UpToDate", "OutOfDate", "Degraded"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			query, args := createQueryFromFilterMap(test.fieldMap)
			queryParts := strings.Split(query, " OR ")
			require.ElementsMatch(test.expectedQuery, queryParts)
			require.ElementsMatch(test.expectedArgs, args)
		})
	}
}

func TestCreateOrQuery(t *testing.T) {
	require := require.New(t)
	tests := []struct {
		name          string
		key           string
		values        []string
		expectedQuery []string
		expectedArgs  []interface{}
	}{
		{
			name:          "empty input",
			key:           "",
			values:        []string{""},
			expectedQuery: []string{""},
			expectedArgs:  []interface{}{},
		},
		{
			name:          "empty field",
			key:           "",
			values:        []string{"foo"},
			expectedQuery: []string{""},
			expectedArgs:  []interface{}{},
		},
		{
			name:          "empty value",
			key:           "owner",
			values:        []string{""},
			expectedQuery: []string{""},
			expectedArgs:  []interface{}{},
		},
		{
			name:          "single value",
			key:           "owner",
			values:        []string{"Fleet/fleet-a"},
			expectedQuery: []string{"owner = ?"},
			expectedArgs:  []interface{}{"Fleet/fleet-a"},
		},
		{
			name:          "multiple values",
			key:           "owner",
			values:        []string{"Fleet/fleet-a", "Fleet/fleet-b"},
			expectedQuery: []string{"owner = ?", "owner = ?"},
			expectedArgs:  []interface{}{"Fleet/fleet-a", "Fleet/fleet-b"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			query, args := createOrQuery(test.key, test.values)
			queryParts := strings.Split(query, " OR ")
			require.ElementsMatch(test.expectedQuery, queryParts)
			require.ElementsMatch(test.expectedArgs, args)
		})
	}
}
