package convert

import (
	v1beta1 "github.com/flightctl/flightctl/internal/api/convert/v1beta1"
)

// Converter provides access to all API version converters.
type Converter interface {
	V1beta1() v1beta1.Converter
}

type converter struct {
	v1beta1Converter v1beta1.Converter
}

// NewConverter creates a new Converter instance with all version-specific converters.
func NewConverter() Converter {
	return &converter{
		v1beta1Converter: v1beta1.NewConverter(),
	}
}

func (c *converter) V1beta1() v1beta1.Converter {
	return c.v1beta1Converter
}
