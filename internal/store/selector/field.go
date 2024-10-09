package selector

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/pkg/queryparser"
	"github.com/flightctl/flightctl/pkg/queryparser/sql"
	"gorm.io/gorm/schema"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/selection"
)

type fieldSelector struct {
	parser        queryparser.Parser
	fieldResolver *selectorFieldResolver
	operators     map[selection.Operator]string
}

func NewFieldSelector(dest any) (*fieldSelector, error) {
	fs := &fieldSelector{
		operators: map[selection.Operator]string{
			selection.Equals:       "EQ",
			selection.DoubleEquals: "EQ",
			selection.NotEquals:    "NEQ",
		},
	}

	var err error
	fs.fieldResolver, err = SelectorFieldResolver(dest)
	if err != nil {
		return nil, err
	}

	fs.parser, err = sql.NewSQLParser(
		sql.WithTokenizer(fs),
		sql.WithOverrideFunction("K", sql.Wrap(fs.queryField)),
	)
	if err != nil {
		return nil, err
	}

	return fs, nil
}

// ParseFromString parses the selector string and returns a SQL query with parameters.
func (fs *fieldSelector) ParseFromString(ctx context.Context, input string) (string, []any, error) {
	selector, err := fields.ParseSelector(input)
	if err != nil {
		return "", nil, NewSelectorError(flterrors.ErrFieldSelectorSyntax, err)
	}

	q, args, err := fs.parser.Parse(ctx, selector)
	if err != nil {
		if se, ok := IsSelectorError(err); ok {
			return "", nil, se
		}
		return "", nil, NewSelectorError(flterrors.ErrFieldSelectorParseFailed, err)
	}
	return q, args, nil
}

// Parse parses the selector and returns a SQL query with parameters.
func (fs *fieldSelector) Parse(ctx context.Context, selector fields.Selector) (string, []any, error) {
	q, args, err := fs.parser.Parse(ctx, selector)
	if err != nil {
		if se, ok := IsSelectorError(err); ok {
			return "", nil, se
		}
		return "", nil, NewSelectorError(flterrors.ErrFieldSelectorParseFailed, err)
	}
	return q, args, nil
}

// Tokenize converts a selector string into a set of queryparser tokens.
func (fs *fieldSelector) Tokenize(ctx context.Context, input any) (queryparser.TokenSet, error) {
	if input == "" {
		return nil, nil
	}

	// Assert that input is a selector
	selector, ok := input.(fields.Selector)
	if !ok {
		return nil, fmt.Errorf("invalid input type: expected fieldSelector, got %T", input)
	}

	requirements := selector.Requirements()
	tokens := make(queryparser.TokenSet, 0)

	for _, req := range requirements {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		field, value, operator := SelectorFieldName(strings.TrimSpace(req.Field)), req.Value, req.Operator
		resolvedFields, err := fs.fieldResolver.ResolveFields(field)
		if err != nil {
			return nil, err
		}

		resolvedTokens := queryparser.NewTokenSet()
		for _, resolvedField := range resolvedFields {
			fieldToken, err := fs.createFieldToken(field, resolvedField)
			if err != nil {
				return nil, err
			}

			valueToken, err := fs.createValueToken(resolvedField, value)
			if err != nil {
				return nil, err
			}

			operatorToken, err := fs.createOperatorToken(operator, resolvedField, fieldToken, valueToken)
			if err != nil {
				return nil, err
			}

			resolvedTokens.Append(operatorToken)
		}

		// If multiple fields are resolved, wrap them in an OR token
		if len(resolvedFields) > 1 {
			tokens.AddFunctionToken("OR", func(ts *queryparser.TokenSet) {
				ts.Append(resolvedTokens)
			})
		} else {
			tokens.Append(resolvedTokens)
		}
	}

	if len(requirements) > 1 {
		andTokens := make(queryparser.TokenSet, 0, len(tokens)+2)
		andTokens.AddFunctionToken("AND", func(ts *queryparser.TokenSet) {
			ts.Append(&tokens)
		})
		tokens = andTokens
	}

	return tokens, nil
}

type resolverFunc[T any] func(T) *queryparser.TokenSet

func (fs *fieldSelector) createFieldToken(field SelectorFieldName, schemaField *schema.Field) (*queryparser.TokenSet, error) {
	return fs.resolveField(field, schemaField, func(f string) *queryparser.TokenSet {
		return queryparser.NewTokenSet().AddFunctionToken("K", func(ts *queryparser.TokenSet) {
			ts.AddValueToken(f)
		})
	})
}

func (fs *fieldSelector) createValueToken(schemaField *schema.Field, value string) (*queryparser.TokenSet, error) {
	return fs.resolveValue(schemaField, value, func(v any) *queryparser.TokenSet {
		return queryparser.NewTokenSet().AddFunctionToken("V", func(ts *queryparser.TokenSet) {
			ts.AddValueToken(v)
		})
	})
}

func (fs *fieldSelector) createOperatorToken(operator selection.Operator, schemaField *schema.Field, fieldToken, valueToken *queryparser.TokenSet) (*queryparser.TokenSet, error) {
	return fs.resolveQuery(operator, schemaField, func(op string) *queryparser.TokenSet {
		return queryparser.NewTokenSet().AddFunctionToken(op, func(ts *queryparser.TokenSet) {
			ts.Append(fieldToken)
			ts.Append(valueToken)
		})
	})
}

func (fs *fieldSelector) resolveQuery(operator selection.Operator, schemaField *schema.Field, resolve resolverFunc[string]) (*queryparser.TokenSet, error) {
	op, exists := fs.operators[operator]
	if !exists {
		return nil, NewSelectorError(flterrors.ErrFieldSelectorSyntax,
			fmt.Errorf("unknown operator %q", operator))
	}

	switch schemaField.DataType {
	case "text[]":
		return fs.applyTextArrayOperator(operator, resolve)
	case "time":
		return fs.applyTimestampOperator(operator, resolve)
	default:
		return resolve(op), nil
	}
}

func (fs *fieldSelector) resolveField(field SelectorFieldName, schemaField *schema.Field, resolve resolverFunc[string]) (*queryparser.TokenSet, error) {
	switch schemaField.DataType {
	case "jsonb":
		return resolve(string(field)), nil
	default:
		return resolve(schemaField.DBName), nil
	}
}

func (fs *fieldSelector) resolveValue(schemaField *schema.Field, value string, resolve resolverFunc[any]) (*queryparser.TokenSet, error) {
	switch schemaField.DataType {
	case schema.Int:
		v, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil {
			return nil, NewSelectorError(flterrors.ErrFieldSelectorParseInput, err)
		}
		return resolve(v), nil

	case schema.Uint:
		v, err := strconv.ParseUint(strings.TrimSpace(value), 10, 0)
		if err != nil {
			return nil, NewSelectorError(flterrors.ErrFieldSelectorParseInput, err)
		}
		return resolve(v), nil

	case schema.Bool:
		v, err := strconv.ParseBool(strings.TrimSpace(value))
		if err != nil {
			return nil, NewSelectorError(flterrors.ErrFieldSelectorParseInput, err)
		}
		return resolve(v), nil

	case schema.Float:
		v, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
		if err != nil {
			return nil, NewSelectorError(flterrors.ErrFieldSelectorParseInput, err)
		}
		return resolve(v), nil

	case schema.Time:
		v, err := time.Parse(time.RFC3339, strings.TrimSpace(value))
		if err != nil {
			return nil, NewSelectorError(flterrors.ErrFieldSelectorParseInput, err)
		}
		return resolve(v), nil

	default:
		return resolve(value), nil
	}
}

// applyTextArrayOperator applies the appropriate operator for text array fields.
func (fs *fieldSelector) applyTextArrayOperator(operator selection.Operator, resolve resolverFunc[string]) (*queryparser.TokenSet, error) {
	switch operator {
	case selection.Equals, selection.DoubleEquals:
		return resolve("CONTAINS"), nil
	case selection.NotEquals:
		return resolve("NCONTAINS"), nil
	default:
		return nil, NewSelectorError(flterrors.ErrFieldSelectorSyntax,
			fmt.Errorf("operator %q is unsupported for the type text[]", operator))
	}
}

// applyTimestampOperator applies the appropriate operator for timestamp fields.
func (fs *fieldSelector) applyTimestampOperator(operator selection.Operator, resolve resolverFunc[string]) (*queryparser.TokenSet, error) {
	switch operator {
	case selection.Equals, selection.DoubleEquals, selection.NotEquals:
		return resolve(fs.operators[operator]), nil
	default:
		return nil, NewSelectorError(flterrors.ErrFieldSelectorSyntax,
			fmt.Errorf("operator %q is unsupported for the type timestamp", operator))
	}
}

var fieldRegex = regexp.MustCompile(`^[a-zA-Z0-9._]+$`)

func (fs *fieldSelector) queryField(args ...string) (*sql.FunctionResult, error) {
	if len(args) != 1 {
		return nil, fmt.Errorf("expected one argument")
	}

	field := strings.TrimSpace(args[0])
	if !fieldRegex.MatchString(field) {
		return nil, fmt.Errorf("the field name contains invalid characters")
	}

	return &sql.FunctionResult{
		Query: createParamsFromKey(field),
	}, nil
}

// createParamsFromKey constructs a SQL query parameter string from a dot-separated key.
// Intermediate parts are prefixed with the -> operator for JSONB, and the last part is prefixed with the ->> operator for JSONB fetching text.
func createParamsFromKey(key string) string {
	parts := strings.Split(key, ".")
	if len(parts) == 0 {
		return ""
	}

	var params strings.Builder
	for i, part := range parts {
		if i == 0 {
			params.WriteString(part)
		} else if i == len(parts)-1 {
			params.WriteString(fmt.Sprintf(" ->> '%s'", part))
		} else {
			params.WriteString(fmt.Sprintf(" -> '%s'", part))
		}
	}

	return params.String()
}
