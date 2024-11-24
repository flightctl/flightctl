package selector

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/pkg/k8s/selector/selection"
	"github.com/flightctl/flightctl/pkg/queryparser"
	"github.com/flightctl/flightctl/pkg/queryparser/sql"
)

// SortOrder defines the possible sorting orders for a SortRequirement.
type SortOrder string

const (
	// Ascending represents sorting in ascending order (e.g., A-Z, 1-10).
	Ascending SortOrder = "Asc"

	// Descending represents sorting in descending order (e.g., Z-A, 10-1).
	Descending SortOrder = "Desc"
)

type SortRequirement interface {
	// By specifies the field or selector name to sort by.
	By() SelectorName

	// Value represents the value used for comparison.
	Value() any

	// Order specifies the sort order (ascending or descending).
	Order() SortOrder
}

type sortSelector struct {
	parser        queryparser.Parser
	fieldResolver *selectorFieldResolver
}

// NewSortSelector initializes a sortSelector instance.
func NewSortSelector(dest any) (*sortSelector, error) {
	s := &sortSelector{}

	var err error
	s.fieldResolver, err = SelectorFieldResolver(dest)
	if err != nil {
		return nil, err
	}

	s.parser, err = sql.NewSQLParser(
		sql.WithTokenizer(s),
		sql.WithOverrideFunction("K", sql.Wrap(s.queryField)),
	)
	if err != nil {
		return nil, err
	}

	return s, nil
}

// Parse parses the selector and returns a SQL query with parameters.
func (s *sortSelector) Parse(ctx context.Context, requirements ...SortRequirement) (string, []any, error) {
	q, args, err := s.parser.Parse(ctx, requirements)
	if err != nil {
		if ok := IsSelectorError(err); ok {
			return "", nil, err
		}
		return "", nil, NewSelectorError(flterrors.ErrFieldSelectorParseFailed, err)
	}
	return q, args, nil
}

type conditionItem struct {
	field queryparser.TokenSet
	value queryparser.TokenSet
	op    selection.Operator
	sf    *SelectorField
}

// Tokenize converts a selector string into a set of queryparser tokens.
func (fs *sortSelector) Tokenize(ctx context.Context, input any) (queryparser.TokenSet, error) {
	if input == nil {
		return nil, nil
	}

	requirements, ok := input.([]SortRequirement)
	if !ok {
		return nil, fmt.Errorf("invalid input type: expected []SortRequirement, got %T", input)
	}

	tokens := make(queryparser.TokenSet, 0)
	conditions := make([]conditionItem, 0, len(requirements))
	for _, req := range requirements {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		name, value, order := req.By(), req.Value(), req.Order()
		resolvedFields, err := fs.fieldResolver.ResolveFields(name)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve fields for selector %q: %w", name, err)
		}

		for _, resolvedField := range resolvedFields {
			if resolvedField.IsJSONBCast() && resolvedField.Type.IsArray() {
				return nil, fmt.Errorf("cannot cast JSONB to an array of type %q; array casting from JSONB is unsupported", resolvedField.Type.String())
			}

			fieldToken, err := fs.createFieldToken(resolvedField)
			if err != nil {
				return nil, NewSelectorError(flterrors.ErrFieldSelectorParseFailed,
					fmt.Errorf("failed to create field token for selector %q: %w", name, err))
			}

			var valueToken queryparser.TokenSet
			var operator selection.Operator
			if value != nil {
				valueToken, err = fs.createValueToken(resolvedField, value)
				if err != nil {
					return nil, NewSelectorError(flterrors.ErrFieldSelectorParseFailed,
						fmt.Errorf("failed to create value token for selector %q: %w", name, err))
				}

				if order == Ascending {
					operator = selection.GreaterThan
				} else if order == Descending {
					operator = selection.LessThan
				} else {
					return nil, NewSelectorError(flterrors.ErrFieldSelectorParseFailed,
						fmt.Errorf("unknown order %q", order))
				}
			} else {
				operator = selection.Exists
			}
			conditions = append(conditions, conditionItem{fieldToken, valueToken, operator, resolvedField})
		}
	}

	for idx := range conditions {
		tokens = tokens.Append(fs.resolveQuery(conditions[:idx+1]))
	}

	if len(conditions) > 1 {
		tokens = queryparser.NewTokenSet(len(tokens)+2).AddFunctionToken("OR", func() queryparser.TokenSet {
			return tokens
		})
	}
	return tokens, nil
}

func (fs *sortSelector) createFieldToken(selectorField *SelectorField) (queryparser.TokenSet, error) {
	return fs.resolveField(selectorField, func(f string) queryparser.TokenSet {
		return queryparser.NewTokenSet().AddFunctionToken("K", func() queryparser.TokenSet {
			return queryparser.NewTokenSet().AddValueToken(f)
		})
	})
}

func (fs *sortSelector) createValueToken(selectorField *SelectorField, value any) (queryparser.TokenSet, error) {
	return fs.resolveValue(selectorField, value, func(v any) queryparser.TokenSet {
		return queryparser.NewTokenSet().AddFunctionToken("V", func() queryparser.TokenSet {
			return queryparser.NewTokenSet().AddValueToken(v)
		})
	})
}

func (fs *sortSelector) resolveField(selectorField *SelectorField, resolve resolverFunc[string]) (queryparser.TokenSet, error) {
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

func (fs *sortSelector) resolveValue(
	selectorField *SelectorField,
	value any,
	resolve resolverFunc[any],
) (queryparser.TokenSet, error) {
	refVal := reflect.ValueOf(value)
	if refVal.Kind() == reflect.Ptr {
		if refVal.IsNil() {
			return nil, nil
		}
		value = refVal.Elem().Interface()
	}

	if valueStr, ok := value.(string); ok {
		return fs.resolveValueString(selectorField, valueStr, resolve)
	}

	switch selectorField.Type {
	case Int, IntArray:
		v, ok := value.(int)
		if !ok {
			return nil, fmt.Errorf("expected int value, got %T", value)
		}
		return resolve(v), nil

	case SmallInt, SmallIntArray:
		v, ok := value.(int16)
		if !ok {
			return nil, fmt.Errorf("expected int16 value, got %T", value)
		}
		return resolve(v), nil

	case BigInt, BigIntArray:
		v, ok := value.(int64)
		if !ok {
			return nil, fmt.Errorf("expected int64 value, got %T", value)
		}
		return resolve(v), nil

	case Float, FloatArray:
		v, ok := value.(float64)
		if !ok {
			return nil, fmt.Errorf("expected float64 value, got %T", value)
		}
		return resolve(v), nil

	case Bool, BoolArray:
		v, ok := value.(bool)
		if !ok {
			return nil, fmt.Errorf("expected bool value, got %T", value)
		}
		return resolve(v), nil

	case Timestamp, TimestampArray:
		v, ok := value.(time.Time)
		if !ok {
			return nil, fmt.Errorf("expected time.Time value, got %T", value)
		}

		fmt.Println(v.Round(time.Microsecond).Format("2006-01-02 15:04:05.9999999"))
		return resolve(v.Round(time.Microsecond)), nil

	case String, TextArray:
		v, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("expected string value, got %T", value)
		}
		return resolve(v), nil

	case Jsonb:
		v, ok := value.(string) // JSON is usually passed as a string
		if !ok {
			return nil, fmt.Errorf("expected string value for JSON parsing, got %T", value)
		}
		if !json.Valid([]byte(v)) {
			return nil, fmt.Errorf("failed to parse JSON value: %q", v)
		}
		return resolve(v), nil

	default:
		return nil, fmt.Errorf("unknown type: %v", selectorField.Type)
	}
}

func (fs *sortSelector) resolveValueString(
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
		return resolve(v.Round(time.Microsecond)), nil

	case String, TextArray:
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

func (fs *sortSelector) resolveQuery(conditions []conditionItem) queryparser.TokenSet {
	res := queryparser.NewTokenSet()

	// Iterate through the cumulative conditions
	for idx, condition := range conditions {
		// If this is not the last condition, it must be an exact match
		if idx < len(conditions)-1 {
			if condition.value == nil {
				// Handle NULL values with DoesNotExist(ISNULL) for exact matches
				res = res.AddFunctionToken(operatorsMap[selection.DoesNotExist], func() queryparser.TokenSet {
					return queryparser.NewTokenSet().Append(condition.field)
				})
			} else {
				// Handle non-NULL exact match (field = value)
				res = res.AddFunctionToken(operatorsMap[selection.Equals], func() queryparser.TokenSet {
					if condition.sf.IsJSONBCast() && condition.sf.Type != String {
						return queryparser.NewTokenSet().AddFunctionToken("CAST", func() queryparser.TokenSet {
							return queryparser.NewTokenSet().Append(condition.field).AddValueToken("timestamptz")
						}).Append(condition.value)
					}
					return queryparser.NewTokenSet().Append(condition.field, condition.value)
				})
			}
		} else {
			// For the last condition, handle range or Exists(ISNOTNULL) logic
			if condition.value == nil {
				res = res.AddFunctionToken(operatorsMap[selection.Exists], func() queryparser.TokenSet {
					return queryparser.NewTokenSet().Append(condition.field)
				})
			} else {
				// Use the operator defined in the condition (e.g., >, <)
				res = res.AddFunctionToken(operatorsMap[condition.op], func() queryparser.TokenSet {
					if condition.sf.IsJSONBCast() && condition.sf.Type != String {
						return queryparser.NewTokenSet().AddFunctionToken("CAST", func() queryparser.TokenSet {
							return queryparser.NewTokenSet().Append(condition.field).AddValueToken("timestamptz")
						}).Append(condition.value)
					}
					return queryparser.NewTokenSet().Append(condition.field, condition.value)
				})
			}
		}
	}

	// Combine all conditions with AND if there are multiple
	if len(conditions) > 1 {
		return queryparser.NewTokenSet().AddFunctionToken("AND", func() queryparser.TokenSet {
			return res
		})
	}
	return res
}

// This function was overridden to pass the column name verification of the infrastructure.
// It is safe since we have already performed all the checks before calling this function.
func (fs *sortSelector) queryField(args ...string) (*sql.FunctionResult, error) {
	if len(args) != 1 {
		return nil, fmt.Errorf("expected one argument")
	}

	return &sql.FunctionResult{
		Query: args[0],
	}, nil
}
