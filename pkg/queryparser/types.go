package queryparser

import (
	"context"
	"fmt"
	"strconv"
	"time"
)

const (
	TokenFunc      = "FUNC"
	TokenValue     = "VALUE"
	TokenFuncClose = "FUNC-CLOSE"

	// RootFunc serves as a seed function that represents the initial state
	// when there is no parent function. It can be useful for indicating
	// the top level of a query structure.
	RootFunc = "ROOT"
)

// Parser defines an interface for parsing input queries with additional parameters
// within a given context, returning a parsed query, additional extracted data,
// and any errors encountered during the process.
type Parser interface {
	// Parse takes an input query and a variadic number of parameters, processes the input
	// based on these parameters, and returns the parsed result, additional extracted
	// data, and any error encountered during parsing.
	//
	// # Parameters:
	//   - ctx: A context to manage deadlines, cancellations, and other request-scoped values.
	//   - input: The input to be parsed.
	//   - params: A variadic number of additional parameters that influence the parsing process.
	//
	// # Returns:
	//   - string: The parsed query.
	//   - []any: Additional data extracted during parsing.
	//   - error: An error object providing details if the parsing fails.
	Parse(ctx context.Context, input any, params ...string) (string, []any, error)
}

// Token represents a token in the input.
type Token struct {
	Type  string
	Value any
}

// TokenSet represents a collection of tokens.
type TokenSet []Token

// NewTokenSet creates and initializes a new TokenSet with an optional capacity.
func NewTokenSet(cap ...int) TokenSet {
	if len(cap) > 0 && cap[0] > 0 {
		return make(TokenSet, 0, cap[0])
	}
	return make(TokenSet, 0)
}

// AddFunctionToken adds a function token with the specified name to the TokenSet.
// If the addTokens function is provided, it will be called to add additional tokens
// between the function tokens.
func (s TokenSet) AddFunctionToken(name string, addTokens func() TokenSet) TokenSet {
	s = append(s, Token{
		Type:  TokenFunc,
		Value: name,
	})

	if addTokens != nil {
		s = append(s, addTokens()...)
	}

	s = append(s, Token{
		Type: TokenFuncClose,
	})
	return s
}

// AddValueToken adds a value token with the specified value to the TokenSet.
func (s TokenSet) AddValueToken(value any) TokenSet {
	s = append(s, Token{
		Type:  TokenValue,
		Value: value,
	})
	return s
}

// Append appends the tokens from multiple TokenSets to the current TokenSet.
func (s TokenSet) Append(sets ...TokenSet) TokenSet {
	for _, set := range sets {
		if len(set) > 0 {
			s = append(s, set...)
		}
	}
	return s
}

// Matches checks if the current TokenSet matches another TokenSet.
func (s TokenSet) Matches(dest TokenSet) bool {
	if len(s) != len(dest) {
		return false
	}

	for i, token := range s {
		switch token.Type {
		case TokenFunc:
			if dest[i].Type != TokenFunc {
				return false
			}
			if fnName, ok := token.Value.(string); !ok || fnName != dest[i].Value.(string) {
				return false
			}

		case TokenFuncClose:
			if dest[i].Type != TokenFuncClose {
				return false
			}

		case TokenValue:
			if dest[i].Type != TokenValue {
				return false
			}
			if toString(s[i].Value) != toString(dest[i].Value) {
				return false
			}
		}
	}
	return true
}

// IsEmpty checks if the TokenSet is empty.
func (s TokenSet) IsEmpty() bool {
	return len(s) == 0
}

// toString converts various types to a string representation.
func toString(v interface{}) string {
	switch v := v.(type) {
	case string:
		return v
	case bool:
		return strconv.FormatBool(v)
	case int, int8, int16, int32, int64:
		return fmt.Sprintf("%d", v)
	case uint, uint8, uint16, uint32, uint64:
		return fmt.Sprintf("%d", v)
	case float32, float64:
		return fmt.Sprintf("%f", v)
	case time.Time:
		return v.Format(time.RFC3339)
	default:
		return fmt.Sprintf("%v", v) // Fallback, but should rarely be used.
	}
}

// QueryFuncHandler represents a query function that includes an invocation method and a set of allowed functions that can call this function.
type QueryFuncHandler struct {
	Invoke func(qf *QueryFunc) error
	UsedBy *Set[string]
}

// QueryFuncSet is a map of query function names to their corresponding QueryFuncHandler.
type QueryFuncSet map[string]QueryFuncHandler

// QueryArg is an interface that represents an argument in a query.
type QueryArg interface {
}

// QueryArgFunc represents a function argument in a query.
type QueryArgFunc struct {
	qf *QueryFunc
}

// Value returns the underlying QueryFunc associated with the QueryArgFunc.
func (qa *QueryArgFunc) Value() *QueryFunc {
	return qa.qf
}

// QueryArgValue represents a value argument in a query.
type QueryArgValue struct {
	val any
}

// Value returns the value of the QueryArgValue.
func (qa *QueryArgValue) Value() any {
	return qa.val
}

// QueryFunc represents a function in the query.
// It holds the function's name, its arguments, and the result of its execution.
type QueryFunc struct {
	name string
	args []QueryArg
	res  interface{}
}

// The name of the function as a string.
func (qf *QueryFunc) Name() string {
	return qf.name
}

// A slice of QueryArg that contains the arguments passed to the function.
// Each argument can be a value or another function.
func (qf *QueryFunc) Args() []QueryArg {
	return qf.args
}

// Holds the result of the function execution.
// The type of this field can vary depending on the function's output.
func (qf *QueryFunc) Result() interface{} {
	return qf.res
}

// Set the result of the function execution.
func (qf *QueryFunc) SetResult(res interface{}) {
	qf.res = res
}

// Tokenizer defines the interface for tokenizing an input into a TokenSet.
// The Tokenize method takes a context and an input, and returns a TokenSet or an error.
type Tokenizer interface {
	Tokenize(ctx context.Context, input any) (TokenSet, error)
}

type queryParser struct {
	tokenizer Tokenizer
	funcs     QueryFuncSet
	params    []string
}

type ParserOption func(*queryParser) error
