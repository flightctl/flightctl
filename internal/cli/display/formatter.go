// Package display contains helpers for formatting FlightCTL resources into different CLI output formats.
package display

import (
	"io"
)

// OutputFormat represents the different output formats supported
type OutputFormat string

const (
	JSONFormat OutputFormat = "json"
	YAMLFormat OutputFormat = "yaml"
	NameFormat OutputFormat = "name"
	WideFormat OutputFormat = "wide"
)

// FormatOptions contains options for formatting output
type FormatOptions struct {
	Kind        string
	Name        string
	Summary     bool
	SummaryOnly bool
	Wide        bool
	WithExports bool
	Writer      io.Writer
}

// OutputFormatter defines the interface for formatting and displaying data
type OutputFormatter interface {
	Format(data interface{}, options FormatOptions) error
}

// NewFormatter creates a new formatter based on the output format
func NewFormatter(format OutputFormat) OutputFormatter {
	switch format {
	case JSONFormat:
		return &JSONFormatter{}
	case YAMLFormat:
		return &YAMLFormatter{}
	case NameFormat:
		return &NameFormatter{}
	case WideFormat:
		return &TableFormatter{wide: true}
	default:
		return &TableFormatter{wide: false}
	}
}
