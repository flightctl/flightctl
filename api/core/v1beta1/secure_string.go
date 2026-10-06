package v1beta1

import (
	"encoding/json"
	"fmt"
)

const (
	redactedPlaceholder = "[REDACTED]"
)

type SecureString string

// String implements fmt.Stringer for direct .String() calls.
func (s SecureString) String() string {
	return redactedPlaceholder
}

// Format implements fmt.Formatter so that every verb (%s, %q, %v, %x, …)
// prints the redacted placeholder instead of the underlying value.
func (s SecureString) Format(f fmt.State, verb rune) {
	fmt.Fprintf(f, fmt.FormatString(f, verb), redactedPlaceholder)
}

// GoString implements fmt.GoStringer interface (used by %#v)
func (s SecureString) GoString() string {
	return redactedPlaceholder
}

// MarshalJSON implements json.Marshaler interface
func (s SecureString) MarshalJSON() ([]byte, error) {
	return json.Marshal(redactedPlaceholder)
}

// MarshalYAML implements the yaml.Marshaler interface so that YAML
// serialisation emits the redacted placeholder instead of the real value.
func (s SecureString) MarshalYAML() (any, error) {
	return redactedPlaceholder, nil
}

func (s SecureString) Value() string {
	return string(s)
}
