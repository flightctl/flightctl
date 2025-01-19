package selector

import (
	"context"
	"fmt"
	"strings"

	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/pkg/k8s/selector"
	"github.com/flightctl/flightctl/pkg/k8s/selector/fields"
	"github.com/flightctl/flightctl/pkg/k8s/selector/selection"
	"github.com/flightctl/flightctl/pkg/queryparser"
	"github.com/flightctl/flightctl/pkg/queryparser/sqljsonb"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

type AnnotationSelector struct {
	field    *SelectorField
	parser   queryparser.Parser
	selector selector.Selector
}

// NewAnnotationSelectorFromMapOrDie creates an AnnotationSelector from a map of annotations
// with an optional invert flag. It panics if the creation fais.
//
// Parameters:
//
//	annotations - A map where keys are annotation names and values are annotation values.
//	invert - If true, inverts the operator to "!=" instead of "=".
//
// Example:
//
//	annotations := map[string]string{"env": "prod", "tier": "backend"}
//	selector := NewAnnotationSelectorFromMapOrDie(annotations)
//	// selector represents: "env=prod,tier=backend"
func NewAnnotationSelectorFromMapOrDie(annotations map[string]string, invert bool) *AnnotationSelector {
	ls, err := NewAnnotationSelectorFromMap(annotations, invert)
	if err != nil {
		panic(err)
	}
	return ls
}

// NewAnnotationSelectorFromMap creates an AnnotationSelector from a map of annotations
// with an optional invert flag.
//
// Parameters:
//
//	annotations - A map where keys are annotation names and values are annotation values.
//	invert - If true, inverts the operator to "!=" instead of "=".
//
// Example:
//
//	annotations := map[string]string{"env": "prod", "tier": "backend"}
//	selector, err := NewAnnotationSelectorFromMap(annotations, true)
//	// selector represents: "env!=prod,tier!=backend"
func NewAnnotationSelectorFromMap(annotations map[string]string, invert bool) (*AnnotationSelector, error) {
	if len(annotations) == 0 {
		return NewAnnotationSelector("")
	}

	operator := selection.Equals
	if invert {
		operator = selection.NotEquals
	}

	var parts []string
	for key, val := range annotations {
		parts = append(parts, key+string(operator)+val)
	}

	return NewAnnotationSelector(strings.Join(parts, ","))
}

// NewAnnotationSelectorOrDie creates an AnnotationSelector from a string using Kubernetes-style
// label selector syntax. It panics if the creation fais.
//
// Parameters:
//
//	input - A string representing the annotation selector in Kubernetes syntax.
//
// Example:
//
//	selector := NewAnnotationSelectorOrDie("env=prod,tier=backend")
//	// selector represents: "env=prod,tier=backend"
func NewAnnotationSelectorOrDie(input string) *AnnotationSelector {
	ls, err := NewAnnotationSelector(input)
	if err != nil {
		panic(err)
	}
	return ls
}

// NewAnnotationSelector creates an AnnotationSelector from a string using Kubernetes-style
// label selector syntax.
//
// Parameters:
//
//	input - A string representing the annotation selector in Kubernetes syntax.
//
// Example:
//
//	selector, err := NewAnnotationSelector("env=prod,tier=backend")
//	// selector represents: "env=prod,tier=backend"
func NewAnnotationSelector(input string) (*AnnotationSelector, error) {
	// The Kubernetes package provides validation for fields and labels but not for annotations.
	// Use the field selector parser, as its lexer supports escaping symbols in values.
	selector, err := fields.ParseSelector(input)
	if err != nil {
		return nil, NewSelectorError(flterrors.ErrAnnotationSelectorSyntax, err)
	}

	// Manually validate annotation keys, as annotation-specific validation is not handled by the parser.
	var allErrs field.ErrorList
	requirements, _ := selector.Requirements()
	for _, r := range requirements {
		if len(r.Key()) > 1 {
			allErrs = append(allErrs, field.Invalid(field.ToPath().Child("key"), r.Key(),
				fmt.Sprintf("keysets with multiple keys are not supported: %v", r.Key())))
			continue
		}

		// Convert the key to lowercase before validation.
		lowerKey := strings.ToLower(r.Key().String())
		if errs := validation.IsQualifiedName(lowerKey); len(errs) != 0 {
			allErrs = append(allErrs, field.Invalid(field.ToPath().Child("key"), r.Key(), strings.Join(errs, "; ")))
		}
	}

	// If validation errors exist, aggregate and return them as a single error.
	if err = allErrs.ToAggregate(); err != nil {
		return nil, NewSelectorError(flterrors.ErrAnnotationSelectorSyntax, err)
	}

	// Return the successfully parsed and validated AnnotationSelector.
	return &AnnotationSelector{
		selector: selector,
	}, nil
}

// Parse converts the AnnotationSelector into a SQL query with parameters.
// The method resolves the destination structure (dest) and maps it
// to the annotation field to generate the query.
//
// Parameters:
//
//	ctx   - The context for managing operation lifecycle.
//	dest  - The target object (e.g., database model) providing field definitions.
//	name  - The selector name to resolve the annotation field.
//
// Returns:
//
//	string - The generated SQL query string.
//	[]any  - Parameters to be used with the SQL query.
//	error  - An error if parsing or field resolution fais.
//
// Example:
//
//	ls, _ := NewAnnotationSelector("key1=value1,key2!=value2")
//	query, args, err := s.Parse(ctx, &MyModel{}, "annotations")
//	if err != nil {
//	    log.Fatalf("Failed to parse annotation selector: %v", err)
//	}
//	fmt.Printf("Query: %s, Args: %v\n", query, args)
func (s *AnnotationSelector) Parse(ctx context.Context, dest any, name SelectorName) (string, []any, error) {
	fr, err := SelectorFieldResolver(dest)
	if err != nil {
		return "", nil, NewSelectorError(flterrors.ErrAnnotationSelectorParseFailed, err)
	}

	resolvedFields, err := fr.ResolveFields(name)
	if err != nil {
		return "", nil, NewSelectorError(flterrors.ErrAnnotationSelectorParseFailed, err)
	}

	if len(resolvedFields) > 1 {
		return "", nil, NewSelectorError(flterrors.ErrAnnotationSelectorParseFailed,
			fmt.Errorf("multiple selector fields are not supported for selector name %q", name))
	}

	s.field = resolvedFields[0]
	if s.field.FieldType != "jsonb" {
		return "", nil, NewSelectorError(flterrors.ErrAnnotationSelectorParseFailed,
			fmt.Errorf("selector field %q must be of type jsonb, got %q", s.field.FieldName, s.field.FieldType))
	}

	s.parser, err = sqljsonb.NewSQLParser(
		sqljsonb.WithTokenizer(s),
		sqljsonb.WithOverrideFunction("K", sqljsonb.Wrap(s.queryField)),
	)
	if err != nil {
		return "", nil, NewSelectorError(flterrors.ErrAnnotationSelectorParseFailed, err)
	}

	q, args, err := s.parser.Parse(ctx, s.selector)
	if err != nil {
		if ok := IsSelectorError(err); ok {
			return "", nil, err
		}
		return "", nil, NewSelectorError(flterrors.ErrAnnotationSelectorParseFailed, err)
	}
	return q, args, nil
}

// Tokenize converts a selector string into a set of queryparser tokens.
func (s *AnnotationSelector) Tokenize(ctx context.Context, input any) (queryparser.TokenSet, error) {
	if input == nil {
		return nil, nil
	}

	// Assert that input is a selector
	selector, ok := input.(selector.Selector)
	if !ok {
		return nil, fmt.Errorf("invalid input type: expected AnnotationSelector, got %T", input)
	}

	requirements, selectable := selector.Requirements()
	if !selectable {
		return nil, nil
	}

	tokens := queryparser.NewTokenSet()
	for _, req := range requirements {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		key := req.Key()
		values := req.Values()
		operator := req.Operator()

		reTokens, err := s.parseRequirement(key, values, operator)
		if err != nil {
			return nil, NewSelectorError(flterrors.ErrAnnotationSelectorParseFailed,
				fmt.Errorf("failed to resolve operation for key %q: %w", key, err))
		}
		tokens = tokens.Append(reTokens)
	}

	if len(requirements) > 1 {
		tokens = queryparser.NewTokenSet(len(tokens)+2).AddFunctionToken("AND", func() queryparser.TokenSet {
			return tokens
		})
	}
	return tokens, nil
}

// parseRequirement parses a single annotation requirement into queryparser tokens.
// It resolves the field, operator, and values for the requirement.
func (s *AnnotationSelector) parseRequirement(key selector.Tuple, values []selector.Tuple, operator selection.Operator) (queryparser.TokenSet, error) {
	// Create a token for the field
	fieldToken := queryparser.NewTokenSet().AddFunctionToken("K", func() queryparser.TokenSet {
		return queryparser.NewTokenSet().AddValueToken(s.field.FieldName)
	})

	// Create an EXISTS token for the key
	existsToken := queryparser.NewTokenSet().AddFunctionToken("EXISTS", func() queryparser.TokenSet {
		return queryparser.NewTokenSet().Append(fieldToken, s.createKeyToken(key.String()))
	})

	// Helper to wrap tokens with ISNULL and NOT logic
	addISNULLAndNot := func(tokens queryparser.TokenSet) queryparser.TokenSet {
		return queryparser.NewTokenSet().AddFunctionToken("OR", func() queryparser.TokenSet {
			return queryparser.NewTokenSet().AddFunctionToken("ISNULL", func() queryparser.TokenSet {
				return fieldToken
			}).AddFunctionToken("NOT", func() queryparser.TokenSet {
				return tokens
			})
		})
	}

	switch operator {
	case selection.Exists:
		// Return the EXISTS token for the "Exists" operator
		return existsToken, nil

	case selection.DoesNotExist:
		// Wrap the EXISTS token in a NOT token and add an ISNULL check
		return addISNULLAndNot(existsToken), nil

	case selection.Equals, selection.DoubleEquals, selection.NotEquals, selection.In, selection.NotIn:
		// Create tokens for values using the "CONTAINS" function
		valuesTokens := queryparser.NewTokenSet()
		for _, val := range values {
			valuesTokens = valuesTokens.Append(queryparser.NewTokenSet().AddFunctionToken("CONTAINS", func() queryparser.TokenSet {
				return queryparser.NewTokenSet().Append(fieldToken, s.createPairToken(key.String(), val.String()))
			}))
		}

		// If there are multiple values, wrap them in an OR token
		if len(values) > 1 {
			valuesTokens = queryparser.NewTokenSet().AddFunctionToken("OR", func() queryparser.TokenSet {
				return valuesTokens
			})
		}

		// Combine EXISTS and value tokens with an AND token
		tokens := queryparser.NewTokenSet().AddFunctionToken("AND", func() queryparser.TokenSet {
			return existsToken.Append(valuesTokens)
		})

		// Wrap the "NotEquals" and "NotIn" operators in a NOT token and add an ISNULL check
		if operator == selection.NotEquals || operator == selection.NotIn {
			tokens = addISNULLAndNot(tokens)
		}

		return tokens, nil

	default:
		return nil, fmt.Errorf("unsupported operator %q for annotation selector", operator)
	}
}

// createKeyToken generates a token for a key in the annotation selector.
func (s *AnnotationSelector) createKeyToken(key string) queryparser.TokenSet {
	return queryparser.NewTokenSet().AddFunctionToken("V", func() queryparser.TokenSet {
		return queryparser.NewTokenSet().AddValueToken(key)
	})
}

// createPairToken generates a token for a key-value pair in the annotation selector.
func (s *AnnotationSelector) createPairToken(key, value string) queryparser.TokenSet {
	return queryparser.NewTokenSet().AddFunctionToken("V", func() queryparser.TokenSet {
		return queryparser.NewTokenSet().AddValueToken(fmt.Sprintf("{\"%s\": \"%s\"}", key, value))
	})
}

// This function was overridden to pass the column name verification of the infrastructure.
// It is safe since we have already performed all the checks before calling this function.
func (s *AnnotationSelector) queryField(args ...string) (*sqljsonb.FunctionResult, error) {
	if len(args) != 1 {
		return nil, fmt.Errorf("expected one argument")
	}

	return &sqljsonb.FunctionResult{
		Query: args[0],
	}, nil
}
