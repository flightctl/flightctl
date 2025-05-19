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

//nolint:staticcheck
package selector

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/flightctl/flightctl/pkg/k8s/selector/selection"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	k8sLabels "k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

var (
	ignoreDetail = cmpopts.IgnoreFields(field.Error{}, "Detail")
)

func TestSelectorParse(t *testing.T) {
	testGoodStrings := []string{
		"x=a,y=b,z=c",
		"",
		"x!=a,y=b",
		"x=",
		"x= ",
		"x=,z= ",
		"x= ,z= ",
		"!x",
		"x>1",
		"x>1,z<5",
		"x>=1",
		"x>=1,z<=5",
		"x>=1",
		"x>=1,z<=5",
		"x>=2024-10-24T10:00:00Z",
		"(x,z)<(1,5)",
		"(x,y)=(a,b)",
		"(x,y)=(a,)",
		"(x,y)=(,)",
	}
	testBadStrings := []string{
		"x=a||y=b",
		"x==a==b",
		"!x=a",
		"x<a",
		"x<2024-10-24T10:00:00",
		"(x,y=(a,)",
		"(x,y)=()",
		"(x,y)=a",
		"(x,y)=",
		"(x)=(a",
	}
	for _, test := range testGoodStrings {
		lq, err := Parse(test)
		if err != nil {
			t.Errorf("%v: error %v (%#v)\n", test, err, err)
			continue
		}
		if strings.Replace(test, " ", "", -1) != lq.String() {
			t.Errorf("%v restring gave: %v\n", test, lq.String())
		}
	}
	for _, test := range testBadStrings {
		_, err := Parse(test)
		if err == nil {
			t.Errorf("%v: did not get expected error\n", test)
		}
	}
}

func TestDeterministicParse(t *testing.T) {
	s1, err := Parse("x=a,a=x")
	s2, err2 := Parse("a=x,x=a")
	if err != nil || err2 != nil {
		t.Errorf("Unexpected parse error")
	}
	if s1.String() != s2.String() {
		t.Errorf("Non-deterministic parse")
	}
}

func expectMatch(t *testing.T, selector string, ls k8sLabels.Set) {
	lq, err := Parse(selector)
	if err != nil {
		t.Errorf("Unable to parse %v as a selector\n", selector)
		return
	}
	if !lq.Matches(ls) {
		t.Errorf("Wanted %s to match '%s', but it did not.\n", selector, ls)
	}
}

func expectNoMatch(t *testing.T, selector string, ls k8sLabels.Set) {
	lq, err := Parse(selector)
	if err != nil {
		t.Errorf("Unable to parse %v as a selector\n", selector)
		return
	}
	if lq.Matches(ls) {
		t.Errorf("Wanted '%s' to not match '%s', but it did.", selector, ls)
	}
}

func TestEverything(t *testing.T) {
	if !Everything().Matches(k8sLabels.Set{"x": "y"}) {
		t.Errorf("Nil selector didn't match")
	}
	if !Everything().Empty() {
		t.Errorf("Everything was not empty")
	}
}

func TestSelectorMatches(t *testing.T) {
	expectMatch(t, "", k8sLabels.Set{"x": "y"})
	expectMatch(t, "x=y", k8sLabels.Set{"x": "y"})
	expectMatch(t, "x=y,z=w", k8sLabels.Set{"x": "y", "z": "w"})
	expectMatch(t, "x!=y,z!=w", k8sLabels.Set{"x": "z", "z": "a"})
	expectMatch(t, "notin=in", k8sLabels.Set{"notin": "in"}) // in and notin in exactMatch
	expectMatch(t, "x", k8sLabels.Set{"x": "z"})
	expectMatch(t, "!x", k8sLabels.Set{"y": "z"})
	expectMatch(t, "x>1", k8sLabels.Set{"x": "2"})
	expectMatch(t, "x>=1", k8sLabels.Set{"x": "2"})
	expectMatch(t, "x<1", k8sLabels.Set{"x": "0"})
	expectMatch(t, "x<=1", k8sLabels.Set{"x": "0"})
	expectMatch(t, "x contains y", k8sLabels.Set{"x": "yw"})
	expectMatch(t, "x contains w", k8sLabels.Set{"x": "yw"})
	expectMatch(t, "x contains yw", k8sLabels.Set{"x": "yw"})
	expectMatch(t, "x contains yw", k8sLabels.Set{"x": "aywb"})
	expectMatch(t, "x contains yw", k8sLabels.Set{"x": "ayw"})
	expectMatch(t, "x notcontains yw", k8sLabels.Set{"x": "ay"})
	expectNoMatch(t, "x=z", k8sLabels.Set{})
	expectNoMatch(t, "x=y", k8sLabels.Set{"x": "z"})
	expectNoMatch(t, "x=y,z=w", k8sLabels.Set{"x": "w", "z": "w"})
	expectNoMatch(t, "x!=y,z!=w", k8sLabels.Set{"x": "z", "z": "w"})
	expectNoMatch(t, "x", k8sLabels.Set{"y": "z"})
	expectNoMatch(t, "!x", k8sLabels.Set{"x": "z"})
	expectNoMatch(t, "x>1", k8sLabels.Set{"x": "0"})
	expectNoMatch(t, "x>=1", k8sLabels.Set{"x": "0"})
	expectNoMatch(t, "x<1", k8sLabels.Set{"x": "2"})
	expectNoMatch(t, "x<=1", k8sLabels.Set{"x": "2"})
	expectNoMatch(t, "x notcontains y", k8sLabels.Set{"x": "yw"})
	expectNoMatch(t, "x notcontains w", k8sLabels.Set{"x": "yw"})
	expectNoMatch(t, "x notcontains yw", k8sLabels.Set{"x": "yw"})
	expectNoMatch(t, "x notcontains yw", k8sLabels.Set{"x": "aywb"})
	expectNoMatch(t, "x notcontains yw", k8sLabels.Set{"x": "ayw"})
	expectNoMatch(t, "x contains yw", k8sLabels.Set{"x": "ay"})

	labelset := k8sLabels.Set{
		"foo": "bar",
		"baz": "blah",
	}
	expectMatch(t, "foo=bar", labelset)
	expectMatch(t, "baz=blah", labelset)
	expectMatch(t, "foo=bar,baz=blah", labelset)
	expectNoMatch(t, "foo=blah", labelset)
	expectNoMatch(t, "baz=bar", labelset)
	expectNoMatch(t, "foo=bar,foobar=bar,baz=blah", labelset)

	// keyset
	expectMatch(t, "(x,y) in ((z,w),(z1,w1))", k8sLabels.Set{"x": "z", "y": "w"})
	expectMatch(t, "(x,y)", k8sLabels.Set{"x": "z", "y": "w"})
	expectMatch(t, "!(x,y)", k8sLabels.Set{"x": "z", "yy": "w"})
	expectNoMatch(t, "!x,!y", k8sLabels.Set{"x": "z", "yy": "w"})
	expectMatch(t, "(x,y)=(z,w)", k8sLabels.Set{"x": "z", "y": "w"})
	expectMatch(t, "(x)=(z)", k8sLabels.Set{"x": "z", "y": "w"})
	expectMatch(t, "(x,y)!=(z,w)", k8sLabels.Set{"x": "z", "y": "ww"})
	expectMatch(t, "(x,a)!=(z,w)", k8sLabels.Set{"x": "z", "y": "w"})
	expectNoMatch(t, "x!=z,y!=w", k8sLabels.Set{"x": "z", "y": "ww"})
	expectMatch(t, "(x,y) in ((z,w))", k8sLabels.Set{"x": "z", "y": "w"})
	expectMatch(t, "(x,y) in ((z,w),(z1,w1))", k8sLabels.Set{"x": "z", "y": "w"})
	expectMatch(t, "(x,y) notin ((z,w))", k8sLabels.Set{"x": "z", "y": "ww"})
	expectMatch(t, "(x,y) notin ((z,w),(z1,w1))", k8sLabels.Set{"x": "z", "y": "w1"})
	expectMatch(t, "(x,y) contains (z,w)", k8sLabels.Set{"x": "azb", "y": "awb"})
	expectMatch(t, "(x,y) notcontains (z,w)", k8sLabels.Set{"x": "azb", "y": "aab"})
	expectNoMatch(t, "x notcontains z, y notcontains w", k8sLabels.Set{"x": "azb", "y": "aab"})
	expectMatch(t, "(x,y) < (3,2)", k8sLabels.Set{"x": "3", "y": "1"})
	expectNoMatch(t, "x < 3, y < 2", k8sLabels.Set{"x": "3", "y": "1"})
}

func expectMatchDirect(t *testing.T, selector, ls k8sLabels.Set) {
	if !SelectorFromSet(selector).Matches(ls) {
		t.Errorf("Wanted %s to match '%s', but it did not.\n", selector, ls)
	}
}

//nolint:staticcheck,unused //iccheck // U1000 currently commented out in TODO of TestSetMatches
func expectNoMatchDirect(t *testing.T, selector, ls k8sLabels.Set) {
	if SelectorFromSet(selector).Matches(ls) {
		t.Errorf("Wanted '%s' to not match '%s', but it did.", selector, ls)
	}
}

func TestSetMatches(t *testing.T) {
	labelset := k8sLabels.Set{
		"foo": "bar",
		"baz": "blah",
	}
	expectMatchDirect(t, k8sLabels.Set{}, labelset)
	expectMatchDirect(t, k8sLabels.Set{"foo": "bar"}, labelset)
	expectMatchDirect(t, k8sLabels.Set{"baz": "blah"}, labelset)
	expectMatchDirect(t, k8sLabels.Set{"foo": "bar", "baz": "blah"}, labelset)

	//TODO: bad values not handled for the moment in SelectorFromSet
	//expectNoMatchDirect(t, k8sLabels.Set{"foo": "=blah"}, labelset)
	//expectNoMatchDirect(t, k8sLabels.Set{"baz": "=bar"}, labelset)
	//expectNoMatchDirect(t, k8sLabels.Set{"foo": "=bar", "foobar": "bar", "baz": "blah"}, labelset)
}

func TestNilMapIsValid(t *testing.T) {
	selector := k8sLabels.Set(nil).AsSelector()
	if selector == nil {
		t.Errorf("Selector for nil set should be Everything")
	}
	if !selector.Empty() {
		t.Errorf("Selector for nil set should be Empty")
	}
}

func TestSetIsEmpty(t *testing.T) {
	if !(k8sLabels.Set{}).AsSelector().Empty() {
		t.Errorf("Empty set should be empty")
	}
	if !(NewSelector()).Empty() {
		t.Errorf("Nil Selector should be empty")
	}
}

func TestLexer(t *testing.T) {
	testcases := []struct {
		s string
		t Token
	}{
		{"", EndOfStringToken},
		{",", CommaToken},
		{"notin", NotInToken},
		{"in", InToken},
		{"=", EqualsToken},
		{"contains", ContainsToken},
		{"notcontains", NotContainsToken},
		{"==", DoubleEqualsToken},
		{">", GreaterThanToken},
		{"<", LessThanToken},
		//Note that Lex returns the longest valid token found
		{"!", DoesNotExistToken},
		{"!=", NotEqualsToken},
		{"(", OpenParToken},
		{")", ClosedParToken},
		//Non-"special" characters are considered part of an identifier
		{"~", IdentifierToken},
		{"||", IdentifierToken},
	}
	for _, v := range testcases {
		l := &lexer{s: v.s, pos: 0}
		token, lit := l.Lex()
		if token != v.t {
			t.Errorf("Got %d it should be %d for '%s'", token, v.t, v.s)
		}
		if v.t != ErrorToken && lit != v.s {
			t.Errorf("Got '%s' it should be '%s'", lit, v.s)
		}
	}
}

func min(l, r int) (m int) {
	m = r
	if l < r {
		m = l
	}
	return m
}

func TestLexerSequence(t *testing.T) {
	testcases := []struct {
		s string
		t []Token
	}{
		{"key in ( value )", []Token{IdentifierToken, InToken, OpenParToken, IdentifierToken, ClosedParToken}},
		{"key notin ( value )", []Token{IdentifierToken, NotInToken, OpenParToken, IdentifierToken, ClosedParToken}},
		{"key in ( value1, value2 )", []Token{IdentifierToken, InToken, OpenParToken, IdentifierToken, CommaToken, IdentifierToken, ClosedParToken}},
		{"key", []Token{IdentifierToken}},
		{"!key", []Token{DoesNotExistToken, IdentifierToken}},
		{"()", []Token{OpenParToken, ClosedParToken}},
		{"x in (),y", []Token{IdentifierToken, InToken, OpenParToken, ClosedParToken, CommaToken, IdentifierToken}},
		{"== != (), = notin", []Token{DoubleEqualsToken, NotEqualsToken, OpenParToken, ClosedParToken, CommaToken, EqualsToken, NotInToken}},
		{"key>2", []Token{IdentifierToken, GreaterThanToken, IdentifierToken}},
		{"key<1", []Token{IdentifierToken, LessThanToken, IdentifierToken}},
		{"key contains a, key notcontains b", []Token{IdentifierToken, ContainsToken, IdentifierToken, CommaToken, IdentifierToken, NotContainsToken, IdentifierToken}},
	}
	for _, v := range testcases {
		var tokens []Token
		l := &lexer{s: v.s, pos: 0}
		for {
			token, _ := l.Lex()
			if token == EndOfStringToken {
				break
			}
			tokens = append(tokens, token)
		}
		if len(tokens) != len(v.t) {
			t.Errorf("Bad number of tokens for '%s %d, %d", v.s, len(tokens), len(v.t))
		}
		for i := 0; i < min(len(tokens), len(v.t)); i++ {
			if tokens[i] != v.t[i] {
				t.Errorf("Test '%s': Mismatching in token type found '%v' it should be '%v'", v.s, tokens[i], v.t[i])
			}
		}
	}
}
func TestParserLookahead(t *testing.T) {
	testcases := []struct {
		s string
		t []Token
	}{
		{"key in ( value )", []Token{IdentifierToken, InToken, OpenParToken, IdentifierToken, ClosedParToken, EndOfStringToken}},
		{"key notin ( value )", []Token{IdentifierToken, NotInToken, OpenParToken, IdentifierToken, ClosedParToken, EndOfStringToken}},
		{"key in ( value1, value2 )", []Token{IdentifierToken, InToken, OpenParToken, IdentifierToken, CommaToken, IdentifierToken, ClosedParToken, EndOfStringToken}},
		{"key", []Token{IdentifierToken, EndOfStringToken}},
		{"!key", []Token{DoesNotExistToken, IdentifierToken, EndOfStringToken}},
		{"()", []Token{OpenParToken, ClosedParToken, EndOfStringToken}},
		{"", []Token{EndOfStringToken}},
		{"x in (),y", []Token{IdentifierToken, InToken, OpenParToken, ClosedParToken, CommaToken, IdentifierToken, EndOfStringToken}},
		{"== != (), = notin", []Token{DoubleEqualsToken, NotEqualsToken, OpenParToken, ClosedParToken, CommaToken, EqualsToken, NotInToken, EndOfStringToken}},
		{"key>2", []Token{IdentifierToken, GreaterThanToken, IdentifierToken, EndOfStringToken}},
		{"key<1", []Token{IdentifierToken, LessThanToken, IdentifierToken, EndOfStringToken}},
		{"key contains a, key notcontains b", []Token{IdentifierToken, ContainsToken, IdentifierToken, CommaToken, IdentifierToken, NotContainsToken, IdentifierToken, EndOfStringToken}},
	}
	for _, v := range testcases {
		p := &Parser{l: &lexer{s: v.s, pos: 0}, position: 0}
		p.scan()
		if len(p.scannedItems) != len(v.t) {
			t.Errorf("Expected %d items found %d", len(v.t), len(p.scannedItems))
		}
		for {
			token, lit := p.lookahead(KeyAndOperator)

			token2, lit2 := p.consume(KeyAndOperator)
			if token == EndOfStringToken {
				break
			}
			if token != token2 || lit != lit2 {
				t.Errorf("Bad values")
			}
		}
	}
}

func TestParseOperator(t *testing.T) {
	testcases := []struct {
		token         string
		expectedError error
	}{
		{"in", nil},
		{"=", nil},
		{"==", nil},
		{">", nil},
		{"<", nil},
		{"notin", nil},
		{"!=", nil},
		{"contains", nil},
		{"notcontains", nil},
		{"!", fmt.Errorf("found '%s', expected: %v", selection.DoesNotExist, strings.Join(binaryOperators, ", "))},
		{"exists", fmt.Errorf("found '%s', expected: %v", selection.Exists, strings.Join(binaryOperators, ", "))},
		{"(", fmt.Errorf("found '%s', expected: %v", "(", strings.Join(binaryOperators, ", "))},
	}
	for _, testcase := range testcases {
		p := &Parser{l: &lexer{s: testcase.token, pos: 0}, position: 0}
		p.scan()

		_, err := p.parseOperator()
		if ok := reflect.DeepEqual(testcase.expectedError, err); !ok {
			t.Errorf("\nexpect err [%v], \nactual err [%v]", testcase.expectedError, err)
		}
	}
}

func TestRequirementConstructor(t *testing.T) {
	requirementConstructorTests := []struct {
		Key     string
		Op      selection.Operator
		Vals    sets.String
		WantErr field.ErrorList
	}{
		{
			Key: "x1",
			Op:  selection.In,
			WantErr: field.ErrorList{
				&field.Error{
					Type:     field.ErrorTypeInvalid,
					Field:    "values",
					BadValue: makeTuples(),
				},
			},
		},
		{
			Key:  "x2",
			Op:   selection.NotIn,
			Vals: sets.NewString(),
			WantErr: field.ErrorList{
				&field.Error{
					Type:     field.ErrorTypeInvalid,
					Field:    "values",
					BadValue: makeTuples(),
				},
			},
		},
		{
			Key:  "x3",
			Op:   selection.In,
			Vals: sets.NewString("foo"),
		},
		{
			Key:  "x4",
			Op:   selection.NotIn,
			Vals: sets.NewString("foo"),
		},
		{
			Key:  "x5",
			Op:   selection.Equals,
			Vals: sets.NewString("foo", "bar"),
			WantErr: field.ErrorList{
				&field.Error{
					Type:     field.ErrorTypeInvalid,
					Field:    "values",
					BadValue: makeTuples("bar", "foo"),
				},
			},
		},
		{
			Key: "x6",
			Op:  selection.Exists,
		},
		{
			Key: "x7",
			Op:  selection.DoesNotExist,
		},
		{
			Key:  "x8",
			Op:   selection.Exists,
			Vals: sets.NewString("foo"),
			WantErr: field.ErrorList{
				&field.Error{
					Type:     field.ErrorTypeInvalid,
					Field:    "values",
					BadValue: makeTuples("foo"),
				},
			},
		},
		{
			Key:  "x9",
			Op:   selection.In,
			Vals: sets.NewString("bar"),
		},
		{
			Key:  "x10",
			Op:   selection.In,
			Vals: sets.NewString("bar"),
		},
		{
			Key:  "x11",
			Op:   selection.GreaterThan,
			Vals: sets.NewString("1"),
		},
		{
			Key:  "x12",
			Op:   selection.LessThan,
			Vals: sets.NewString("6"),
		},
		{
			Key: "x13",
			Op:  selection.GreaterThan,
			WantErr: field.ErrorList{
				&field.Error{
					Type:     field.ErrorTypeInvalid,
					Field:    "values",
					BadValue: makeTuples(),
				},
			},
		},
		{
			Key:  "x14",
			Op:   selection.GreaterThan,
			Vals: sets.NewString("bar"),
			WantErr: field.ErrorList{
				&field.Error{
					Type:     field.ErrorTypeInvalid,
					Field:    "values[0]",
					BadValue: "bar",
				},
			},
		},
		{
			Key:  "x15",
			Op:   selection.LessThan,
			Vals: sets.NewString("bar"),
			WantErr: field.ErrorList{
				&field.Error{
					Type:     field.ErrorTypeInvalid,
					Field:    "values[0]",
					BadValue: "bar",
				},
			},
		},
		/* We do support
		{
			Key: strings.Repeat("a", 254), //breaks DNS rule that len(key) <= 253
			Op:  selection.Exists,
			WantErr: field.ErrorList{
				&field.Error{
					Type:     field.ErrorTypeInvalid,
					Field:    "key",
					BadValue: strings.Repeat("a", 254),
				},
			},
		},
		{
			Key:  "x16",
			Op:   selection.Equals,
			Vals: sets.NewString(strings.Repeat("a", 254)),
			WantErr: field.ErrorList{
				&field.Error{
					Type:     field.ErrorTypeInvalid,
					Field:    "values[0][x16]",
					BadValue: strings.Repeat("a", 254),
				},
			},
		},
		{
			Key:  "x17",
			Op:   selection.Equals,
			Vals: sets.NewString("a b"),
			WantErr: field.ErrorList{
				&field.Error{
					Type:     field.ErrorTypeInvalid,
					Field:    "values[0][x17]",
					BadValue: "a b",
				},
			},
		},
		*/
		{
			Key: "x18",
			Op:  "unsupportedOp",
			WantErr: field.ErrorList{
				&field.Error{
					Type:     field.ErrorTypeNotSupported,
					Field:    "operator",
					BadValue: selection.Operator("unsupportedOp"),
				},
			},
		},
		{
			Key:  "x19",
			Op:   selection.Contains,
			Vals: sets.NewString("foo", "bar"),
			WantErr: field.ErrorList{
				&field.Error{
					Type:     field.ErrorTypeInvalid,
					Field:    "values",
					BadValue: makeTuples("bar", "foo"),
				},
			},
		},
		{
			Key:  "x19",
			Op:   selection.NotContains,
			Vals: sets.NewString("foo", "bar"),
			WantErr: field.ErrorList{
				&field.Error{
					Type:     field.ErrorTypeInvalid,
					Field:    "values",
					BadValue: makeTuples("bar", "foo"),
				},
			},
		},
	}
	for _, rc := range requirementConstructorTests {
		values := make([]Tuple, 0, rc.Vals.Len())
		for _, v := range rc.Vals.List() {
			values = append(values, tuple(v))
		}
		_, err := NewRequirement(tuple(rc.Key), rc.Op, values)
		if diff := cmp.Diff(rc.WantErr.ToAggregate(), err, ignoreDetail); diff != "" {
			t.Errorf("NewRequirement test %v returned unexpected error (-want,+got):\n%s", rc.Key, diff)
		}
	}
}

func TestToString(t *testing.T) {
	var req Requirement
	toStringTests := []struct {
		In    *internalSelector
		Out   string
		Valid bool
	}{

		{&internalSelector{
			getRequirement(tuple("x"), selection.In, makeTuples("abc", "def"), t),
			getRequirement(tuple("y"), selection.NotIn, makeTuples("jkl"), t),
			getRequirement(tuple("z"), selection.Exists, nil, t)},
			"x in (abc,def),y notin (jkl),z", true},
		{&internalSelector{
			getRequirement(tuple("x"), selection.NotIn, makeTuples("abc", "def"), t),
			getRequirement(tuple("y"), selection.NotEquals, makeTuples("jkl"), t),
			getRequirement(tuple("z"), selection.DoesNotExist, nil, t)},
			"x notin (abc,def),y!=jkl,!z", true},
		{&internalSelector{
			getRequirement(tuple("x"), selection.In, makeTuples("abc", "def"), t),
			req}, // adding empty req for the trailing ','
			"x in (abc,def),", false},
		{&internalSelector{
			getRequirement(tuple("x"), selection.NotIn, makeTuples("abc"), t),
			getRequirement(tuple("y"), selection.In, makeTuples("jkl", "mno"), t),
			getRequirement(tuple("z"), selection.NotIn, makeTuples(""), t)},
			"x notin (abc),y in (jkl,mno),z notin ()", true},
		{&internalSelector{
			getRequirement(tuple("x"), selection.Equals, makeTuples("abc"), t),
			getRequirement(tuple("y"), selection.DoubleEquals, makeTuples("jkl"), t),
			getRequirement(tuple("z"), selection.NotEquals, makeTuples("a"), t),
			getRequirement(tuple("z"), selection.Exists, nil, t)},
			"x=abc,y==jkl,z!=a,z", true},
		{&internalSelector{
			getRequirement(tuple("x"), selection.GreaterThan, makeTuples("2"), t),
			getRequirement(tuple("y"), selection.LessThan, makeTuples("8"), t),
			getRequirement(tuple("z"), selection.Exists, nil, t)},
			"x>2,y<8,z", true},
		{&internalSelector{
			getRequirement(tuple("x"), selection.Contains, makeTuples("2"), t),
			getRequirement(tuple("y"), selection.NotContains, makeTuples("8"), t),
			getRequirement(tuple("z"), selection.Exists, nil, t)},
			"x contains 2,y notcontains 8,z", true},
		{&internalSelector{
			getRequirement(tuple("x"), selection.Equals, makeTuples("2"), t),
			getRequirement(tuple("y"), selection.NotContains, makeTuples("8"), t),
			getRequirement(tuple("z", "w"), selection.Exists, nil, t)},
			"x=2,y notcontains 8,(z,w)", true},
		{&internalSelector{
			getRequirement(tuple("x", "y"), selection.Equals, []Tuple{tuple("2", "8")}, t),
			getRequirement(tuple("z", "w"), selection.DoesNotExist, nil, t)},
			"(x,y)=(2,8),!(z,w)", true},
		{&internalSelector{
			getRequirement(tuple("x", "y"), selection.In, []Tuple{tuple("2", "8"), tuple("3", "9")}, t)},
			"(x,y) in ((2,8),(3,9))", true},
	}
	for _, ts := range toStringTests {
		if out := ts.In.String(); out == "" && ts.Valid {
			t.Errorf("%#v.String() => '%v' expected no error", ts.In, out)
		} else if out != ts.Out {
			t.Errorf("%#v.String() => '%v' want '%v'", ts.In, out, ts.Out)
		}
	}
}

func TestRequirementSelectorMatching(t *testing.T) {
	var req Requirement
	labelSelectorMatchingTests := []struct {
		Set   k8sLabels.Set
		Sel   Selector
		Match bool
	}{
		{k8sLabels.Set{"x": "foo", "y": "baz"}, &internalSelector{
			req,
		}, false},
		{k8sLabels.Set{"x": "foo", "y": "baz"}, &internalSelector{
			getRequirement(tuple("x"), selection.In, makeTuples("foo"), t),
			getRequirement(tuple("y"), selection.NotIn, makeTuples("alpha"), t),
		}, true},
		{k8sLabels.Set{"x": "foo", "y": "baz"}, &internalSelector{
			getRequirement(tuple("x"), selection.In, makeTuples("foo"), t),
			getRequirement(tuple("y"), selection.In, makeTuples("alpha"), t),
		}, false},
		{k8sLabels.Set{"y": ""}, &internalSelector{
			getRequirement(tuple("x"), selection.NotIn, makeTuples(""), t),
			getRequirement(tuple("y"), selection.Exists, nil, t),
		}, true},
		{k8sLabels.Set{"y": ""}, &internalSelector{
			getRequirement(tuple("x"), selection.DoesNotExist, nil, t),
			getRequirement(tuple("y"), selection.Exists, nil, t),
		}, true},
		{k8sLabels.Set{"y": ""}, &internalSelector{
			getRequirement(tuple("x"), selection.NotIn, makeTuples(""), t),
			getRequirement(tuple("y"), selection.DoesNotExist, nil, t),
		}, false},
		{k8sLabels.Set{"y": "baz"}, &internalSelector{
			getRequirement(tuple("x"), selection.In, makeTuples(""), t),
		}, false},
		{k8sLabels.Set{"z": "2"}, &internalSelector{
			getRequirement(tuple("z"), selection.GreaterThan, makeTuples("1"), t),
		}, true},
		{k8sLabels.Set{"z": "v2"}, &internalSelector{
			getRequirement(tuple("z"), selection.GreaterThan, makeTuples("1"), t),
		}, false},
		{k8sLabels.Set{"x": "v1", "y": "v2"}, &internalSelector{
			getRequirement(tuple("x", "y"), selection.NotEquals, []Tuple{tuple("v1", "v3")}, t),
		}, true},
		{k8sLabels.Set{"x": "v1", "y": "v2"}, &internalSelector{
			getRequirement(tuple("x", "y"), selection.NotEquals, []Tuple{tuple("v1", "v2")}, t),
		}, false},
	}
	for _, lsm := range labelSelectorMatchingTests {
		if match := lsm.Sel.Matches(lsm.Set); match != lsm.Match {
			t.Errorf("%+v.Matches(%#v) => %v, want %v", lsm.Sel, lsm.Set, match, lsm.Match)
		}
	}
}

func TestSetSelectorParser(t *testing.T) {
	setSelectorParserTests := []struct {
		In    string
		Out   Selector
		Match bool
		Valid bool
	}{
		{"", NewSelector(), true, true},
		{"\rx", internalSelector{
			getRequirement(tuple("x"), selection.Exists, nil, t),
		}, true, true},
		{"this-is-a-dns.domain.com/key-with-dash", internalSelector{
			getRequirement(tuple("this-is-a-dns.domain.com/key-with-dash"), selection.Exists, nil, t),
		}, true, true},
		{"this-is-another-dns.domain.com/key-with-dash in (so,what)", internalSelector{
			getRequirement(tuple("this-is-another-dns.domain.com/key-with-dash"), selection.In, makeTuples("so", "what"), t),
		}, true, true},
		{"0.1.2.domain/99 notin (10.10.100.1, tick.tack.clock)", internalSelector{
			getRequirement(tuple("0.1.2.domain/99"), selection.NotIn, makeTuples("10.10.100.1", "tick.tack.clock"), t),
		}, true, true},
		{"foo  in	 (abc)", internalSelector{
			getRequirement(tuple("foo"), selection.In, makeTuples("abc"), t),
		}, true, true},
		{"x notin\n (abc)", internalSelector{
			getRequirement(tuple("x"), selection.NotIn, makeTuples("abc"), t),
		}, true, true},
		{"x  notin	\t	(abc,def)", internalSelector{
			getRequirement(tuple("x"), selection.NotIn, makeTuples("abc", "def"), t),
		}, true, true},
		{"x in (abc,def)", internalSelector{
			getRequirement(tuple("x"), selection.In, makeTuples("abc", "def"), t),
		}, true, true},
		{"x in (abc,)", internalSelector{
			getRequirement(tuple("x"), selection.In, makeTuples("abc", ""), t),
		}, true, true},
		{"x in ()", internalSelector{
			getRequirement(tuple("x"), selection.In, makeTuples(""), t),
		}, true, true},
		{"x notin (abc,,def),bar,z in (),w", internalSelector{
			getRequirement(tuple("bar"), selection.Exists, nil, t),
			getRequirement(tuple("w"), selection.Exists, nil, t),
			getRequirement(tuple("x"), selection.NotIn, makeTuples("abc", "", "def"), t),
			getRequirement(tuple("z"), selection.In, makeTuples(""), t),
		}, true, true},
		{"x,y in (a)", internalSelector{
			getRequirement(tuple("y"), selection.In, makeTuples("a"), t),
			getRequirement(tuple("x"), selection.Exists, nil, t),
		}, false, true},
		{"x=a", internalSelector{
			getRequirement(tuple("x"), selection.Equals, makeTuples("a"), t),
		}, true, true},
		{"x>1", internalSelector{
			getRequirement(tuple("x"), selection.GreaterThan, makeTuples("1"), t),
		}, true, true},
		{"x<7", internalSelector{
			getRequirement(tuple("x"), selection.LessThan, makeTuples("7"), t),
		}, true, true},
		{"x=a,y!=b", internalSelector{
			getRequirement(tuple("x"), selection.Equals, makeTuples("a"), t),
			getRequirement(tuple("y"), selection.NotEquals, makeTuples("b"), t),
		}, true, true},
		{"x=a,y!=b,z in (h,i,j)", internalSelector{
			getRequirement(tuple("x"), selection.Equals, makeTuples("a"), t),
			getRequirement(tuple("y"), selection.NotEquals, makeTuples("b"), t),
			getRequirement(tuple("z"), selection.In, makeTuples("h", "i", "j"), t),
		}, true, true},
		{"x=a||y=b", internalSelector{}, false, false},
		{"x,,y", nil, true, false},
		{",x,y", nil, true, false},
		{"x nott in (y)", nil, true, false},
		{"x notin ( )", internalSelector{
			getRequirement(tuple("x"), selection.NotIn, makeTuples(""), t),
		}, true, true},
		{"x notin (, a)", internalSelector{
			getRequirement(tuple("x"), selection.NotIn, makeTuples("", "a"), t),
		}, true, true},
		{"a in (xyz),", nil, true, false},
		{"a in (xyz)b notin ()", nil, true, false},
		{"a ", internalSelector{
			getRequirement(tuple("a"), selection.Exists, nil, t),
		}, true, true},
		{"a in (x,y,notin, z,in)", internalSelector{
			getRequirement(tuple("a"), selection.In, makeTuples("x", "y", "notin", "z", "in"), t),
		}, true, true}, // operator 'in' inside list of identifiers
		{"(x,y) != (a,b)", internalSelector{
			getRequirement(tuple("x", "y"), selection.NotEquals, []Tuple{tuple("a", "b")}, t),
		}, false, true},
		{"(x,y) == (a,b)", internalSelector{
			getRequirement(tuple("x", "y"), selection.DoubleEquals, []Tuple{tuple("a", "b")}, t),
		}, true, true},
		{"(x) == (a)", internalSelector{
			getRequirement(tuple("x"), selection.DoubleEquals, []Tuple{tuple("a")}, t),
		}, true, true},
		{"(x) == (a,b)", nil, false, false},
		{"(x,y) == (a)", nil, false, false},
		{"a in (xyz abc)", nil, false, false}, // no comma
		{"a notin(", nil, true, false},        // bad formed
		{"a (", nil, false, false},            // cpar
		{"(", nil, false, false},              // opar
	}

	for _, ssp := range setSelectorParserTests {
		if sel, err := Parse(ssp.In); err != nil && ssp.Valid {
			t.Errorf("Parse(%s) => %v expected no error", ssp.In, err)
		} else if err == nil && !ssp.Valid {
			t.Errorf("Parse(%s) => %+v expected error", ssp.In, sel)
		} else if ssp.Match && !reflect.DeepEqual(sel, ssp.Out) {
			t.Errorf("Parse(%s) => parse output '%#v' doesn't match '%#v' expected match", ssp.In, sel, ssp.Out)
		}
	}
}

func makeTuples(elements ...string) []Tuple {
	tuples := make([]Tuple, 0, len(elements))
	for _, e := range elements {
		tuples = append(tuples, tuple(e))
	}
	return tuples
}

func getRequirement(key Tuple, op selection.Operator, vals []Tuple, t *testing.T) Requirement {
	req, err := NewRequirement(key, op, vals)
	if err != nil {
		t.Errorf("NewRequirement(%v, %v, %v) resulted in error:%v", key, op, vals, err)
		return Requirement{}
	}
	return *req
}

func TestAdd(t *testing.T) {
	testCases := []struct {
		name        string
		sel         Selector
		key         string
		operator    selection.Operator
		values      []string
		refSelector Selector
	}{
		{
			"keyInOperator",
			internalSelector{},
			"key",
			selection.In,
			[]string{"value"},
			internalSelector{Requirement{tuple("key"), selection.In, makeTuples("value")}},
		},
		{
			"keyEqualsOperator",
			internalSelector{Requirement{tuple("key"), selection.In, makeTuples("value")}},
			"key2",
			selection.Equals,
			[]string{"value2"},
			internalSelector{
				Requirement{tuple("key"), selection.In, makeTuples("value")},
				Requirement{tuple("key2"), selection.Equals, makeTuples("value2")},
			},
		},
	}
	for _, ts := range testCases {
		vals := make([]Tuple, 0, len(ts.values))
		for _, v := range ts.values {
			vals = append(vals, tuple(v))
		}
		req, err := NewRequirement(tuple(ts.key), ts.operator, vals)
		if err != nil {
			t.Errorf("%s - Unable to create labels.Requirement", ts.name)
		}
		ts.sel = ts.sel.Add(*req)
		if !reflect.DeepEqual(ts.sel, ts.refSelector) {
			t.Errorf("%s - Expected %v found %v", ts.name, ts.refSelector, ts.sel)
		}
	}
}

func BenchmarkSelectorFromValidatedSet(b *testing.B) {
	set := map[string]string{
		"foo": "foo",
		"bar": "bar",
	}
	matchee := k8sLabels.Set(map[string]string{
		"foo":   "foo",
		"bar":   "bar",
		"extra": "label",
	})

	for i := 0; i < b.N; i++ {
		s := SelectorFromValidatedSet(set)
		if s.Empty() {
			b.Errorf("Unexpected selector")
		}
		if !s.Matches(matchee) {
			b.Errorf("Unexpected match")
		}
	}
}

func BenchmarkSetSelector(b *testing.B) {
	set := map[string]string{
		"foo": "foo",
		"bar": "bar",
	}
	matchee := k8sLabels.Set(map[string]string{
		"foo":   "foo",
		"bar":   "bar",
		"extra": "label",
	})

	for i := 0; i < b.N; i++ {
		s := ValidatedSetSelector(set)
		if s.Empty() {
			b.Errorf("Unexpected selector")
		}
		if !s.Matches(matchee) {
			b.Errorf("Unexpected match")
		}
	}
}

func TestSetSelectorString(t *testing.T) {
	cases := []struct {
		set k8sLabels.Set
		out string
	}{
		{
			k8sLabels.Set{},
			"",
		},
		{
			k8sLabels.Set{"app": "foo"},
			"app=foo",
		},
		{
			k8sLabels.Set{"app": "foo", "a": "b"},
			"a=b,app=foo",
		},
	}

	for _, tt := range cases {
		t.Run(tt.out, func(t *testing.T) {
			if got := ValidatedSetSelector(tt.set).String(); tt.out != got {
				t.Fatalf("expected %v, got %v", tt.out, got)
			}
		})
	}
}

func TestRequiresExactMatch(t *testing.T) {
	testCases := []struct {
		name          string
		sel           Selector
		label         string
		expectedFound bool
		expectedValue string
	}{
		{
			name:          "keyInOperatorExactMatch",
			sel:           internalSelector{Requirement{tuple("key"), selection.In, makeTuples("value")}},
			label:         "key",
			expectedFound: true,
			expectedValue: "value",
		},
		{
			name:          "keyInOperatorNotExactMatch",
			sel:           internalSelector{Requirement{tuple("key"), selection.In, makeTuples("value", "value2")}},
			label:         "key",
			expectedFound: false,
			expectedValue: "",
		},
		{
			name: "keyInOperatorNotExactMatch",
			sel: internalSelector{
				Requirement{tuple("key"), selection.In, makeTuples("value", "value1")},
				Requirement{tuple("key2"), selection.In, makeTuples("value2")},
			},
			label:         "key2",
			expectedFound: true,
			expectedValue: "value2",
		},
		{
			name:          "keyEqualOperatorExactMatch",
			sel:           internalSelector{Requirement{tuple("key"), selection.Equals, makeTuples("value")}},
			label:         "key",
			expectedFound: true,
			expectedValue: "value",
		},
		{
			name:          "keyDoubleEqualOperatorExactMatch",
			sel:           internalSelector{Requirement{tuple("key"), selection.DoubleEquals, makeTuples("value")}},
			label:         "key",
			expectedFound: true,
			expectedValue: "value",
		},
		{
			name:          "keyNotEqualOperatorExactMatch",
			sel:           internalSelector{Requirement{tuple("key"), selection.NotEquals, makeTuples("value")}},
			label:         "key",
			expectedFound: false,
			expectedValue: "",
		},
		{
			name: "keyEqualOperatorExactMatchFirst",
			sel: internalSelector{
				Requirement{tuple("key"), selection.In, makeTuples("value")},
				Requirement{tuple("key2"), selection.In, makeTuples("value2")},
			},
			label:         "key",
			expectedFound: true,
			expectedValue: "value",
		},
		{
			name: "keysetEqualOperatorExactMatch",
			sel: internalSelector{
				Requirement{tuple("k1", "k2"), selection.Equals, []Tuple{tuple("v1", "v2")}},
			},
			label:         "k1",
			expectedFound: true,
			expectedValue: "v1",
		},
	}
	for _, ts := range testCases {
		t.Run(ts.name, func(t *testing.T) {
			value, found := ts.sel.RequiresExactMatch(ts.label)
			if found != ts.expectedFound {
				t.Errorf("Expected match %v, found %v", ts.expectedFound, found)
			}
			if found && value != ts.expectedValue {
				t.Errorf("Expected value %v, found %v", ts.expectedValue, value)
			}

		})
	}
}

func TestValidatedSelectorFromSet(t *testing.T) {
	tests := []struct {
		name             string
		input            k8sLabels.Set
		expectedSelector internalSelector
		expectedError    field.ErrorList
	}{
		{
			name:  "Simple Set, no error",
			input: k8sLabels.Set{"key": "val"},
			expectedSelector: internalSelector{
				Requirement{
					key:       tuple("key"),
					operator:  selection.Equals,
					strValues: makeTuples("val"),
				},
			},
		},
		/* We do support
		{
			name:  "Invalid Set, value too long",
			input: k8sLabels.Set{"Key": "axahm2EJ8Phiephe2eixohbee9eGeiyees1thuozi1xoh0GiuH3diewi8iem7Nui"},
			expectedError: field.ErrorList{
				&field.Error{
					Type:     field.ErrorTypeInvalid,
					Field:    "values[0][Key]",
					BadValue: "axahm2EJ8Phiephe2eixohbee9eGeiyees1thuozi1xoh0GiuH3diewi8iem7Nui",
				},
			},
		},
		*/
	}

	for _, tc := range tests {
		selector, err := ValidatedSelectorFromSet(tc.input)
		if diff := cmp.Diff(tc.expectedError.ToAggregate(), err, ignoreDetail); diff != "" {
			t.Errorf("ValidatedSelectorFromSet %#v returned unexpected error (-want,+got):\n%s", tc.name, diff)
		}
		if err == nil {
			if diff := cmp.Diff(tc.expectedSelector, selector); diff != "" {
				t.Errorf("ValidatedSelectorFromSet %#v returned unexpected selector (-want,+got):\n%s", tc.name, diff)
			}
		}
	}
}

func BenchmarkRequirementString(b *testing.B) {
	r := Requirement{
		key:      tuple("environment"),
		operator: selection.NotIn,
		strValues: makeTuples(
			"dev",
		),
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if r.String() != "environment notin (dev)" {
			b.Errorf("Unexpected Requirement string")
		}
	}
}

func TestRequirementEqual(t *testing.T) {
	tests := []struct {
		name string
		x, y *Requirement
		want bool
	}{
		{
			name: "same requirements should be equal",
			x: &Requirement{
				key:       tuple("key"),
				operator:  selection.Equals,
				strValues: makeTuples("foo", "bar"),
			},
			y: &Requirement{
				key:       tuple("key"),
				operator:  selection.Equals,
				strValues: makeTuples("foo", "bar"),
			},
			want: true,
		},
		{
			name: "requirements with different keys should not be equal",
			x: &Requirement{
				key:       tuple("key1"),
				operator:  selection.Equals,
				strValues: makeTuples("foo", "bar"),
			},
			y: &Requirement{
				key:       tuple("key2"),
				operator:  selection.Equals,
				strValues: makeTuples("foo", "bar"),
			},
			want: false,
		},
		{
			name: "requirements with different operators should not be equal",
			x: &Requirement{
				key:       tuple("key"),
				operator:  selection.Equals,
				strValues: makeTuples("foo", "bar"),
			},
			y: &Requirement{
				key:       tuple("key"),
				operator:  selection.In,
				strValues: makeTuples("foo", "bar"),
			},
			want: false,
		},
		{
			name: "requirements with different values should not be equal",
			x: &Requirement{
				key:       tuple("key"),
				operator:  selection.Equals,
				strValues: makeTuples("foo", "bar"),
			},
			y: &Requirement{
				key:       tuple("key"),
				operator:  selection.Equals,
				strValues: makeTuples("foobar"),
			},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cmp.Equal(tt.x, tt.y); got != tt.want {
				t.Errorf("cmp.Equal() = %v, want %v", got, tt.want)
			}
		})
	}
}
