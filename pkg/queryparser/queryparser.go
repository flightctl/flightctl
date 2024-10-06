package queryparser

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// WithTokenizer sets a custom tokenizer for the query parser.
// This allows for overriding the default tokenization behavior with a user-provided tokenizer
func WithTokenizer(tokenizer Tokenizer) ParserOption {
	return func(qp *queryParser) error {
		qp.tokenizer = tokenizer
		return nil
	}
}

// WithParams sets a list of query parameters for the query parser.
// This allows for a specified number of additional parameters that influence the parsing process by replacing value placeholders.
// Placeholders in the query input (e.g., func($1, $2)) will be replaced with the corresponding values from the provided parameters.
func WithParams(params []string) ParserOption {
	return func(qp *queryParser) error {
		qp.params = params
		return nil
	}
}

// WithFunctions sets a list of query functions for the query parser.
// This allows for defining the set of functions that the query parser will use.
func WithFunctions(funcs QueryFuncSet) ParserOption {
	return func(qp *queryParser) error {
		qp.funcs = funcs
		return nil
	}
}

// Parse process the input query with the provided options and returns a structured QueryFuncHandler representation.
func Parse(ctx context.Context, input any, options ...ParserOption) (*QueryFunc, error) {
	var err error
	var opStack []*QueryFunc
	var tokens TokenSet

	qp := &queryParser{}
	if err := processOptions(qp, options); err != nil {
		return nil, err
	}

	if tokens, err = qp.tokenize(ctx, input); err != nil {
		return nil, err
	}

	if err := verifyTokens(tokens, qp.funcs); err != nil {
		return nil, fmt.Errorf("tokens verification failed: %w", err)
	}

	// Prepend a dummy function to handle initial state
	tokens = append(TokenSet{{Type: TokenFunc, Value: RootFunc}}, tokens...)
	for i, token := range tokens {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		switch token.Type {
		case TokenFunc:
			fn, err := AssertType[string](token.Value)
			if err != nil {
				return nil, fmt.Errorf("invalid function name: %w", err)
			}
			opStack = append(opStack, &QueryFunc{name: fn})

		case TokenValue:
			val := token.Value
			if v, ok := isParam(token); ok {
				n, err := extractParamNumber(v)
				if err != nil {
					return nil, fmt.Errorf("unable to extract param number at token position %d: %w", i, err)
				}
				if err := validateParamNumber(n, len(qp.params), i); err != nil {
					return nil, fmt.Errorf("param number %d is out of bounds at token position %d", i, n)
				}
				val = qp.params[n-1]
			}
			opStack[len(opStack)-1].args = append(opStack[len(opStack)-1].args, &QueryArgValue{val})

		case TokenFuncClose:
			// Ensure there's a corresponding function to close
			// NOTE: Dummy function is inside the queue
			if len(opStack) < 2 {
				return nil, fmt.Errorf("unexpected function close at token position %d", i)
			}

			currentFunc := opStack[len(opStack)-1]
			if err := executeFunction(currentFunc, qp.funcs, i); err != nil {
				return nil, err
			}

			// Pop the completed function
			opStack = opStack[:len(opStack)-1]

			// Add the completed function as an argument to the previous function
			opStack[len(opStack)-1].args = append(opStack[len(opStack)-1].args, &QueryArgFunc{currentFunc})

		default:
			return nil, fmt.Errorf("unknown token type at token position %d: %s", i, token.Type)
		}
	}

	if len(opStack) > 1 {
		return nil, fmt.Errorf("unmatched function: %v", opStack)
	}

	return opStack[0], nil
}

// Tokenize breaks the input into tokens. It recognizes function names and values.
//
// Example Input:
// "AND(EQ(key1, val1), OR(NOTEQ(key2, val2), NOTEQ(key3, val3)))"
//
// This input contains:
//
// - Function names: AND, EQ, OR, NOTEQ
//
// - Literals: key1, key2, key3, val1, val2, val3
//
// The function will return tokens representing these elements, allowing further processing of the query.
func Tokenize(ctx context.Context, input string) (TokenSet, error) {
	var tokens TokenSet
	var current strings.Builder
	openParenCount := 0
	pos := 0
	expectComma := false
	escaped := false

	for i, char := range input {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		// Handle escape sequences with '\'
		if escaped {
			current.WriteRune(char)
			escaped = false
			continue
		}

		switch char {
		case '\\':
			// If the current character is a backslash, escape the next character
			escaped = true
		case '(':
			if currentStr := strings.TrimSpace(current.String()); currentStr != "" {
				for _, c := range currentStr {
					if unicode.IsSpace(c) {
						return nil, fmt.Errorf("function name at position %d cannot contain spaces", pos)
					}
				}
				tokens = append(tokens, Token{Type: TokenFunc, Value: currentStr})
				current.Reset()
			} else {
				return nil, fmt.Errorf("function name cannot be empty at position %d", i)
			}
			openParenCount++
		case ')':
			if currentStr := strings.TrimSpace(current.String()); currentStr != "" {
				tokens = append(tokens, Token{Type: TokenValue, Value: currentStr})
				current.Reset()
			}
			if openParenCount == 0 {
				return nil, fmt.Errorf("mismatched closing parenthesis at position %d", i)
			}
			tokens = append(tokens, Token{Type: TokenFuncClose})
			openParenCount--
			expectComma = true // After a closing parenthesis, expect a comma or another closing parenthesis
		case ',':
			if currentStr := strings.TrimSpace(current.String()); currentStr != "" {
				tokens = append(tokens, Token{Type: TokenValue, Value: currentStr})
				current.Reset()
			} else if !expectComma {
				return nil, fmt.Errorf("unexpected character '%c' at position %d", char, i)
			}
			expectComma = false // After a comma, expect a new function or value
		default:
			if unicode.IsSpace(char) && current.Len() == 0 {
				continue
			}
			if expectComma {
				return nil, fmt.Errorf("unexpected character '%c' at position %d, expected a comma", char, i)
			}
			pos = i - current.Len()
			current.WriteRune(char)
		}
	}

	if currentStr := strings.TrimSpace(current.String()); currentStr != "" {
		tokens = append(tokens, Token{Type: TokenValue, Value: currentStr})
	}

	if openParenCount > 0 {
		return nil, fmt.Errorf("mismatched opening parenthesis")
	}

	return tokens, nil
}

// IsValue checks if the provided QueryArg is of type QueryArgValue.
func IsValue(arg QueryArg) bool {
	switch arg.(type) {
	case *QueryArgValue:
		return true
	default:
		return false
	}
}

// Checks that all function tokens have matching closing tokens.
// This is essential when the tokens slice is created manually to ensure
// proper structure and validity of the query representation.
func verifyTokens(tokens TokenSet, funcs QueryFuncSet) error {
	if tokens.IsEmpty() {
		return nil
	}

	openFuncCount := 0
	opStack := make(TokenSet, 0, len(tokens))
	for i, token := range tokens {
		switch token.Type {
		case TokenFunc:
			fn, err := AssertType[string](token.Value)
			if err != nil {
				return fmt.Errorf("invalid function name: %w", err)
			}

			if len(fn) == 0 {
				return fmt.Errorf("function name must not be empty")
			}
			currentFunc, exists := funcs[fn]
			if !exists {
				return fmt.Errorf("function name %s is undefined at token position %d", token.Value, i)
			}

			// Check whether the function is limited to certain parent functions
			if currentFunc.UsedBy.Size() > 0 {
				parentFunc := RootFunc
				if len(opStack) > 0 {
					parentFunc, _ = AssertType[string](opStack[len(opStack)-1].Value)
				}

				if !currentFunc.UsedBy.Contains(parentFunc) {
					return fmt.Errorf("function name %s at token position %d is restricted to functions %v",
						token.Value, i, currentFunc.UsedBy.Print())
				}
			}
			openFuncCount++
			opStack = append(opStack, token)

		case TokenFuncClose:
			if openFuncCount == 0 {
				return fmt.Errorf("unexpected closing function without matching opening function at token position %d", i)
			}
			openFuncCount--
			opStack = opStack[:len(opStack)-1]
		case TokenValue:
			// continue
		default:
			return fmt.Errorf("unknown token type at token position %d: %s", i, token.Type)
		}
	}

	if openFuncCount > 0 {
		return fmt.Errorf("mismatched opening function")
	}

	return nil
}

func executeFunction(qf *QueryFunc, funcs QueryFuncSet, position int) error {
	function, exists := funcs[qf.name]
	if !exists {
		return fmt.Errorf("function name %s is undefined at token position %d", qf.name, position)
	}

	if err := function.Invoke(qf); err != nil {
		return fmt.Errorf("query function %s at token position %d has returned an error: %w", qf.name, position, err)
	}

	return nil
}

func processOptions(qp *queryParser, options []ParserOption) error {
	for _, opt := range options {
		if err := opt(qp); err != nil {
			return err
		}
	}
	return nil
}

func (qp *queryParser) tokenize(ctx context.Context, input any) (TokenSet, error) {
	if qp.tokenizer != nil {
		tokens, err := qp.tokenizer.Tokenize(ctx, input)
		if err != nil {
			return nil, fmt.Errorf("unable to tokenize input using custom tokenizer: %w", err)
		}
		return tokens, nil
	}

	// Ensure the input is a string, as the default tokenizer expects a string input.
	strInput, ok := input.(string)
	if !ok {
		return nil, fmt.Errorf("expected input of type string, got %T", input)
	}

	tokens, err := Tokenize(ctx, strInput)
	if err != nil {
		return nil, fmt.Errorf("unable to tokenize input using default tokenizer: %w", err)
	}
	return tokens, nil
}

var paramPattern = regexp.MustCompile(`^\$(\d+)$`)

func isParam(token Token) (string, bool) {
	if v, err := AssertType[string](token.Value); err == nil && token.Type == TokenValue {
		if paramPattern.MatchString(v) {
			return v, true
		}
	}
	return "", false
}

func extractParamNumber(token string) (int, error) {
	matches := paramPattern.FindStringSubmatch(token)
	if len(matches) != 2 {
		return 0, fmt.Errorf("not a valid parameter format")
	}
	return strconv.Atoi(matches[1])
}

func validateParamNumber(paramNumber, paramCount, tokenPosition int) error {
	if paramNumber < 1 || paramNumber > paramCount {
		return fmt.Errorf("param number %d is out of bounds at token position %d", paramNumber, tokenPosition)
	}
	return nil
}
