package protocol

import (
	"fmt"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// This file implements the strict JSON reader the envelope requires.
//
// encoding/json cannot be used for it. Three of this profile's rules are
// outside what the standard decoder offers at any setting: it resolves
// duplicate member names by last-wins rather than rejecting them, it cannot
// distinguish an explicit null from an absent member when decoding into a
// struct, and -- decisively -- it substitutes U+FFFD for unpaired surrogate
// escapes. Silently rewriting a decoded string would corrupt log content and,
// in an id or method, would rewrite a routing key.
//
// Syntax errors stop the parse: a message that is not JSON cannot be
// attributed, so the fault is link-scoped. Strictness faults -- a duplicate
// member name, an undefined or unpaired escape, a malformed number -- are
// recorded with their path and the parse continues, because whether such a
// fault is link-scoped or request-scoped depends on whether it landed in a
// routing member, and that is not known until the routing members have been
// read.

// ValueKind discriminates a parsed JSON value. Null is a kind of its own:
// this profile rejects null wherever a member is specified, and never reads it
// as absence.
type ValueKind uint8

const (
	ValueNull ValueKind = iota
	ValueBool
	ValueNumber
	ValueString
	ValueArray
	ValueObject
)

func (k ValueKind) String() string {
	switch k {
	case ValueNull:
		return "null"
	case ValueBool:
		return "boolean"
	case ValueNumber:
		return "number"
	case ValueString:
		return "string"
	case ValueArray:
		return "array"
	case ValueObject:
		return "object"
	default:
		return "unknown"
	}
}

// Value is a parsed JSON value. Numbers are retained as their raw lexeme so
// that integer members convert through a lossless decimal path rather than a
// binary64 intermediate.
type Value struct {
	Kind   ValueKind
	Bool   bool
	Num    string
	Str    string
	Array  []*Value
	Object map[string]*Value
	// Keys preserves member order for diagnostics.
	Keys []string
}

// Member returns the named member and whether it is present. An explicit null
// is present -- callers reject it rather than treating it as absent.
func (v *Value) Member(name string) (*Value, bool) {
	if v == nil || v.Kind != ValueObject {
		return nil, false
	}
	m, ok := v.Object[name]
	return m, ok
}

// fault records a strictness violation together with the path at which it
// occurred, so its scope can be decided after the routing members are read.
type fault struct {
	path string
	msg  string
}

type parser struct {
	buf    []byte
	pos    int
	depth  int
	path   []string
	faults []fault
}

func (p *parser) record(msg string) {
	// Only the first few faults are worth keeping; the message is rejected
	// either way and an attacker must not be able to grow this slice.
	if len(p.faults) >= 8 {
		return
	}
	p.faults = append(p.faults, fault{path: strings.Join(p.path, "."), msg: msg})
}

func (p *parser) errorf(format string, args ...any) error {
	return fmt.Errorf("at byte %d: %s", p.pos, fmt.Sprintf(format, args...))
}

func (p *parser) skipWhitespace() {
	for p.pos < len(p.buf) {
		switch p.buf[p.pos] {
		case ' ', '\t', '\n', '\r':
			p.pos++
		default:
			return
		}
	}
}

func (p *parser) peek() (byte, bool) {
	if p.pos >= len(p.buf) {
		return 0, false
	}
	return p.buf[p.pos], true
}

// parseDocument parses a complete JSON document and rejects trailing content.
func (p *parser) parseDocument() (*Value, error) {
	p.skipWhitespace()
	v, err := p.parseValue()
	if err != nil {
		return nil, err
	}
	p.skipWhitespace()
	if p.pos != len(p.buf) {
		return nil, p.errorf("trailing content after top-level value")
	}
	return v, nil
}

func (p *parser) parseValue() (*Value, error) {
	c, ok := p.peek()
	if !ok {
		return nil, p.errorf("unexpected end of input")
	}
	switch c {
	case '{':
		return p.parseObject()
	case '[':
		return p.parseArray()
	case '"':
		s, err := p.parseString()
		if err != nil {
			return nil, err
		}
		return &Value{Kind: ValueString, Str: s}, nil
	case 't':
		if err := p.expectLiteral("true"); err != nil {
			return nil, err
		}
		return &Value{Kind: ValueBool, Bool: true}, nil
	case 'f':
		if err := p.expectLiteral("false"); err != nil {
			return nil, err
		}
		return &Value{Kind: ValueBool, Bool: false}, nil
	case 'n':
		if err := p.expectLiteral("null"); err != nil {
			return nil, err
		}
		return &Value{Kind: ValueNull}, nil
	default:
		return p.parseNumber()
	}
}

func (p *parser) expectLiteral(lit string) error {
	if p.pos+len(lit) > len(p.buf) || string(p.buf[p.pos:p.pos+len(lit)]) != lit {
		return p.errorf("invalid literal, expected %q", lit)
	}
	p.pos += len(lit)
	return nil
}

func (p *parser) enter() error {
	p.depth++
	// Depth is a resource guard, not a strictness rule: the parse cannot
	// continue past it, so exceeding it stops the parse outright.
	if p.depth > MaxNestingDepth {
		return p.errorf("nesting deeper than %d", MaxNestingDepth)
	}
	return nil
}

func (p *parser) parseObject() (*Value, error) {
	if err := p.enter(); err != nil {
		return nil, err
	}
	defer func() { p.depth-- }()

	p.pos++ // consume '{'
	v := &Value{Kind: ValueObject, Object: map[string]*Value{}}

	p.skipWhitespace()
	if c, ok := p.peek(); ok && c == '}' {
		p.pos++
		return v, nil
	}

	for {
		p.skipWhitespace()
		if c, ok := p.peek(); !ok || c != '"' {
			return nil, p.errorf("expected member name")
		}
		key, err := p.parseString()
		if err != nil {
			return nil, err
		}

		p.skipWhitespace()
		if c, ok := p.peek(); !ok || c != ':' {
			return nil, p.errorf("expected ':' after member name")
		}
		p.pos++
		p.skipWhitespace()

		p.path = append(p.path, key)
		member, err := p.parseValue()
		if err != nil {
			return nil, err
		}

		if _, exists := v.Object[key]; exists {
			// RFC 8259 leaves duplicate handling to the parser, so a
			// security-relevant field resolved by last-wins is not
			// trustworthy. Record it and continue so the parse can reach the
			// routing members and decide the fault's scope.
			p.record("duplicate member name")
		} else {
			v.Keys = append(v.Keys, key)
		}
		v.Object[key] = member
		p.path = p.path[:len(p.path)-1]

		p.skipWhitespace()
		c, ok := p.peek()
		if !ok {
			return nil, p.errorf("unexpected end of object")
		}
		switch c {
		case ',':
			p.pos++
		case '}':
			p.pos++
			return v, nil
		default:
			return nil, p.errorf("expected ',' or '}' in object")
		}
	}
}

func (p *parser) parseArray() (*Value, error) {
	if err := p.enter(); err != nil {
		return nil, err
	}
	defer func() { p.depth-- }()

	p.pos++ // consume '['
	v := &Value{Kind: ValueArray}

	p.skipWhitespace()
	if c, ok := p.peek(); ok && c == ']' {
		p.pos++
		return v, nil
	}

	for i := 0; ; i++ {
		p.skipWhitespace()
		p.path = append(p.path, fmt.Sprintf("[%d]", i))
		item, err := p.parseValue()
		if err != nil {
			return nil, err
		}
		p.path = p.path[:len(p.path)-1]
		v.Array = append(v.Array, item)

		p.skipWhitespace()
		c, ok := p.peek()
		if !ok {
			return nil, p.errorf("unexpected end of array")
		}
		switch c {
		case ',':
			p.pos++
		case ']':
			p.pos++
			return v, nil
		default:
			return nil, p.errorf("expected ',' or ']' in array")
		}
	}
}

// parseString decodes a JSON string. Escape handling is strict: an undefined
// escape, an unpaired surrogate, a reversed pair or an incomplete pair is
// recorded as a fault. U+FFFD is never substituted -- the message is rejected
// instead, so a decoded string is always a sequence of Unicode scalar values.
func (p *parser) parseString() (string, error) {
	p.pos++ // consume opening '"'
	var sb strings.Builder

	for {
		if p.pos >= len(p.buf) {
			return "", p.errorf("unterminated string")
		}
		c := p.buf[p.pos]

		switch {
		case c == '"':
			p.pos++
			return sb.String(), nil

		case c < 0x20:
			// RFC 8259 requires control characters to be escaped.
			return "", p.errorf("unescaped control character 0x%02x in string", c)

		case c == '\\':
			p.pos++
			if p.pos >= len(p.buf) {
				return "", p.errorf("unterminated escape sequence")
			}
			e := p.buf[p.pos]
			p.pos++
			switch e {
			case '"':
				sb.WriteByte('"')
			case '\\':
				sb.WriteByte('\\')
			case '/':
				sb.WriteByte('/')
			case 'b':
				sb.WriteByte('\b')
			case 'f':
				sb.WriteByte('\f')
			case 'n':
				sb.WriteByte('\n')
			case 'r':
				sb.WriteByte('\r')
			case 't':
				sb.WriteByte('\t')
			case 'u':
				r, err := p.parseUnicodeEscape()
				if err != nil {
					return "", err
				}
				sb.WriteRune(r)
			default:
				// An undefined escape is a strictness fault, not a syntax
				// error: the message can still be attributed if the fault is
				// outside the routing members.
				p.record("undefined escape sequence")
				sb.WriteByte(e)
			}

		default:
			// The message was validated as UTF-8 before the parse, so a
			// multi-byte sequence can be copied through unchanged.
			_, size := utf8.DecodeRune(p.buf[p.pos:])
			sb.Write(p.buf[p.pos : p.pos+size])
			p.pos += size
		}
	}
}

// parseUnicodeEscape reads one \uXXXX escape and, when it opens a surrogate
// pair, the \uXXXX that must complete it.
func (p *parser) parseUnicodeEscape() (rune, error) {
	hi, err := p.readHex4()
	if err != nil {
		return 0, err
	}

	if !utf16.IsSurrogate(rune(hi)) {
		return rune(hi), nil
	}

	// A low surrogate arriving first is a reversed pair.
	if hi >= 0xDC00 {
		p.record("unpaired low surrogate escape")
		return utf8.RuneError, nil
	}

	// A high surrogate must be followed by \uXXXX naming a low surrogate.
	if p.pos+1 >= len(p.buf) || p.buf[p.pos] != '\\' || p.buf[p.pos+1] != 'u' {
		p.record("high surrogate escape not followed by a low surrogate")
		return utf8.RuneError, nil
	}
	p.pos += 2
	lo, err := p.readHex4()
	if err != nil {
		return 0, err
	}
	r := utf16.DecodeRune(rune(hi), rune(lo))
	if r == utf8.RuneError {
		p.record("invalid surrogate pair")
	}
	return r, nil
}

func (p *parser) readHex4() (uint32, error) {
	if p.pos+4 > len(p.buf) {
		return 0, p.errorf("truncated \\u escape")
	}
	var val uint32
	for i := 0; i < 4; i++ {
		c := p.buf[p.pos+i]
		var d uint32
		switch {
		case c >= '0' && c <= '9':
			d = uint32(c - '0')
		case c >= 'a' && c <= 'f':
			d = uint32(c-'a') + 10
		case c >= 'A' && c <= 'F':
			d = uint32(c-'A') + 10
		default:
			return 0, p.errorf("invalid hex digit %q in \\u escape", c)
		}
		val = val<<4 | d
	}
	p.pos += 4
	return val, nil
}

// parseNumber reads a number and retains its raw lexeme. Whether the lexeme is
// an acceptable integer for a given member is decided at extraction, because
// only some members are numeric and each carries its own range.
func (p *parser) parseNumber() (*Value, error) {
	start := p.pos

	if c, ok := p.peek(); ok && c == '-' {
		p.pos++
	}

	// int: either a single 0 or a nonzero digit followed by digits.
	c, ok := p.peek()
	if !ok {
		return nil, p.errorf("unexpected end of number")
	}
	switch {
	case c == '0':
		p.pos++
	case c >= '1' && c <= '9':
		for p.pos < len(p.buf) && p.buf[p.pos] >= '0' && p.buf[p.pos] <= '9' {
			p.pos++
		}
	default:
		return nil, p.errorf("invalid value")
	}

	// frac
	if p.pos < len(p.buf) && p.buf[p.pos] == '.' {
		p.pos++
		digits := 0
		for p.pos < len(p.buf) && p.buf[p.pos] >= '0' && p.buf[p.pos] <= '9' {
			p.pos++
			digits++
		}
		if digits == 0 {
			return nil, p.errorf("missing digits after decimal point")
		}
	}

	// exp
	if p.pos < len(p.buf) && (p.buf[p.pos] == 'e' || p.buf[p.pos] == 'E') {
		p.pos++
		if p.pos < len(p.buf) && (p.buf[p.pos] == '+' || p.buf[p.pos] == '-') {
			p.pos++
		}
		digits := 0
		for p.pos < len(p.buf) && p.buf[p.pos] >= '0' && p.buf[p.pos] <= '9' {
			p.pos++
			digits++
		}
		if digits == 0 {
			return nil, p.errorf("missing digits in exponent")
		}
	}

	return &Value{Kind: ValueNumber, Num: string(p.buf[start:p.pos])}, nil
}

// parseJSON parses msg strictly. It returns the value, any recorded
// strictness faults, and a syntax error if the input is not JSON at all.
//
// The caller must have validated that msg is well-formed UTF-8 before calling.
func parseJSON(msg []byte) (*Value, []fault, error) {
	p := &parser{buf: msg}
	v, err := p.parseDocument()
	if err != nil {
		return nil, nil, err
	}
	return v, p.faults, nil
}

// parseInt64Lexeme converts an integer decimal lexeme directly to int64 with
// an explicit range check.
//
// The conversion never passes through a binary64 intermediate, which would
// silently lose precision above 2^53. RFC 8259 notes that interoperability can
// only be expected up to 2^53-1, but that is a caution about other
// implementations and not a normative JSON maximum; this profile does not
// adopt it as one.
func parseInt64Lexeme(lexeme string) (int64, error) {
	if lexeme == "" {
		return 0, fmt.Errorf("empty number")
	}
	if strings.ContainsAny(lexeme, ".eE") {
		// 1.0 and 1e0 are rejected even though integral.
		return 0, fmt.Errorf("number %q has a fraction or exponent", lexeme)
	}

	digits := lexeme
	negative := false
	if strings.HasPrefix(digits, "-") {
		negative = true
		digits = digits[1:]
	}
	if digits == "" {
		return 0, fmt.Errorf("number %q has no digits", lexeme)
	}
	// Bound the lexeme before conversion rather than after.
	if len(digits) > MaxIntegerDigits {
		return 0, fmt.Errorf("number %q exceeds %d digits", lexeme, MaxIntegerDigits)
	}

	var magnitude uint64
	for i := 0; i < len(digits); i++ {
		c := digits[i]
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("number %q is not an integer", lexeme)
		}
		magnitude = magnitude*10 + uint64(c-'0')
	}

	if negative {
		if magnitude > 1<<63 {
			return 0, fmt.Errorf("number %q underflows int64", lexeme)
		}
		if magnitude == 1<<63 {
			return -1 << 63, nil
		}
		return -int64(magnitude), nil
	}
	if magnitude > 1<<63-1 {
		return 0, fmt.Errorf("number %q overflows int64", lexeme)
	}
	return int64(magnitude), nil
}
