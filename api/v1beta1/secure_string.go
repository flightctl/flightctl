package v1beta1

import "encoding/json"

const (
	redactedPlaceholder = "[REDACTED]"
)

type SecureString string

// String implements fmt.Stringer interface, used by fmt.Println, fmt.Printf, etc.
func (s SecureString) String() string {
	return redactedPlaceholder
}

// GoString implements fmt.GoStringer interface (used by %#v)
func (s SecureString) GoString() string {
	return redactedPlaceholder
}

// MarshalJSON implements json.Marshaler interface
func (s SecureString) MarshalJSON() ([]byte, error) {
	return json.Marshal(redactedPlaceholder)
}

func (s SecureString) Value() string {
	return string(s)
}
