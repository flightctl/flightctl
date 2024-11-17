package fields

import (
	"fmt"

	"github.com/flightctl/flightctl/pkg/k8s/selector"
)

// string2token contains the mapping between lexer Token and token literal
// (except IdentifierToken, EndOfStringToken and ErrorToken since it makes no sense)
var string2token = map[string]selector.Token{
	")":           selector.ClosedParToken,
	",":           selector.CommaToken,
	"!":           selector.DoesNotExistToken,
	"==":          selector.DoubleEqualsToken,
	"=":           selector.EqualsToken,
	">":           selector.GreaterThanToken,
	">=":          selector.GreaterThanOrEqualsToken,
	"contains":    selector.ContainsToken,
	"in":          selector.InToken,
	"<":           selector.LessThanToken,
	"<=":          selector.LessThanOrEqualsToken,
	"!=":          selector.NotEqualsToken,
	"notcontains": selector.NotContainsToken,
	"notin":       selector.NotInToken,
	"(":           selector.OpenParToken,
}

// Lexer represents the Lexer struct.
// It contains necessary information to tokenize an input string.
type lexer struct {
	// s stores the string to be tokenized
	s string
	// pos is the current position in the string being tokenized
	pos int

	// state holds the current context and parsing state of the lexer.
	state lexerState
}

type lexerContext int

const (
	// lhsOrOp represents the initial context where the lexer expects either
	// the left-hand side (lhs) of an expression (e.g., a key or identifier)
	// or an operator (e.g., "!").
	lhsOrOp lexerContext = iota

	// lhs represents the context where the lexer is processing the left-hand side
	// of an expression, such as a key or identifier (e.g., "name" or "age").
	lhs

	// op represents the context where the lexer is processing an operator,
	// such as "=", "!=", ">", or "<".
	op

	// rhs represents the context where the lexer is processing the right-hand side
	// of an expression, such as a value or literal (e.g., "John" or "30").
	rhs
)

type lexerState struct {
	context       lexerContext
	inParentheses int
}

// ScannedItem contains the Token and the literal produced by the lexer.
type ScannedItem struct {
	tok     selector.Token
	literal string
}

// Lex returns a pair of Token and the literal.
// The literal is meaningful only for IdentifierToken.
func (l *lexer) Lex() (tok selector.Token, lit string) {
	switch ch := l.skipWhiteSpaces(l.read()); {
	case ch == 0: // End of input
		l.state.context = lhsOrOp
		return selector.EndOfStringToken, ""

	case isSyntaxChar(ch): // Handle syntax-related characters
		l.unread()
		tok, lit = l.scanSyntaxChar()
		l.updateContextForSyntaxChar(tok)
		return tok, lit

	default: // Handle identifiers or keywords
		l.unread()
		tok, lit = l.scanIDOrKeyword()
		l.updateContextForIdentifiers()
		return tok, lit
	}
}

// updateContextForSyntaxChar updates the lexer state based on a syntax token.
func (l *lexer) updateContextForSyntaxChar(tok selector.Token) {
	switch l.state.context {
	case lhsOrOp:
		l.state.context = lhs
	case op:
		l.state.context = rhs
	default:
		switch tok {
		case selector.OpenParToken:
			l.state.inParentheses++
		case selector.ClosedParToken:
			l.state.inParentheses--
		case selector.CommaToken:
			if l.state.inParentheses == 0 {
				l.state.context = lhsOrOp
			}
		}
	}
}

// updateContextForIdentifiers updates the lexer state after scanning an identifier or keyword.
func (l *lexer) updateContextForIdentifiers() {
	switch l.state.context {
	case lhs, lhsOrOp:
		l.state.context = op
	case op:
		l.state.context = rhs
	}
}

func isSyntaxCharForVal(ch byte) bool {
	switch ch {
	case '(', ')', ',':
		return true
	}
	return false
}

// isSyntaxChar detects if the character ch can be part of syntax.
func isSyntaxChar(ch byte) bool {
	switch ch {
	case '=', '!', '(', ')', ',', '>', '<':
		return true
	}
	return false
}

// read returns the character currently being lexed.
// It increments the position and checks for buffer overflow.
func (l *lexer) read() (b byte) {
	if l.pos < len(l.s) {
		b = l.s[l.pos]
		l.pos++
	}
	return b
}

// unread undoes the last read operation.
func (l *lexer) unread() {
	if l.pos > 0 {
		l.pos--
	}
}

// scanIDOrKeyword scans the input to recognize a literal token (e.g., 'in') or an identifier.
func (l *lexer) scanIDOrKeyword() (tok selector.Token, lit string) {
	var buffer []byte
	escapeNextChar := false

	for {
		ch := l.read()

		// End of input
		if ch == 0 {
			break
		}

		// Handle whitespace
		if isWhitespace(ch) && !escapeNextChar {
			l.unread()
			break
		}

		// Handle special symbols based on context
		if !escapeNextChar && ((l.state.context == rhs && isSyntaxCharForVal(ch)) ||
			(l.state.context != rhs && isSyntaxChar(ch))) {
			l.unread()
			break
		}

		// Handle escape sequences
		if ch == '\\' && !escapeNextChar {
			escapeNextChar = true
			continue
		}

		// Append character to buffer
		escapeNextChar = false
		buffer = append(buffer, ch)
	}

	// Convert buffer to string
	s := string(buffer)

	// Check if the string matches a literal token
	if val, ok := string2token[s]; ok {
		return val, s
	}

	// Return as an identifier token if no match is found
	return selector.IdentifierToken, s
}

// scanSyntaxChar scans input starting with a syntax-related character.
// syntax-related character identify non literal operators. "!=", "==", "="
func (l *lexer) scanSyntaxChar() (selector.Token, string) {
	lastScannedItem := ScannedItem{}
	var buffer []byte
SpecialSymbolLoop:
	for {
		switch ch := l.read(); {
		case ch == 0:
			break SpecialSymbolLoop
		case isSyntaxChar(ch):
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
		return selector.ErrorToken, fmt.Sprintf("error expected: keyword found '%s'", buffer)
	}
	return lastScannedItem.tok, lastScannedItem.literal
}

// skipWhiteSpaces consumes all blank characters and returns the first non-blank character.
func (l *lexer) skipWhiteSpaces(ch byte) byte {
	for isWhitespace(ch) {
		ch = l.read()
	}
	return ch
}

// isWhitespace returns true if the rune is a space, tab, or newline.
func isWhitespace(ch byte) bool {
	return ch == ' ' || ch == '\t' || ch == '\r' || ch == '\n'
}
