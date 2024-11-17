package fields

import (
	"testing"

	"github.com/flightctl/flightctl/pkg/k8s/selector"
)

func TestLexer(t *testing.T) {
	testcases := []struct {
		s string
		t selector.Token
	}{
		{"", selector.EndOfStringToken},
		{",", selector.CommaToken},
		{"notin", selector.NotInToken},
		{"in", selector.InToken},
		{"=", selector.EqualsToken},
		{"contains", selector.ContainsToken},
		{"notcontains", selector.NotContainsToken},
		{"==", selector.DoubleEqualsToken},
		{">", selector.GreaterThanToken},
		{"<", selector.LessThanToken},
		//Note that Lex returns the longest valid token found
		{"!", selector.DoesNotExistToken},
		{"!=", selector.NotEqualsToken},
		{"(", selector.OpenParToken},
		{")", selector.ClosedParToken},
		//Non-"special" characters are considered part of an identifier
		{"~", selector.IdentifierToken},
		{"||", selector.IdentifierToken},
	}
	for _, v := range testcases {
		l := &lexer{s: v.s, pos: 0}
		token, lit := l.Lex()
		if token != v.t {
			t.Errorf("Got %d it should be %d for '%s'", token, v.t, v.s)
		}
		if v.t != selector.ErrorToken && lit != v.s {
			t.Errorf("Got '%s' it should be '%s'", lit, v.s)
		}
	}
}

func TestLexerSequence(t *testing.T) {
	testcases := []struct {
		s string
		t []selector.Token
	}{
		{"key in ( value )", []selector.Token{selector.IdentifierToken, selector.InToken, selector.OpenParToken, selector.IdentifierToken, selector.ClosedParToken}},
		{"key notin ( value )", []selector.Token{selector.IdentifierToken, selector.NotInToken, selector.OpenParToken, selector.IdentifierToken, selector.ClosedParToken}},
		{"key in ( value1, value2 )", []selector.Token{selector.IdentifierToken, selector.InToken, selector.OpenParToken, selector.IdentifierToken, selector.CommaToken, selector.IdentifierToken, selector.ClosedParToken}},
		{"key", []selector.Token{selector.IdentifierToken}},
		{"!key", []selector.Token{selector.DoesNotExistToken, selector.IdentifierToken}},
		{"()", []selector.Token{selector.OpenParToken, selector.ClosedParToken}},
		{"x in (),y", []selector.Token{selector.IdentifierToken, selector.InToken, selector.OpenParToken, selector.ClosedParToken, selector.CommaToken, selector.IdentifierToken}},
		{"== != (), = notin", []selector.Token{selector.DoubleEqualsToken, selector.NotEqualsToken, selector.OpenParToken, selector.ClosedParToken, selector.CommaToken, selector.EqualsToken, selector.NotInToken}},
		{"key>2", []selector.Token{selector.IdentifierToken, selector.GreaterThanToken, selector.IdentifierToken}},
		{"key<1", []selector.Token{selector.IdentifierToken, selector.LessThanToken, selector.IdentifierToken}},
		{"key contains a, key notcontains b", []selector.Token{selector.IdentifierToken, selector.ContainsToken, selector.IdentifierToken, selector.CommaToken, selector.IdentifierToken, selector.NotContainsToken, selector.IdentifierToken}},
		{"key contains a=b, key notcontains b", []selector.Token{selector.IdentifierToken, selector.ContainsToken, selector.IdentifierToken, selector.CommaToken, selector.IdentifierToken, selector.NotContainsToken, selector.IdentifierToken}},
		{"key contains a=b, key notcontains hello!", []selector.Token{selector.IdentifierToken, selector.ContainsToken, selector.IdentifierToken, selector.CommaToken, selector.IdentifierToken, selector.NotContainsToken, selector.IdentifierToken}},
	}

	for _, v := range testcases {
		var tokens []selector.Token
		l := &lexer{s: v.s, pos: 0}
		for {
			token, _ := l.Lex()
			if token == selector.EndOfStringToken {
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
