package catalogcollector

import (
	"fmt"
	"strings"
)

// ComponentID identifies a configured component instance using the format
// type[/name]. The type selects the registered factory. The optional name
// distinguishes multiple instances of the same type.
//
// A component ID is unique within its component kind. Therefore,
// "http/input" as a source and "http/input" as a destination do not
// collide because source and destination namespaces are separate.
//
// Examples:
//
//	"http"           → Type: "http",      Name: ""
//	"http/dev-input" → Type: "http",      Name: "dev-input"
//	"flightctl"      → Type: "flightctl", Name: ""
type ComponentID struct {
	Type ComponentType
	Name string
}

// ParseComponentID parses a component identifier in the format type[/name].
//
// Validation rejects:
//   - Empty identifiers
//   - Empty type (e.g. "/name")
//   - Empty name after separator (e.g. "http/")
//   - More than one separator (e.g. "a/b/c")
func ParseComponentID(value string) (ComponentID, error) {
	if value == "" {
		return ComponentID{}, fmt.Errorf("empty component identifier")
	}

	typ, name, hasSep := strings.Cut(value, "/")
	if typ == "" {
		return ComponentID{}, fmt.Errorf("component identifier %q has empty type", value)
	}
	if hasSep && name == "" {
		return ComponentID{}, fmt.Errorf("component identifier %q has empty name after separator", value)
	}
	if hasSep && strings.Contains(name, "/") {
		return ComponentID{}, fmt.Errorf("component identifier %q contains multiple separators", value)
	}

	return ComponentID{
		Type: ComponentType(typ),
		Name: name,
	}, nil
}

// String returns the canonical string representation: type when unnamed,
// type/name when named.
func (id ComponentID) String() string {
	if id.Name == "" {
		return string(id.Type)
	}
	return string(id.Type) + "/" + id.Name
}
