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
			fieldMap:      map[string][]string{"status": {"active"}},
			expectedQuery: []string{"status ->> 'status' = ?"},
			expectedArgs:  []interface{}{"active"},
		},
		{
			name:          "nested fields",
			fieldMap:      map[string][]string{"device.status": {"active"}},
			expectedQuery: []string{"status -> 'device' ->> 'status' = ?"},
			expectedArgs:  []interface{}{"active"},
		},
		{
			name: "nested fields multiple values",
			fieldMap: map[string][]string{
				"updated.status":              {"UpToDate", "OutOfDate"},
				"applications.summary.status": {"Degraded"},
			},
			expectedQuery: []string{
				"status -> 'updated' ->> 'status' = ?",
				"status -> 'updated' ->> 'status' = ?",
				"status -> 'applications' -> 'summary' ->> 'status' = ?",
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
