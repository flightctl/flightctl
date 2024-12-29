package selector

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/pkg/k8s/selector"
	"github.com/flightctl/flightctl/pkg/k8s/selector/fields"
	"github.com/flightctl/flightctl/pkg/k8s/selector/selection"
	"github.com/flightctl/flightctl/pkg/queryparser"
	"github.com/flightctl/flightctl/pkg/queryparser/sql"
)

type FieldSelector struct {
	parser           queryparser.Parser
	fieldResolver    *selectorFieldResolver
	selector         selector.Selector
	privateSelectors bool
}

type FieldSelectorOption func(*FieldSelector)

// WithPrivateSelectors enables the use of private selectors in the FieldSelector.
// Private selectors are internal selectors that can be used for
// specific processing but are not exposed to the end-user for querying.
func WithPrivateSelectors() FieldSelectorOption {
	return func(fs *FieldSelector) {
		fs.privateSelectors = true
	}
}

// NewFieldSelectorFromMapOrDie creates a FieldSelector from a map of key-value pairs,
// where each pair represents a field selector condition. If the operation fails,
// it panics. This function is a convenience wrapper around NewFieldSelectorFromMap.
//
// The `invert` parameter allows toggling between equality (`=`) and inequality (`!=`) operators
// for the field selector conditions. By default, it uses equality (`=`).
//
// Example:
//
//	fs := NewFieldSelectorFromMapOrDie(map[string]string{"key": "value"}, true)
//	// Equivalent to creating a selector: "key!=value"
func NewFieldSelectorFromMapOrDie(fields map[string]string, invert bool, opts ...FieldSelectorOption) *FieldSelector {
	fs, err := NewFieldSelectorFromMap(fields, invert, opts...)
	if err != nil {
		panic(err)
	}
	return fs
}

// NewFieldSelectorFromMap creates a FieldSelector from a map of key-value pairs,
// where each pair represents a field selector condition.
//
// The `invert` parameter allows toggling between equality (`=`) and inequality (`!=`) operators
// for the field selector conditions. By default, it uses equality (`=`).
//
// Example:
//
//	fs, err := NewFieldSelectorFromMap(map[string]string{"key1": "value1", "key2": "value2"})
//	// Equivalent to creating a selector: "key1=value1,key2=value2"
//
//	fs, err := NewFieldSelectorFromMap(map[string]string{"key1": "value1"}, true)
//	// Equivalent to creating a selector: "key1!=value1"
func NewFieldSelectorFromMap(fields map[string]string, invert bool, opts ...FieldSelectorOption) (*FieldSelector, error) {
	if len(fields) == 0 {
		return NewFieldSelector("")
	}

	operator := selection.Equals
	if invert {
		operator = selection.NotEquals
	}

	var parts []string
	for key, val := range fields {
		parts = append(parts, key+string(operator)+val)
	}

	return NewFieldSelector(strings.Join(parts, ","), opts...)
}

// NewFieldSelectorOrDie creates a FieldSelector from a given string input using Kubernetes selector syntax.
// If the input is invalid or parsing fails, it panics.
//
// This function is useful for cases where selector initialization is expected to succeed,
// and failure is considered a programming error.
//
// Example:
//
//	fs := NewFieldSelectorOrDie("key1=value1,key2!=value2")
//	// Creates a FieldSelector for the given conditions.
//
// Parameters:
//
//	input - A string containing the field selector conditions in Kubernetes selector syntax.
func NewFieldSelectorOrDie(input string, opts ...FieldSelectorOption) *FieldSelector {
	fs, err := NewFieldSelector(input, opts...)
	if err != nil {
		panic(err)
	}
	return fs
}

// NewFieldSelector creates a FieldSelector from a given string input using Kubernetes selector syntax.
// This function parses the input string to generate a FieldSelector that can be used for filtering resources
// based on specified field conditions.
//
// Example:
//
//	fs, err := NewFieldSelector("key1=value1,key2!=value2")
//	if err != nil {
//	    log.Fatalf("Failed to create FieldSelector: %v", err)
//	}
//	// Successfully creates a FieldSelector for the given conditions.
//
// Parameters:
//
//	input - A string containing the field selector conditions in Kubernetes selector syntax.
func NewFieldSelector(input string, opts ...FieldSelectorOption) (*FieldSelector, error) {
	selector, err := fields.ParseSelector(input)
	if err != nil {
		return nil, NewSelectorError(flterrors.ErrFieldSelectorSyntax, err)
	}

	fs := &FieldSelector{
		selector: selector,
	}

	for _, opt := range opts {
		opt(fs)
	}

	return fs, nil
}

// Parse translates a FieldSelector into a SQL query with parameters.
// This method is responsible for resolving field names, operators, and values
// from the FieldSelector and generating a corresponding SQL query that can be
// executed against a database.
//
// The method validates and processes the destination structure (dest) to map field
// names and types correctly, ensuring compatibility with the database schema.
//
// Parameters:
//
//	ctx  - A context.Context to manage the lifetime of the operation.
//	dest - The target object (e.g., a database model) that provides field definitions
//	       for resolving selector fields.
//
// Returns:
//
//	string - The generated SQL query as a string.
//	[]any  - A slice of arguments to be used as parameters for the SQL query.
//	error  - An error if the parsing fails due to invalid input, unresolved fields, or other issues.
//
// Example:
//
//	fs, _ := NewFieldSelector("key1=value1")
//	query, args, err := fs.Parse(ctx, &MyModel{})
//	if err != nil {
//	    log.Fatalf("Failed to parse selector: %v", err)
//	}
//	fmt.Printf("Query: %s, Args: %v\n", query, args)
func (fs *FieldSelector) Parse(ctx context.Context, dest any) (string, []any, error) {
	var err error

	fs.fieldResolver, err = SelectorFieldResolver(dest)
	if err != nil {
		return "", nil, NewSelectorError(flterrors.ErrFieldSelectorParseFailed, err)
	}

	fs.parser, err = sql.NewSQLParser(
		sql.WithTokenizer(fs),
		sql.WithOverrideFunction("K", sql.Wrap(fs.queryField)),
	)
	if err != nil {
		return "", nil, NewSelectorError(flterrors.ErrFieldSelectorParseFailed, err)
	}

	q, args, err := fs.parser.Parse(ctx, fs.selector)
	if err != nil {
		if ok := IsSelectorError(err); ok {
			return "", nil, err
		}
		return "", nil, NewSelectorError(flterrors.ErrFieldSelectorParseFailed, err)
	}
	return q, args, nil
}

// Tokenize converts a selector string into a set of queryparser tokens.
func (fs *FieldSelector) Tokenize(ctx context.Context, input any) (queryparser.TokenSet, error) {
	if input == nil {
		return nil, nil
	}

	if fs.fieldResolver == nil {
		return nil, fmt.Errorf("fieldResolver is not defined")
	}

	// Assert that input is a selector
	selector, ok := input.(selector.Selector)
	if !ok {
		return nil, fmt.Errorf("invalid input type: expected fieldSelector, got %T", input)
	}

	requirements, selectable := selector.Requirements()
	if !selectable {
		return nil, nil
	}

	tokens := make(queryparser.TokenSet, 0)
	for _, req := range requirements {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		key, values, operator := req.Key(), req.Values(), req.Operator()
		resolvedFields, err := fs.resolveSelectorField(key)
		if err != nil {
			return nil, err
		}

		resolvedTokens := queryparser.NewTokenSet()
		for _, resolvedField := range resolvedFields {
			if resolvedField.IsJSONBCast() && resolvedField.Type.IsArray() {
				return nil, fmt.Errorf("cannot cast JSONB to an array of type %q; array casting from JSONB is unsupported",
					resolvedField.Type.String())
			}

			fieldToken, err := fs.createFieldToken(resolvedField)
			if err != nil {
				return nil, NewSelectorError(flterrors.ErrFieldSelectorParseFailed,
					fmt.Errorf("failed to parse selector %q: %w", key, err))
			}

			var valuesToken queryparser.TokenSet
			if values.Len() > 0 {
				valuesToken = queryparser.NewTokenSet()
				for _, val := range values.List() {
					valueToken, err := fs.createValueToken(operator, resolvedField, val)
					if err != nil {
						return nil, NewSelectorError(flterrors.ErrFieldSelectorParseFailed,
							fmt.Errorf("failed to parse value for selector %q: %w", key, err))
					}
					valuesToken = valuesToken.Append(valueToken)
				}
			}

			operatorToken, err := fs.createOperatorToken(operator, resolvedField, fieldToken, valuesToken)
			if err != nil {
				return nil, NewSelectorError(flterrors.ErrFieldSelectorParseFailed,
					fmt.Errorf("failed to resolve operation for selector %q: %w", key, err))
			}

			resolvedTokens = resolvedTokens.Append(operatorToken)
		}

		// If multiple fields are resolved, wrap them in an OR token
		if len(resolvedFields) > 1 {
			tokens = tokens.AddFunctionToken("OR", func() queryparser.TokenSet {
				return resolvedTokens
			})
		} else {
			tokens = tokens.Append(resolvedTokens)
		}
	}

	if len(requirements) > 1 {
		tokens = queryparser.NewTokenSet(len(tokens)+2).AddFunctionToken("AND", func() queryparser.TokenSet {
			return tokens
		})
	}

	return tokens, nil
}

type resolverFunc[T any] func(T) queryparser.TokenSet

func (fs *FieldSelector) createFieldToken(selectorField *SelectorField) (queryparser.TokenSet, error) {
	return fs.resolveField(selectorField, func(f string) queryparser.TokenSet {
		return queryparser.NewTokenSet().AddFunctionToken("K", func() queryparser.TokenSet {
			return queryparser.NewTokenSet().AddValueToken(f)
		})
	})
}

func (fs *FieldSelector) createValueToken(operator selection.Operator, selectorField *SelectorField, value string) (queryparser.TokenSet, error) {
	return fs.resolveValue(operator, selectorField, value, func(v any) queryparser.TokenSet {
		return queryparser.NewTokenSet().AddFunctionToken("V", func() queryparser.TokenSet {
			return queryparser.NewTokenSet().AddValueToken(v)
		})
	})
}

func (fs *FieldSelector) createOperatorToken(operator selection.Operator, selectorField *SelectorField, fieldToken, valueToken queryparser.TokenSet) (queryparser.TokenSet, error) {
	return fs.resolveQuery(operator, selectorField, func(op string) queryparser.TokenSet {
		switch operator {
		case selection.Exists, selection.DoesNotExist:
			// Avoid using JSONB casting in default case
			return queryparser.NewTokenSet().AddFunctionToken(op, func() queryparser.TokenSet {
				return queryparser.NewTokenSet().Append(fieldToken, valueToken)
			})
		case selection.NotEquals, selection.NotIn:
			return queryparser.NewTokenSet().AddFunctionToken("OR", func() queryparser.TokenSet {
				return queryparser.NewTokenSet().AddFunctionToken("ISNULL", func() queryparser.TokenSet { return fieldToken }).
					AddFunctionToken(op, func() queryparser.TokenSet {
						if selectorField.IsJSONBCast() && selectorField.Type != String {
							return queryparser.NewTokenSet().AddFunctionToken("CAST", func() queryparser.TokenSet {
								return queryparser.NewTokenSet().Append(fieldToken).AddValueToken(selectorField.Type.String())
							}).Append(valueToken)
						}
						return queryparser.NewTokenSet().Append(fieldToken, valueToken)
					})
			})
		default:
			return queryparser.NewTokenSet().AddFunctionToken(op, func() queryparser.TokenSet {
				if selectorField.IsJSONBCast() && selectorField.Type != String {
					return queryparser.NewTokenSet().AddFunctionToken("CAST", func() queryparser.TokenSet {
						return queryparser.NewTokenSet().Append(fieldToken).AddValueToken(selectorField.Type.String())
					}).Append(valueToken)
				}
				return queryparser.NewTokenSet().Append(fieldToken, valueToken)
			})
		}
	})
}

var fieldRegex = regexp.MustCompile(`^[A-Za-z0-9][-A-Za-z0-9_.*\[\]0-9]*[A-Za-z0-9\]]$`)

func (fs *FieldSelector) resolveField(selectorField *SelectorField, resolve resolverFunc[string]) (queryparser.TokenSet, error) {
	if _, ok := selectorField.Options["private"]; ok && !fs.privateSelectors {
		return nil, fmt.Errorf("field is marked as private and cannot be selected")
	}

	if !fieldRegex.MatchString(selectorField.FieldName) {
		return nil, fmt.Errorf(
			"field must consist of alphanumeric characters, '-', '_', or '.', "+
				"and must start with an alphanumeric character and end with either an alphanumeric character or an array index "+
				"(e.g., 'MyField', 'my.field', '123-abc', or 'arrayField[0]'); "+
				"regex used for validation is '%s'",
			fieldRegex.String())
	}

	if selectorField.FieldType == "jsonb" {
		var params strings.Builder
		parts := strings.Split(selectorField.FieldName, ".")
		params.WriteString(parts[0])

		for i, part := range parts[1:] {
			// Handle array indexing in JSONB fields if applicable
			if openBracketIdx, closeBracketIdx := strings.Index(part, "["), strings.Index(part, "]"); openBracketIdx > -1 || closeBracketIdx > -1 {
				if !arrayPattern.MatchString(part) {
					return nil, fmt.Errorf(
						"array access must specify a valid index (e.g., 'conditions[0]'); invalid part: %s", part)
				}
				// Parse the array field and index
				arrayKey := part[:openBracketIdx]
				arrayIndex := part[openBracketIdx+1 : len(part)-1]

				params.WriteString(" -> '")
				params.WriteString(arrayKey)
				params.WriteString("'")

				// Use '->>' if casting to text is needed for the final part
				if i == len(parts[1:])-1 && selectorField.IsJSONBCast() {
					params.WriteString(" ->> ")
				} else {
					params.WriteString(" -> ")
				}
				params.WriteString(arrayIndex)
			} else {
				// Handle regular JSON key access
				if i == len(parts[1:])-1 && selectorField.IsJSONBCast() {
					params.WriteString(" ->> '")
				} else {
					params.WriteString(" -> '")
				}
				params.WriteString(part)
				params.WriteString("'")
			}
		}
		return resolve(params.String()), nil
	}

	// For non-JSONB fields, directly use the FieldName
	return resolve(selectorField.FieldName), nil
}

func (fs *FieldSelector) resolveValue(
	operator selection.Operator,
	selectorField *SelectorField,
	value string,
	resolve resolverFunc[any],
) (queryparser.TokenSet, error) {
	switch selectorField.Type {
	case Int, IntArray:
		v, err := strconv.Atoi(value)
		if err != nil {
			return nil, fmt.Errorf("failed to parse integer value: %w", err)
		}
		return resolve(v), nil

	case SmallInt, SmallIntArray:
		v, err := strconv.ParseInt(value, 10, 16)
		if err != nil {
			return nil, fmt.Errorf("failed to parse small integer value: %w", err)
		}
		if v < math.MinInt16 || v > math.MaxInt16 {
			return nil, fmt.Errorf("value out of range for int16: %d", v)
		}
		return resolve(int16(v)), nil

	case BigInt, BigIntArray:
		v, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("failed to parse big integer value: %w", err)
		}
		return resolve(v), nil

	case Float, FloatArray:
		v, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return nil, fmt.Errorf("failed to parse float value: %w", err)
		}
		return resolve(v), nil

	case Bool, BoolArray:
		v, err := strconv.ParseBool(value)
		if err != nil {
			return nil, fmt.Errorf("failed to parse boolean value: %w", err)
		}
		return resolve(v), nil

	case Timestamp, TimestampArray:
		v, err := time.Parse(time.RFC3339, value)
		if err != nil {
			return nil, fmt.Errorf("failed to parse timestamp value: %w", err)
		}
		return resolve(v), nil

	case String, TextArray:
		if !selectorField.IsJSONBCast() && selectorField.Type == String &&
			(operator == selection.Contains || operator == selection.NotContains) {

			if strings.Contains(value, "%") {
				return nil, fmt.Errorf("partial match strings cannot contain '%%' characters")
			}
			return resolve("%" + value + "%"), nil
		}
		return resolve(value), nil

	case Jsonb:
		if !json.Valid([]byte(value)) {
			return nil, fmt.Errorf("failed to parse JSON value %q", value)
		}
		return resolve(value), nil

	default:
		return nil, fmt.Errorf("unknown type")
	}
}

func (fs *FieldSelector) resolveQuery(operator selection.Operator, selectorField *SelectorField, resolve resolverFunc[string]) (queryparser.TokenSet, error) {
	_, exists := operatorsMap[operator]
	if !exists {
		return nil, fmt.Errorf("unknown operator %q", operator)
	}

	switch selectorField.Type {
	case Int, Float, SmallInt, BigInt:
		return fs.applyNumbersOperator(operator, resolve)
	case Bool:
		return fs.applyBooleanOperator(operator, resolve)
	case Timestamp:
		return fs.applyTimestampOperator(operator, resolve)
	case IntArray, SmallIntArray, BigIntArray, FloatArray, BoolArray, TimestampArray, TextArray:
		return fs.applyArrayOperator(operator, resolve)
	case Jsonb:
		return fs.applyJsonbOperator(operator, resolve)
	case String:
		return fs.applyStringOperator(operator, selectorField, resolve)
	default:
		return nil, fmt.Errorf("unsupported type %q for operator %q", selectorField.Type.String(), operator)
	}
}

// applyArrayOperator applies the appropriate operator for array fields.
func (fs *FieldSelector) applyArrayOperator(operator selection.Operator, resolve resolverFunc[string]) (queryparser.TokenSet, error) {
	switch operator {
	case selection.Contains:
		return resolve("CONTAINS"), nil
	case selection.NotContains:
		return resolve("NOTCONTAINS"), nil
	case selection.In:
		return resolve("OVERLAPS"), nil
	case selection.NotIn:
		return resolve("NOTOVERLAPS"), nil
	case selection.Exists, selection.DoesNotExist:
		return resolve(operatorsMap[operator]), nil
	default:
		return nil, fmt.Errorf("operator %q is unsupported for type array", operator)
	}
}

// applyTimestampOperator applies the appropriate operator for timestamp fields.
func (fs *FieldSelector) applyTimestampOperator(operator selection.Operator, resolve resolverFunc[string]) (queryparser.TokenSet, error) {
	switch operator {
	case selection.Equals, selection.DoubleEquals, selection.NotEquals, selection.GreaterThan,
		selection.GreaterThanOrEquals, selection.LessThan, selection.LessThanOrEquals,
		selection.In, selection.NotIn, selection.Exists, selection.DoesNotExist:
		return resolve(operatorsMap[operator]), nil
	default:
		return nil, fmt.Errorf("operator %q is unsupported for type timestamp", operator)
	}
}

// applyNumbersOperator applies the appropriate operator for numbers fields.
func (fs *FieldSelector) applyNumbersOperator(operator selection.Operator, resolve resolverFunc[string]) (queryparser.TokenSet, error) {
	switch operator {
	case selection.Equals, selection.DoubleEquals, selection.NotEquals, selection.GreaterThan,
		selection.GreaterThanOrEquals, selection.LessThan, selection.LessThanOrEquals,
		selection.In, selection.NotIn, selection.Exists, selection.DoesNotExist:
		return resolve(operatorsMap[operator]), nil
	default:
		return nil, fmt.Errorf("operator %q is unsupported for type number", operator)
	}
}

// applyBooleanOperator applies the appropriate operator for boolean fields.
func (fs *FieldSelector) applyBooleanOperator(operator selection.Operator, resolve resolverFunc[string]) (queryparser.TokenSet, error) {
	switch operator {
	case selection.Equals, selection.DoubleEquals, selection.NotEquals, selection.In, selection.NotIn,
		selection.Exists, selection.DoesNotExist:
		return resolve(operatorsMap[operator]), nil
	default:
		return nil, fmt.Errorf("operator %q is unsupported for type boolean", operator)
	}
}

// applyJsonbOperator applies the appropriate operator for JSONB fields.
func (fs *FieldSelector) applyJsonbOperator(operator selection.Operator, resolve resolverFunc[string]) (queryparser.TokenSet, error) {
	switch operator {
	case selection.Equals, selection.DoubleEquals, selection.NotEquals,
		selection.Exists, selection.DoesNotExist:
		return resolve(operatorsMap[operator]), nil
	case selection.Contains:
		return resolve("JSONB_CONTAINS"), nil
	case selection.NotContains:
		return resolve("JSONB_NOTCONTAINS"), nil
	default:
		return nil, fmt.Errorf("operator %q is unsupported for type JSONB", operator)
	}
}

// applyStringOperator applies the appropriate operator for text fields.
func (fs *FieldSelector) applyStringOperator(operator selection.Operator, selectorField *SelectorField, resolve resolverFunc[string]) (queryparser.TokenSet, error) {
	switch operator {
	case selection.Equals, selection.DoubleEquals, selection.NotEquals, selection.In, selection.NotIn,
		selection.Exists, selection.DoesNotExist:
		return resolve(operatorsMap[operator]), nil
	case selection.Contains, selection.NotContains:
		if selectorField.IsJSONBCast() {
			return nil, fmt.Errorf("the operator %q is not supported for partial string matching when the field is of type JSONB with string casting", operator)
		}
		if selectorField.IsArrayElement() {
			return nil, fmt.Errorf("the operator %q is not supported for partial string matching when the selector is an element within an array", operator)
		}
		return resolve(operatorsMap[operator]), nil
	default:
		return nil, fmt.Errorf("operator %q is unsupported for type string", operator)
	}
}

// This function was overridden to pass the column name verification of the infrastructure.
// It is safe since we have already performed all the checks before calling this function.
func (fs *FieldSelector) queryField(args ...string) (*sql.FunctionResult, error) {
	if len(args) != 1 {
		return nil, fmt.Errorf("expected one argument")
	}

	return &sql.FunctionResult{
		Query: args[0],
	}, nil
}

// resolveSelectorField attempts to resolve a field using both visible and hidden selectors.
func (fs *FieldSelector) resolveSelectorField(key string) ([]*SelectorField, error) {
	resolvedFields, err := fs.fieldResolver.ResolveFields(NewSelectorName(key))
	if err != nil {
		// Fallback to resolving as a hidden selector
		return fs.fieldResolver.ResolveFields(NewHiddenSelectorName(key))
	}
	return resolvedFields, nil
}
