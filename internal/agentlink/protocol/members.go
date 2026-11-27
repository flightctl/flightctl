package protocol

import "fmt"

// Typed member accessors for the bodies a method defines -- its params and its
// data payload.
//
// The envelope decoder validates params and payload only as objects, because
// method resolution happens before parameter decoding: under the strict
// unknown-member rule the params of an unimplemented method is a shape the
// endpoint cannot validate, so deciding it first would report every future
// method as a failure rather than as MethodNotSupported. These helpers are how
// a method decodes its own body once it has been resolved.
//
// Every one of them keeps the envelope's rules, so a method schema cannot
// quietly relax them: an explicit null is a present member of the wrong type
// and never absence, types are exact with no coercion, and an integer converts
// through the same lossless decimal path the envelope's own integers use.

// RejectUnknownMembers reports the first member of obj that allowed does not
// list. Unknown members are rejected rather than ignored, which is what keeps
// a removed field -- a byte-denominated limit, say -- from returning by
// accident under its old name and being silently discarded.
//
// where names the object in the diagnostic. It is a compiled-in literal, never
// peer input.
func RejectUnknownMembers(obj *Value, allowed map[string]bool, where string) error {
	if obj == nil || obj.Kind != ValueObject {
		return fmt.Errorf("%s is not an object", where)
	}
	for _, k := range obj.Keys {
		if !allowed[k] {
			return fmt.Errorf("unknown member %q in %s", truncate(k, 64), where)
		}
	}
	return nil
}

// StringMember reads an optional string member. The second return reports
// presence, so a caller can tell an absent member from an empty string.
func StringMember(obj *Value, name, where string) (string, bool, error) {
	m, ok := obj.Member(name)
	if !ok {
		return "", false, nil
	}
	if m.Kind != ValueString {
		return "", true, fmt.Errorf("member %q in %s is a %s, not a string", name, where, m.Kind)
	}
	return m.Str, true, nil
}

// BoolMember reads an optional boolean member. A boolean is never read from a
// number or a string: 1 and "true" are the wrong type, not a true value.
func BoolMember(obj *Value, name, where string) (bool, bool, error) {
	m, ok := obj.Member(name)
	if !ok {
		return false, false, nil
	}
	if m.Kind != ValueBool {
		return false, true, fmt.Errorf("member %q in %s is a %s, not a boolean", name, where, m.Kind)
	}
	return m.Bool, true, nil
}

// Int64Member reads an optional integer member within the inclusive range
// [min, max]. A fraction or an exponent is rejected even when the value is
// integral, so 1.0 and 1e0 are not accepted spellings of 1.
func Int64Member(obj *Value, name, where string, min, max int64) (int64, bool, error) {
	m, ok := obj.Member(name)
	if !ok {
		return 0, false, nil
	}
	if m.Kind != ValueNumber {
		return 0, true, fmt.Errorf("member %q in %s is a %s, not an integer", name, where, m.Kind)
	}
	n, err := parseInt64Lexeme(m.Num)
	if err != nil {
		return 0, true, fmt.Errorf("member %q in %s: %v", name, where, err)
	}
	if n < min || n > max {
		return 0, true, fmt.Errorf("member %q in %s is %d, outside %d..%d", name, where, n, min, max)
	}
	return n, true, nil
}

// StringArrayMember reads an optional array of strings. An element of another
// type fails the member; it is never coerced and never skipped.
func StringArrayMember(obj *Value, name, where string, maxElements int) ([]string, bool, error) {
	m, ok := obj.Member(name)
	if !ok {
		return nil, false, nil
	}
	if m.Kind != ValueArray {
		return nil, true, fmt.Errorf("member %q in %s is a %s, not an array", name, where, m.Kind)
	}
	if len(m.Array) > maxElements {
		return nil, true, fmt.Errorf("member %q in %s has %d elements, the maximum is %d",
			name, where, len(m.Array), maxElements)
	}
	out := make([]string, 0, len(m.Array))
	for i, el := range m.Array {
		if el.Kind != ValueString {
			return nil, true, fmt.Errorf("element %d of member %q in %s is a %s, not a string",
				i, name, where, el.Kind)
		}
		out = append(out, el.Str)
	}
	return out, true, nil
}

// ObjectMember reads an optional object member.
func ObjectMember(obj *Value, name, where string) (*Value, bool, error) {
	m, ok := obj.Member(name)
	if !ok {
		return nil, false, nil
	}
	if m.Kind != ValueObject {
		return nil, true, fmt.Errorf("member %q in %s is a %s, not an object", name, where, m.Kind)
	}
	return m, true, nil
}
