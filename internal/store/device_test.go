package store

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestLabelKeyToSymbol(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"simple key", "region", "region"},
		{"dotted key", "node.zone", "node_dot_zone"},
		{"dashed key", "my-key", "my_dash_key"},
		{"slashed key", "kubernetes.io/zone", "kubernetes_dot_io_slash_zone"},
		{"combined", "a.b-c/d", "a_dot_b_dash_c_slash_d"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := labelKeyToSymbol(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestQuoteIdentifier(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"simple name", "region", `"region"`},
		{"with underscore", "my_key", `"my_key"`},
		{"with embedded double quote", `my"key`, `"my""key"`},
		{"label symbol", "node_dot_zone", `"node_dot_zone"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := quoteIdentifier(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}
