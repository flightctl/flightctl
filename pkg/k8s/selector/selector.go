//go:build !lint
// +build !lint

/*
Copyright 2014 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.

Modifications by Assaf Albo (asafbss): Added support for the containment operator and unified fields and labels selector.
*/

package selector

import (
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/flightctl/flightctl/pkg/k8s/selector/selection"
	"github.com/google/uuid"
	k8sLabels "k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/klog/v2"
)

var (
	unaryOperators = []string{
		string(selection.Exists), string(selection.DoesNotExist),
	}
	binaryOperators = []string{
		string(selection.In), string(selection.NotIn),
		string(selection.Equals), string(selection.DoubleEquals), string(selection.NotEquals),
		string(selection.Contains), string(selection.NotContains),
		string(selection.GreaterThan), string(selection.GreaterThanOrEquals),
		string(selection.LessThan), string(selection.LessThanOrEquals),
	}
	validRequirementOperators = append(binaryOperators, unaryOperators...)
)

// Requirements is AND of all requirements.
type Requirements []Requirement

func (r Requirements) String() string {
	var sb strings.Builder

	for i, requirement := range r {
		if i > 0 {
			sb.WriteString(", ")
		}
		sb.WriteString(requirement.String())
	}

	return sb.String()
}

// Selector represents a label selector.
type Selector interface {
	// Matches returns true if this selector matches the given set of k8sLabels.
	Matches(k8sLabels.Labels) bool

	// Empty returns true if this selector does not restrict the selection space.
	Empty() bool

	// String returns a human readable string that represents this selector.
	String() string

	// Add adds requirements to the Selector
	Add(r ...Requirement) Selector

	// Requirements converts this interface into Requirements to expose
	// more detailed selection information.
	// If there are querying parameters, it will return converted requirements and selectable=true.
	// If this selector doesn't want to select anything, it will return selectable=false.
	Requirements() (requirements Requirements, selectable bool)

	// Make a deep copy of the selector.
	DeepCopySelector() Selector

	// RequiresExactMatch allows a caller to introspect whether a given selector
	// requires a single specific label to be set, and if so returns the value it
	// requires.
	RequiresExactMatch(label string) (value string, found bool)
}

// Sharing this saves 1 alloc per use; this is safe because it's immutable.
var sharedEverythingSelector Selector = internalSelector{}

// Everything returns a selector that matches all k8sLabels.
func Everything() Selector {
	return sharedEverythingSelector
}

type nothingSelector struct{}

func (n nothingSelector) Matches(_ k8sLabels.Labels) bool    { return false }
func (n nothingSelector) Empty() bool                        { return false }
func (n nothingSelector) String() string                     { return "" }
func (n nothingSelector) Add(_ ...Requirement) Selector      { return n }
func (n nothingSelector) Requirements() (Requirements, bool) { return nil, false }
func (n nothingSelector) DeepCopySelector() Selector         { return n }
func (n nothingSelector) RequiresExactMatch(label string) (value string, found bool) {
	return "", false
}

// Sharing this saves 1 alloc per use; this is safe because it's immutable.
var sharedNothingSelector Selector = nothingSelector{}

// Nothing returns a selector that matches no labels
func Nothing() Selector {
	return sharedNothingSelector
}

// NewSelector returns a nil selector
func NewSelector() Selector {
	return internalSelector(nil)
}

type internalSelector []Requirement

func (s internalSelector) DeepCopy() internalSelector {
	if s == nil {
		return nil
	}
	result := make([]Requirement, len(s))
	for i := range s {
		s[i].DeepCopyInto(&result[i])
	}
	return result
}

func (s internalSelector) DeepCopySelector() Selector {
	return s.DeepCopy()
}

type Tuple []string

func tuple(elements ...string) Tuple {
	for i := range elements {
		elements[i] = strings.TrimSpace(elements[i])
	}
	return Tuple(elements)
}

func (t Tuple) String() string {
	switch len(t) {
	case 0:
		return ""
	case 1:
		return t[0]
	default:
		return fmt.Sprintf("(%s)", strings.Join(t, ","))
	}
}

// ByKey sorts requirements by key to obtain deterministic parser
type ByKey []Requirement

func (a ByKey) Len() int { return len(a) }

func (a ByKey) Swap(i, j int) { a[i], a[j] = a[j], a[i] }

func (a ByKey) Less(i, j int) bool { return a[i].key.String() < a[j].key.String() }

// Requirement contains values, a key, and an operator that relates the key and values.
// The zero value of Requirement is invalid.
// Requirement implements both set based match and exact match
// Requirement should be initialized via NewRequirement constructor for creating a valid Requirement.
// +k8s:deepcopy-gen=true
type Requirement struct {
	key      Tuple
	operator selection.Operator
	// In huge majority of cases we have at most one value here.
	// It is generally faster to operate on a single-element slice
	// than on a single-element map, so we have a slice here.
	strValues []Tuple
}

// NewRequirement is the constructor for a Requirement.
// If any of these rules is violated, an error is returned:
//  1. The operator can only be In, NotIn, Equals, DoubleEquals, Contains, NotContains, Gt, Lt, Gte, Lte, NotEquals, Exists, or DoesNotExist.
//  2. If the operator is In or NotIn, the values set must be non-empty.
//  3. If the operator is Equals, DoubleEquals, or NotEquals, the values set must contain one value.
//  4. If the operator is Exists or DoesNotExist, the value set must be empty.
//  5. If the operator is Gt, Lt, Gte, Lte, the values set must contain only one value.
//
// The empty string is a valid value in the input values set.
// Returned error, if not nil, is guaranteed to be an aggregated field.ErrorList
func NewRequirement(key Tuple, op selection.Operator, vals []Tuple, opts ...field.PathOption) (*Requirement, error) {
	var allErrs field.ErrorList
	path := field.ToPath(opts...)

	valuePath := path.Child("values")
	switch op {
	case selection.In, selection.NotIn:
		if len(vals) == 0 {
			allErrs = append(allErrs, field.Invalid(valuePath, vals, "for 'in', 'notin' operators, values set can't be empty"))
		}
	case selection.Equals, selection.DoubleEquals, selection.NotEquals:
		if len(vals) != 1 {
			allErrs = append(allErrs, field.Invalid(valuePath, vals, "exact-match compatibility requires one single value"))
		}
	case selection.Contains, selection.NotContains:
		if len(vals) != 1 {
			allErrs = append(allErrs, field.Invalid(valuePath, vals, "partial-match compatibility requires one single value"))
		}
	case selection.Exists, selection.DoesNotExist:
		if len(vals) != 0 {
			allErrs = append(allErrs, field.Invalid(valuePath, vals, "values set must be empty for exists and does not exist"))
		}
	case selection.GreaterThan, selection.LessThan, selection.GreaterThanOrEquals, selection.LessThanOrEquals:
		if len(vals) != 1 {
			allErrs = append(allErrs, field.Invalid(valuePath, vals, "for 'Gt', 'Lt', 'Gte', Lte operators, exactly one value is required"))
		}
		for i := range vals {
			for j := range vals[i] {
				if _, err := strconv.ParseInt(vals[i][j], 10, 64); err != nil {
					if _, err := time.Parse(time.RFC3339, vals[i][j]); err != nil {
						if _, err := uuid.Parse(vals[i][j]); err != nil {
							allErrs = append(allErrs, field.Invalid(valuePath.Index(i), vals[i][j], "for 'Gt', 'Lt', 'Gte', and 'Lte' operators, the value must be a number or a valid time in RFC3339 format"))
						}
					}
				}
			}
		}
	default:
		allErrs = append(allErrs, field.NotSupported(path.Child("operator"), op, validRequirementOperators))
	}
	return &Requirement{key: key, operator: op, strValues: vals}, allErrs.ToAggregate()
}

func (r *Requirement) hasValue(value Tuple) bool {
	for i := range r.strValues {
		if slices.Equal(r.strValues[i], value) {
			return true
		}
	}
	return false
}

// Matches returns true if the Requirement matches the input k8sLabels.
// There is a match in the following cases:
//  1. The operator is Exists and Labels has the Requirement's key.
//  2. The operator is In, Labels has the Requirement's key and Labels'
//     value for that key is in Requirement's value set.
//  3. The operator is NotIn, Labels has the Requirement's key and
//     Labels' value for that key is not in Requirement's value set.
//  4. The operator is DoesNotExist or NotIn and Labels does not have the
//     Requirement's key.
//  5. The operator is GreaterThanOperator or LessThanOperator, and Labels has
//     the Requirement's key and the corresponding value satisfies mathematical inequality.
//
//nolint:gocyclo
func (r *Requirement) Matches(ls k8sLabels.Labels) bool {
	switch r.operator {
	case selection.In, selection.Equals, selection.DoubleEquals:
		v := make(Tuple, len(r.key))
		for i := range r.key {
			if !ls.Has(r.key[i]) {
				return false
			}
			v[i] = ls.Get(r.key[i])
		}
		return r.hasValue(tuple(v...))

	case selection.Contains:
		v := make(Tuple, len(r.key))
		for i := range r.key {
			if !ls.Has(r.key[i]) {
				return false
			}
			v[i] = ls.Get(r.key[i])
		}
		if len(r.strValues) != 1 {
			klog.V(10).Infof("Invalid values count %+v of requirement %#v, for 'Contains' operator, exactly one value is required", len(r.strValues), r)
			return false
		}
		if len(r.strValues[0]) != len(r.key) {
			klog.V(10).Infof("Mismatch between key count (%d) and strValues[0] length (%d)", len(r.key), len(r.strValues[0]))
			return false
		}
		for i := range r.strValues[0] {
			if !strings.Contains(v[i], r.strValues[0][i]) {
				return false
			}
		}
		return true

	case selection.NotContains:
		v := make(Tuple, len(r.key))
		for i := range r.key {
			if !ls.Has(r.key[i]) {
				return true
			}
			v[i] = ls.Get(r.key[i])
		}
		if len(r.strValues) != 1 {
			klog.V(10).Infof("Invalid values count %+v of requirement %#v, for 'Contains' operator, exactly one value is required", len(r.strValues), r)
			return false
		}
		if len(r.strValues[0]) != len(r.key) {
			klog.V(10).Infof("Mismatch between key count (%d) and strValues[0] length (%d)", len(r.key), len(r.strValues[0]))
			return false
		}
		for i := range r.strValues[0] {
			if !strings.Contains(v[i], r.strValues[0][i]) {
				return true
			}
		}
		return false

	case selection.NotIn, selection.NotEquals:
		v := make(Tuple, len(r.key))
		for i := range r.key {
			if !ls.Has(r.key[i]) {
				return true
			}
			v[i] = ls.Get(r.key[i])
		}
		return !r.hasValue(tuple(v...))

	case selection.Exists:
		for i := range r.key {
			if !ls.Has(r.key[i]) {
				return false
			}
		}
		return true

	case selection.DoesNotExist:
		for i := range r.key {
			if !ls.Has(r.key[i]) {
				return true
			}
		}
		return false

	case selection.GreaterThan, selection.LessThan, selection.GreaterThanOrEquals, selection.LessThanOrEquals:
		var err error

		// Parse the tuple values from the labels
		v := make([]int64, len(r.key))
		for i := range r.key {
			if !ls.Has(r.key[i]) {
				return false
			}
			v[i], err = strconv.ParseInt(ls.Get(r.key[i]), 10, 64)
			if err != nil {
				klog.V(10).Infof("ParseInt failed for value %+v in label %+v, %+v", ls.Get(r.key[i]), ls, err)
				return false
			}
		}

		// There should be only one strValue in r.strValues, and can be converted to an integer.
		if len(r.strValues) != 1 {
			klog.V(10).Infof("Invalid values count %+v of requirement %#v, for 'Gt', 'Lt' operators, exactly one value is required", len(r.strValues), r)
			return false
		}

		if len(r.strValues[0]) != len(r.key) {
			klog.V(10).Infof("Mismatch between key count (%d) and strValues[0] length (%d)", len(r.key), len(r.strValues[0]))
			return false
		}

		rValues := make([]int64, len(r.strValues[0]))
		for i := range r.strValues[0] {
			rValues[i], err = strconv.ParseInt(r.strValues[0][i], 10, 64)
			if err != nil {
				klog.V(10).Infof("ParseInt failed for value %+v in requirement %#v, for 'Gt', 'Lt' operators, the value must be an integer", r.strValues[0][i], r)
				return false
			}
		}

		// Perform lexicographical comparison
		for i := range v {
			switch r.operator {
			case selection.GreaterThan:
				if v[i] < rValues[i] {
					return false // Lexicographically less
				} else if v[i] > rValues[i] {
					return true // Lexicographically greater
				}
			case selection.LessThan:
				if v[i] > rValues[i] {
					return false // Lexicographically greater
				} else if v[i] < rValues[i] {
					return true // Lexicographically less
				}
			case selection.GreaterThanOrEquals:
				if v[i] < rValues[i] {
					return false // Lexicographically less
				} else if v[i] > rValues[i] {
					return true // Lexicographically greater
				}
			case selection.LessThanOrEquals:
				if v[i] > rValues[i] {
					return false // Lexicographically greater
				} else if v[i] < rValues[i] {
					return true // Lexicographically less
				}
			}
		}

		// If all elements are equal, handle equality at the end
		switch r.operator {
		case selection.GreaterThan, selection.LessThan:
			return false // Equal tuples do not satisfy strict inequality
		case selection.GreaterThanOrEquals, selection.LessThanOrEquals:
			return true // Equal tuples satisfy non-strict inequality
		}
		return false

	default:
		return false
	}
}

// Key returns requirement key
func (r *Requirement) Key() Tuple {
	return r.key
}

// Operator returns requirement operator
func (r *Requirement) Operator() selection.Operator {
	return r.operator
}

// Values returns requirement values
func (r *Requirement) Values() []Tuple {
	t := make([]Tuple, 0, len(r.strValues))
	s := sets.NewString()
	for i := range r.strValues {
		if !s.Has(r.strValues[i].String()) {
			s.Insert(r.strValues[i].String())
			t = append(t, r.strValues[i])
		}
	}
	return t
}

// Equal checks the equality of requirement.
func (r Requirement) Equal(x Requirement) bool {
	if !slices.Equal(r.key, x.key) {
		return false
	}
	if r.operator != x.operator {
		return false
	}

	if len(r.strValues) != len(x.strValues) {
		return false
	}

	for i := range r.strValues {
		if !slices.Equal(r.strValues[i], x.strValues[i]) {
			return false
		}
	}
	return true
}

// Empty returns true if the internalSelector doesn't restrict selection space
func (s internalSelector) Empty() bool {
	if s == nil {
		return true
	}
	return len(s) == 0
}

// String returns a human-readable string that represents this
// Requirement. If called on an invalid Requirement, an error is
// returned. See NewRequirement for creating a valid Requirement.
func (r *Requirement) String() string {
	var sb strings.Builder
	sb.Grow(
		// length of r.key
		len(r.key.String()) +
			// length of 'r.operator' + 2 spaces for the worst case ('in', 'notin' and 'contains', 'notcontains')
			len(r.operator) + 2 +
			// length of 'r.strValues' slice times. Heuristically 5 chars per word
			+5*len(r.strValues))
	if r.operator == selection.DoesNotExist {
		sb.WriteString("!")
	}
	sb.WriteString(r.key.String())

	switch r.operator {
	case selection.Equals:
		sb.WriteString("=")
	case selection.DoubleEquals:
		sb.WriteString("==")
	case selection.NotEquals:
		sb.WriteString("!=")
	case selection.Contains:
		sb.WriteString(" contains ")
	case selection.NotContains:
		sb.WriteString(" notcontains ")
	case selection.In:
		sb.WriteString(" in ")
	case selection.NotIn:
		sb.WriteString(" notin ")
	case selection.GreaterThan:
		sb.WriteString(">")
	case selection.GreaterThanOrEquals:
		sb.WriteString(">=")
	case selection.LessThan:
		sb.WriteString("<")
	case selection.LessThanOrEquals:
		sb.WriteString("<=")
	case selection.Exists, selection.DoesNotExist:
		return sb.String()
	}

	switch r.operator {
	case selection.In, selection.NotIn:
		sb.WriteString("(")
	}
	if len(r.strValues) == 1 {
		sb.WriteString(r.strValues[0].String())
	} else { // only > 1 since == 0 prohibited by NewRequirement
		// normalizes value order on output, without mutating the in-memory selector representation
		// also avoids normalization when it is not required, and ensures we do not mutate shared data
		sb.WriteString(strings.Join(func() []string {
			s := make([]string, len(r.strValues))
			for i, v := range r.strValues {
				s[i] = v.String()
			}
			return s
		}(), ","))
	}

	switch r.operator {
	case selection.In, selection.NotIn:
		sb.WriteString(")")
	}
	return sb.String()
}

// Add adds requirements to the selector. It copies the current selector returning a new one
func (s internalSelector) Add(reqs ...Requirement) Selector {
	ret := make(internalSelector, 0, len(s)+len(reqs))
	ret = append(ret, s...)
	ret = append(ret, reqs...)
	sort.Sort(ByKey(ret))
	return ret
}

// Matches for a internalSelector returns true if all
// its Requirements match the input k8sLabels. If any
// Requirement does not match, false is returned.
func (s internalSelector) Matches(l k8sLabels.Labels) bool {
	for ix := range s {
		if matches := s[ix].Matches(l); !matches {
			return false
		}
	}
	return true
}

func (s internalSelector) Requirements() (Requirements, bool) { return Requirements(s), true }

// String returns a comma-separated string of all
// the internalSelector Requirements' human-readable strings.
func (s internalSelector) String() string {
	var reqs []string
	for ix := range s {
		reqs = append(reqs, s[ix].String())
	}
	return strings.Join(reqs, ",")
}

// RequiresExactMatch introspects whether a given selector requires a single specific field
// to be set, and if so returns the value it requires.
func (s internalSelector) RequiresExactMatch(label string) (value string, found bool) {
	for ix := range s {
		for i := range s[ix].key {
			if s[ix].key[i] == label {
				switch s[ix].operator {
				case selection.Equals, selection.DoubleEquals, selection.In:
					if len(s[ix].strValues) == 1 && i < len(s[ix].strValues[0]) {
						return s[ix].strValues[0][i], true
					}
				}
			}
		}
	}
	return "", false
}

// Token represents constant definition for lexer token
type Token int

const (
	// ErrorToken represents scan error
	ErrorToken Token = iota
	// EndOfStringToken represents end of string
	EndOfStringToken
	// ClosedParToken represents close parenthesis
	ClosedParToken
	// CommaToken represents the comma
	CommaToken
	// DoesNotExistToken represents logic not
	DoesNotExistToken
	// DoubleEqualsToken represents double equals
	DoubleEqualsToken
	// EqualsToken represents equal
	EqualsToken
	// GreaterThanToken represents greater than
	GreaterThanToken
	// GreaterThanOrEqualsToken represents greater than or equal
	GreaterThanOrEqualsToken
	// IdentifierToken represents identifier, e.g. keys and values
	IdentifierToken
	// ContainsToken represents contains
	ContainsToken
	// InToken represents in
	InToken
	// LessThanToken represents less than or equal
	LessThanToken
	// LessThanOrEqualsToken represents less than
	LessThanOrEqualsToken
	// NotEqualsToken represents not equal
	NotEqualsToken
	// NotContainsToken represents not contains
	NotContainsToken
	// NotInToken represents not in
	NotInToken
	// OpenParToken represents open parenthesis
	OpenParToken
)

// string2token contains the mapping between lexer Token and token literal
// (except IdentifierToken, EndOfStringToken and ErrorToken since it makes no sense)
var string2token = map[string]Token{
	")":           ClosedParToken,
	",":           CommaToken,
	"!":           DoesNotExistToken,
	"==":          DoubleEqualsToken,
	"=":           EqualsToken,
	">":           GreaterThanToken,
	">=":          GreaterThanOrEqualsToken,
	"contains":    ContainsToken,
	"in":          InToken,
	"<":           LessThanToken,
	"<=":          LessThanOrEqualsToken,
	"!=":          NotEqualsToken,
	"notcontains": NotContainsToken,
	"notin":       NotInToken,
	"(":           OpenParToken,
}

// ScannedItem contains the Token and the literal produced by the lexer.
type ScannedItem struct {
	tok     Token
	literal string
}

// isWhitespace returns true if the rune is a space, tab, or newline.
func isWhitespace(ch byte) bool {
	return ch == ' ' || ch == '\t' || ch == '\r' || ch == '\n'
}

// isSpecialSymbol detects if the character ch can be an operator
func isSpecialSymbol(ch byte) bool {
	switch ch {
	case '=', '!', '(', ')', ',', '>', '<':
		return true
	}
	return false
}

type Lexer interface {
	Lex() (tok Token, lit string)
}

// Lexer represents the Lexer struct for label selector.
// It contains necessary informationt to tokenize the input string
type lexer struct {
	// s stores the string to be tokenized
	s string
	// pos is the position currently tokenized
	pos int
}

// read returns the character currently lexed
// increment the position and check the buffer overflow
func (l *lexer) read() (b byte) {
	b = 0
	if l.pos < len(l.s) {
		b = l.s[l.pos]
		l.pos++
	}
	return b
}

// unread 'undoes' the last read character
func (l *lexer) unread() {
	l.pos--
}

// scanIDOrKeyword scans string to recognize literal token (for example 'in') or an identifier.
func (l *lexer) scanIDOrKeyword() (tok Token, lit string) {
	var buffer []byte
IdentifierLoop:
	for {
		switch ch := l.read(); {
		case ch == 0:
			break IdentifierLoop
		case isSpecialSymbol(ch) || isWhitespace(ch):
			l.unread()
			break IdentifierLoop
		default:
			buffer = append(buffer, ch)
		}
	}
	s := string(buffer)
	if val, ok := string2token[s]; ok { // is a literal token?
		return val, s
	}
	return IdentifierToken, s // otherwise is an identifier
}

// scanSpecialSymbol scans string starting with special symbol.
// special symbol identify non literal operators. "!=", "==", "="
func (l *lexer) scanSpecialSymbol() (Token, string) {
	lastScannedItem := ScannedItem{}
	var buffer []byte
SpecialSymbolLoop:
	for {
		switch ch := l.read(); {
		case ch == 0:
			break SpecialSymbolLoop
		case isSpecialSymbol(ch):
			buffer = append(buffer, ch)
			if token, ok := string2token[string(buffer)]; ok {
				lastScannedItem = ScannedItem{tok: token, literal: string(buffer)}
			} else if lastScannedItem.tok != 0 {
				l.unread()
				break SpecialSymbolLoop
			}
		default:
			l.unread()
			break SpecialSymbolLoop
		}
	}
	if lastScannedItem.tok == 0 {
		return ErrorToken, fmt.Sprintf("error expected: keyword found '%s'", buffer)
	}
	return lastScannedItem.tok, lastScannedItem.literal
}

// skipWhiteSpaces consumes all blank characters
// returning the first non blank character
func (l *lexer) skipWhiteSpaces(ch byte) byte {
	for {
		if !isWhitespace(ch) {
			return ch
		}
		ch = l.read()
	}
}

// Lex returns a pair of Token and the literal
// literal is meaningfull only for IdentifierToken token
func (l *lexer) Lex() (tok Token, lit string) {
	switch ch := l.skipWhiteSpaces(l.read()); {
	case ch == 0:
		return EndOfStringToken, ""
	case isSpecialSymbol(ch):
		l.unread()
		return l.scanSpecialSymbol()
	default:
		l.unread()
		return l.scanIDOrKeyword()
	}
}

// Parser data structure contains the label selector parser data structure
type Parser struct {
	l            Lexer
	scannedItems []ScannedItem
	position     int
	path         *field.Path
}

// ParserContext represents context during parsing:
// some literal for example 'in' and 'notin' can be
// recognized as operator for example 'x in (a)' but
// it can be recognized as value for example 'value in (in)'
type ParserContext int

const (
	// KeyAndOperator represents key and operator
	KeyAndOperator ParserContext = iota
	// Values represents values
	Values
)

// lookahead func returns the current token and string. No increment of current position
func (p *Parser) lookahead(context ParserContext) (Token, string) {
	tok, lit := p.scannedItems[p.position].tok, p.scannedItems[p.position].literal
	if context == Values {
		switch tok {
		case InToken, NotInToken:
			tok = IdentifierToken
		}
	}
	return tok, lit
}

// consume returns current token and string. Increments the position
func (p *Parser) consume(context ParserContext) (Token, string) {
	p.position++
	tok, lit := p.scannedItems[p.position-1].tok, p.scannedItems[p.position-1].literal
	if context == Values {
		switch tok {
		case InToken, NotInToken:
			tok = IdentifierToken
		}
	}
	return tok, lit
}

// scan runs through the input string and stores the ScannedItem in an array
// Parser can now lookahead and consume the tokens
func (p *Parser) scan() {
	for {
		token, literal := p.l.Lex()
		p.scannedItems = append(p.scannedItems, ScannedItem{token, literal})
		if token == EndOfStringToken {
			break
		}
	}
}

// parse runs the left recursive descending algorithm
// on input string. It returns a list of Requirement objects.
func (p *Parser) parse() (internalSelector, error) {
	p.scan() // init scannedItems

	var requirements internalSelector
	for {
		tok, lit := p.lookahead(Values)
		switch tok {
		case IdentifierToken, DoesNotExistToken, OpenParToken:
			r, err := p.parseRequirement()
			if err != nil {
				return nil, fmt.Errorf("unable to parse requirement: %v", err)
			}
			requirements = append(requirements, *r)
			t, l := p.consume(Values)
			switch t {
			case EndOfStringToken:
				return requirements, nil
			case CommaToken:
				t2, l2 := p.lookahead(Values)
				if t2 != IdentifierToken && t2 != DoesNotExistToken && t2 != OpenParToken {
					return nil, fmt.Errorf("found '%s', expected: identifier after ','", l2)
				}
			default:
				return nil, fmt.Errorf("found '%s', expected: ',' or 'end of string'", l)
			}
		case EndOfStringToken:
			return requirements, nil
		default:
			return nil, fmt.Errorf("found '%s', expected: !, identifier, or 'end of string'", lit)
		}
	}
}
func (p *Parser) parseRequirement() (*Requirement, error) {
	key, operator, err := p.parseKeyAndInferOperator()
	if err != nil {
		return nil, err
	}
	if operator == selection.Exists || operator == selection.DoesNotExist {
		return NewRequirement(key, operator, nil, field.WithPath(p.path))
	}
	operator, err = p.parseOperator()
	if err != nil {
		return nil, err
	}
	var values []Tuple
	switch operator {
	case selection.In, selection.NotIn:
		if len(key) > 1 {
			values, err = p.parseTuples()
			if err != nil {
				return nil, err
			}
		} else {
			vals, err := p.parseValues()
			if err != nil {
				return nil, err
			}
			for _, v := range vals {
				values = append(values, tuple(v))
			}
		}
	case selection.Equals, selection.DoubleEquals, selection.NotEquals, selection.Contains, selection.NotContains,
		selection.GreaterThan, selection.LessThan, selection.GreaterThanOrEquals, selection.LessThanOrEquals:
		var v []string

		if t, _ := p.lookahead(Values); t == OpenParToken {
			v, err = p.parseValues()
		} else {
			v, err = p.parseExactValue()
		}
		if err != nil {
			return nil, err
		}
		values = append(values, tuple(v...))
	}

	for _, v := range values {
		if len(key) != len(v) {
			return nil, fmt.Errorf(
				"length mismatch: key %v has %d elements but value %v has %d elements",
				key, len(key), v, len(v),
			)
		}
	}

	// Construct the requirement and return
	return NewRequirement(key, operator, values, field.WithPath(p.path))
}

// parseKeyAndInferOperator parses literals.
// in case of no operator '!, in, notin, ==, =, !=' are found
// the 'exists' operator is inferred
func (p *Parser) parseKeyAndInferOperator() (Tuple, selection.Operator, error) {
	var operator selection.Operator
	var key Tuple
	var err error

	tok, literal := p.consume(Values)
	key = Tuple{literal}

	if tok == DoesNotExistToken {
		operator = selection.DoesNotExist
		tok, literal = p.consume(Values)
		key = Tuple{literal}
	}
	// Handle multi-key parsing for keyset
	if tok == OpenParToken {
		p.position--
		key, err = p.parseValues() // Parse the keyset
		if err != nil {
			return nil, "", err
		}
		for _, k := range key {
			if strings.TrimSpace(k) == "" {
				return nil, "", fmt.Errorf("empty key found in keyset")
			}
		}
		tok, literal = IdentifierToken, key.String()
	}
	if tok != IdentifierToken {
		err := fmt.Errorf("found '%s', expected: identifier", literal)
		return nil, "", err
	}
	if t, _ := p.lookahead(Values); t == EndOfStringToken || t == CommaToken {
		if operator != selection.DoesNotExist {
			operator = selection.Exists
		}
	}
	return key, operator, nil
}

// parseOperator returns operator and eventually matchType
// matchType can be exact
func (p *Parser) parseOperator() (op selection.Operator, err error) {
	tok, lit := p.consume(KeyAndOperator)
	switch tok {
	// DoesNotExistToken shouldn't be here because it's a unary operator, not a binary operator
	case InToken:
		op = selection.In
	case EqualsToken:
		op = selection.Equals
	case DoubleEqualsToken:
		op = selection.DoubleEquals
	case ContainsToken:
		op = selection.Contains
	case GreaterThanToken:
		op = selection.GreaterThan
	case GreaterThanOrEqualsToken:
		op = selection.GreaterThanOrEquals
	case LessThanToken:
		op = selection.LessThan
	case LessThanOrEqualsToken:
		op = selection.LessThanOrEquals
	case NotInToken:
		op = selection.NotIn
	case NotEqualsToken:
		op = selection.NotEquals
	case NotContainsToken:
		op = selection.NotContains
	default:
		return "", fmt.Errorf("found '%s', expected: %v", lit, strings.Join(binaryOperators, ", "))
	}
	return op, nil
}

func (p *Parser) parseTuples() ([]Tuple, error) {
	tok, lit := p.consume(Values)
	if tok != OpenParToken {
		return nil, fmt.Errorf("found '%s', expected: '('", lit)
	}

	var ret []Tuple
	for {
		tok, lit = p.lookahead(Values)
		switch tok {
		case OpenParToken: // Start parsing a nested group
			s, err := p.parseValues() // Parse a single set of values
			if err != nil {
				return nil, fmt.Errorf("error parsing nested values: %w", err)
			}
			ret = append(ret, s)

			tok, lit = p.consume(Values)
			if tok == CommaToken {
				continue // Continue to the next group
			} else if tok == ClosedParToken {
				return ret, nil // End of the multi-value group
			} else {
				return nil, fmt.Errorf("found '%s', expected ',' or ')'", lit)
			}
		case ClosedParToken: // Empty nested group
			p.consume(Values)
			return nil, fmt.Errorf("unexpected closing parenthesis")
		default: // Invalid token
			return nil, fmt.Errorf("found '%s', expected '(' or ')'", lit)
		}
	}
}

// parseValues parses the values for set based matching (x,y,z)
func (p *Parser) parseValues() ([]string, error) {
	tok, lit := p.consume(Values)
	if tok != OpenParToken {
		return nil, fmt.Errorf("found '%s' expected: '('", lit)
	}
	tok, lit = p.lookahead(Values)
	switch tok {
	case IdentifierToken, CommaToken:
		s, err := p.parseIdentifiersList() // handles general cases
		if err != nil {
			return s, err
		}
		if tok, _ = p.consume(Values); tok != ClosedParToken {
			return nil, fmt.Errorf("found '%s', expected: ')'", lit)
		}
		return s, nil
	case ClosedParToken: // handles "()"
		p.consume(Values)
		return []string{""}, nil
	default:
		return nil, fmt.Errorf("found '%s', expected: ',', ')' or identifier", lit)
	}
}

// parseIdentifiersList parses a (possibly empty) list of
// of comma separated (possibly empty) identifiers
func (p *Parser) parseIdentifiersList() ([]string, error) {
	s := make([]string, 0)
	for {
		tok, lit := p.consume(Values)
		switch tok {
		case IdentifierToken:
			s = append(s, lit)
			tok2, lit2 := p.lookahead(Values)
			switch tok2 {
			case CommaToken:
				continue
			case ClosedParToken:
				return s, nil
			default:
				return nil, fmt.Errorf("found '%s', expected: ',' or ')'", lit2)
			}
		case CommaToken: // handled here since we can have "(,"
			if len(s) == 0 {
				s = append(s, "") // to handle (,
			}
			tok2, _ := p.lookahead(Values)
			if tok2 == ClosedParToken {
				s = append(s, "") // to handle ,)  Double "" removed by StringSet
				return s, nil
			}
			if tok2 == CommaToken {
				p.consume(Values)
				s = append(s, "") // to handle ,, Double "" removed by StringSet
			}
		default: // it can be operator
			return s, fmt.Errorf("found '%s', expected: ',', or identifier", lit)
		}
	}
}

// parseExactValue parses the only value for exact match style
func (p *Parser) parseExactValue() ([]string, error) {
	tok, _ := p.lookahead(Values)
	if tok == EndOfStringToken || tok == CommaToken {
		return []string{""}, nil
	}
	tok, lit := p.consume(Values)
	if tok == IdentifierToken {
		return []string{lit}, nil
	}
	return nil, fmt.Errorf("found '%s', expected: identifier", lit)
}

// Parse takes a string representing a selector and returns a selector object, or an error.
// The input will cause an error if it does not follow this form:
//
//	<selector-syntax>           ::= <requirement> | <requirement> "," <selector-syntax>
//	<requirement>               ::= [!] KEY [ <set-based-restriction> | <exact-match-restriction> | <partial-match-restriction> ]
//	<set-based-restriction>     ::= "" | <inclusion-exclusion> <value-set>
//	<inclusion-exclusion>       ::= <inclusion> | <exclusion>
//	<exclusion>                 ::= "notin"
//	<inclusion>                 ::= "in"
//	<value-set>                 ::= "(" <values> ")"
//	<values>                    ::= VALUE | VALUE "," <values>
//	<exact-match-restriction>   ::= ["="|"=="|"!="] VALUE
//	<partial-match-restriction> ::= ["contains"|"notcontains"] VALUE
//
// Delimiter is white space: (' ', '\t')
// Example of valid syntax:
//
//	"x in (foo,,baz),y,z notin ()"
//
// Note:
//  1. Inclusion - " in " - denotes that the KEY exists and is equal to any of the
//     VALUEs in its requirement
//  2. Exclusion - " notin " - denotes that the KEY is not equal to any
//     of the VALUEs in its requirement or does not exist
//  3. The empty string is a valid VALUE
//  4. A requirement with just a KEY - as in "y" above - denotes that
//     the KEY exists and can be any VALUE.
//  5. A requirement with just !KEY requires that the KEY not exist.
func Parse(selector string, opts ...field.PathOption) (Selector, error) {
	parsedSelector, err := parse(&lexer{s: selector, pos: 0}, field.ToPath(opts...))
	if err == nil {
		return parsedSelector, nil
	}
	return nil, err
}

// ParseWithLexer takes a selector string and a custom lexer to tokenize the input,
// and returns a selector object or an error.
// This function is similar to Parse but allows for a custom lexer implementation.
func ParseWithLexer(selector string, l Lexer, opts ...field.PathOption) (Selector, error) {
	parsedSelector, err := parse(l, field.ToPath(opts...))
	if err == nil {
		return parsedSelector, nil
	}
	return nil, err
}

// parse parses the string representation of the selector and returns the internalSelector struct.
// The callers of this method can then decide how to return the internalSelector struct to their
// callers. This function has two callers now, one returns a Selector interface and the other
// returns a list of requirements.
func parse(l Lexer, path *field.Path) (internalSelector, error) {
	p := &Parser{l: l, path: path}
	items, err := p.parse()
	if err != nil {
		return nil, err
	}
	sort.Sort(ByKey(items)) // sort to grant determistic parsing
	return items, err
}

// SelectorFromSet returns a Selector which will match exactly the given Set. A
// nil and empty Sets are considered equivalent to Everything().
// It does not perform any validation, which means the server will reject
// the request if the Set contains invalid values.
func SelectorFromSet(ls k8sLabels.Set) Selector {
	return SelectorFromValidatedSet(ls)
}

// ValidatedSelectorFromSet returns a Selector which will match exactly the given Set. A
// nil and empty Sets are considered equivalent to Everything().
// The Set is validated client-side, which allows to catch errors early.
func ValidatedSelectorFromSet(ls k8sLabels.Set) (Selector, error) {
	if len(ls) == 0 {
		return internalSelector{}, nil
	}
	requirements := make([]Requirement, 0, len(ls))
	for label, value := range ls {
		r, err := NewRequirement(tuple(label), selection.Equals, []Tuple{tuple(value)})
		if err != nil {
			return nil, err
		}
		requirements = append(requirements, *r)
	}
	// sort to have deterministic string representation
	sort.Sort(ByKey(requirements))
	return internalSelector(requirements), nil
}

// SelectorFromValidatedSet returns a Selector which will match exactly the given Set.
// A nil and empty Sets are considered equivalent to Everything().
// It assumes that Set is already validated and doesn't do any validation.
// Note: this method copies the Set; if the Set is immutable, consider wrapping it with ValidatedSetSelector
// instead, which does not copy.
func SelectorFromValidatedSet(ls k8sLabels.Set) Selector {
	if len(ls) == 0 {
		return internalSelector{}
	}
	requirements := make([]Requirement, 0, len(ls))
	for label, value := range ls {
		requirements = append(requirements, Requirement{key: tuple(label),
			operator: selection.Equals, strValues: []Tuple{tuple(value)}})
	}
	// sort to have deterministic string representation
	sort.Sort(ByKey(requirements))
	return internalSelector(requirements)
}

// ParseToRequirements takes a string representing a selector and returns a list of
// requirements. This function is suitable for those callers that perform additional
// processing on selector requirements.
// See the documentation for Parse() function for more details.
// TODO: Consider exporting the internalSelector type instead.
func ParseToRequirements(selector string, opts ...field.PathOption) ([]Requirement, error) {
	return parse(&lexer{s: selector, pos: 0}, field.ToPath(opts...))
}

// ValidatedSetSelector wraps a Set, allowing it to implement the Selector interface. Unlike
// Set.AsSelectorPreValidated (which copies the input Set), this type simply wraps the underlying
// Set. As a result, it is substantially more efficient. A nil and empty Sets are considered
// equivalent to Everything().
//
// Callers MUST ensure the underlying Set is not mutated, and that it is already validated. If these
// constraints are not met, Set.AsValidatedSelector should be preferred
//
// None of the Selector methods mutate the underlying Set, but Add() and Requirements() convert to
// the less optimized version.
type ValidatedSetSelector k8sLabels.Set

func (s ValidatedSetSelector) Matches(labels k8sLabels.Labels) bool {
	for k, v := range s {
		if !labels.Has(k) || v != labels.Get(k) {
			return false
		}
	}
	return true
}

func (s ValidatedSetSelector) Empty() bool {
	return len(s) == 0
}

func (s ValidatedSetSelector) String() string {
	keys := make([]string, 0, len(s))
	for k := range s {
		keys = append(keys, k)
	}
	// Ensure deterministic output
	sort.Strings(keys)
	b := strings.Builder{}
	for i, key := range keys {
		v := s[key]
		b.Grow(len(key) + 2 + len(v))
		if i != 0 {
			b.WriteString(",")
		}
		b.WriteString(key)
		b.WriteString("=")
		b.WriteString(v)
	}
	return b.String()
}

func (s ValidatedSetSelector) Add(r ...Requirement) Selector {
	return s.toFullSelector().Add(r...)
}

func (s ValidatedSetSelector) Requirements() (requirements Requirements, selectable bool) {
	return s.toFullSelector().Requirements()
}

func (s ValidatedSetSelector) DeepCopySelector() Selector {
	res := make(ValidatedSetSelector, len(s))
	for k, v := range s {
		res[k] = v
	}
	return res
}

func (s ValidatedSetSelector) RequiresExactMatch(label string) (value string, found bool) {
	v, f := s[label]
	return v, f
}

func (s ValidatedSetSelector) toFullSelector() Selector {
	return SelectorFromValidatedSet(k8sLabels.Set(s))
}

var _ Selector = ValidatedSetSelector{}
