package selector

import (
	"context"
	"fmt"
	"strings"

	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/pkg/k8s/selector"
	"github.com/flightctl/flightctl/pkg/k8s/selector/labels"
	"github.com/flightctl/flightctl/pkg/k8s/selector/selection"
	"github.com/flightctl/flightctl/pkg/queryparser"
	"github.com/flightctl/flightctl/pkg/queryparser/sqljsonb"
)

type LabelSelector struct {
	field    *SelectorField
	parser   queryparser.Parser
	selector selector.Selector
}

// NewLabelSelectorFromMapOrDie creates a LabelSelector from a map of labels
// with an optional invert flag. It panics if the creation fails.
//
// Parameters:
//
//	labels - A map where keys are label names and values are label values.
//	invert - If true, inverts the operator to "!=" instead of "=".
//
// Example:
//
//	labels := map[string]string{"env": "prod", "tier": "backend"}
//	selector := NewLabelSelectorFromMapOrDie(labels)
//	// selector represents: "env=prod,tier=backend"
func NewLabelSelectorFromMapOrDie(labels map[string]string, invert bool) *LabelSelector {
	ls, err := NewLabelSelectorFromMap(labels, invert)
	if err != nil {
		panic(err)
	}
	return ls
}

// NewLabelSelectorFromMap creates a LabelSelector from a map of labels
// with an optional invert flag.
//
// Parameters:
//
//	labels - A map where keys are label names and values are label values.
//	invert - If true, inverts the operator to "!=" instead of "=".
//
// Example:
//
//	labels := map[string]string{"env": "prod", "tier": "backend"}
//	selector, err := NewLabelSelectorFromMap(labels, true)
//	// selector represents: "env!=prod,tier!=backend"
func NewLabelSelectorFromMap(labels map[string]string, invert bool) (*LabelSelector, error) {
	if len(labels) == 0 {
		return NewLabelSelector("")
	}

	operator := selection.Equals
	if invert {
		operator = selection.NotEquals
	}

	var parts []string
	for key, val := range labels {
		parts = append(parts, key+string(operator)+val)
	}

	return NewLabelSelector(strings.Join(parts, ","))
}

// NewLabelSelectorOrDie creates a LabelSelector from a string using Kubernetes-style
// label selector syntax. It panics if the creation fails.
//
// Parameters:
//
//	input - A string representing the label selector in Kubernetes syntax.
//
// Example:
//
//	selector := NewLabelSelectorOrDie("env=prod,tier=backend")
//	// selector represents: "env=prod,tier=backend"
func NewLabelSelectorOrDie(input string) *LabelSelector {
	ls, err := NewLabelSelector(input)
	if err != nil {
		panic(err)
	}
	return ls
}

// NewLabelSelector creates a LabelSelector from a string using Kubernetes-style
// label selector syntax.
//
// Parameters:
//
//	input - A string representing the label selector in Kubernetes syntax.
//
// Example:
//
//	selector, err := NewLabelSelector("env=prod,tier=backend")
//	// selector represents: "env=prod,tier=backend"
func NewLabelSelector(input string) (*LabelSelector, error) {
	selector, err := labels.Parse(input)
	if err != nil {
		return nil, NewSelectorError(flterrors.ErrLabelSelectorSyntax, err)
	}

	return &LabelSelector{
		selector: selector,
	}, nil
}

// Parse converts the LabelSelector into a SQL query with parameters.
// The method resolves the destination structure (dest) and maps it
// to the label field to generate the query.
//
// Parameters:
//
//	ctx   - The context for managing operation lifecycle.
//	dest  - The target object (e.g., database model) providing field definitions.
//	name  - The selector name to resolve the label field.
//
// Returns:
//
//	string - The generated SQL query string.
//	[]any  - Parameters to be used with the SQL query.
//	error  - An error if parsing or field resolution fails.
//
// Example:
//
//	ls, _ := NewLabelSelector("key1=value1,key2!=value2")
//	query, args, err := ls.Parse(ctx, &MyModel{}, "labels")
//	if err != nil {
//	    log.Fatalf("Failed to parse label selector: %v", err)
//	}
//	fmt.Printf("Query: %s, Args: %v\n", query, args)
func (ls *LabelSelector) Parse(ctx context.Context, dest any, name SelectorName) (string, []any, error) {
	fr, err := SelectorFieldResolver(dest)
	if err != nil {
		return "", nil, NewSelectorError(flterrors.ErrLabelSelectorParseFailed, err)
	}

	resolvedFields, err := fr.ResolveFields(name)
	if err != nil {
		return "", nil, NewSelectorError(flterrors.ErrLabelSelectorParseFailed, err)
	}

	if len(resolvedFields) > 1 {
		return "", nil, NewSelectorError(flterrors.ErrLabelSelectorParseFailed,
			fmt.Errorf("multiple selector fields are not supported for selector name %q", name))
	}

	ls.field = resolvedFields[0]
	if ls.field.FieldType != "jsonb" {
		return "", nil, NewSelectorError(flterrors.ErrLabelSelectorParseFailed,
			fmt.Errorf("selector field %q must be of type jsonb, got %q", ls.field.FieldName, ls.field.FieldType))
	}

	ls.parser, err = sqljsonb.NewSQLParser(
		sqljsonb.WithTokenizer(ls),
		sqljsonb.WithOverrideFunction("K", sqljsonb.Wrap(ls.queryField)),
	)
	if err != nil {
		return "", nil, NewSelectorError(flterrors.ErrLabelSelectorParseFailed, err)
	}

	q, args, err := ls.parser.Parse(ctx, ls.selector)
	if err != nil {
		if ok := IsSelectorError(err); ok {
			return "", nil, err
		}
		return "", nil, NewSelectorError(flterrors.ErrLabelSelectorParseFailed, err)
	}
	return q, args, nil
}

// Tokenize converts a selector string into a set of queryparser tokens.
func (ls *LabelSelector) Tokenize(ctx context.Context, input any) (queryparser.TokenSet, error) {
	if input == nil {
		return nil, nil
	}

	if ls.field.FieldType != "jsonb" {
		return nil, fmt.Errorf("selector field %q must be of type jsonb, got %q", ls.field.FieldName, ls.field.FieldType)
	}

	// Assert that input is a selector
	selector, ok := input.(selector.Selector)
	if !ok {
		return nil, fmt.Errorf("invalid input type: expected labelSelector, got %T", input)
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

		key := strings.TrimSpace(req.Key())
		values := req.Values().List()
		operator := req.Operator()

		reTokens, err := ls.parseRequirement(key, values, operator)
		if err != nil {
			return nil, NewSelectorError(flterrors.ErrLabelSelectorParseFailed,
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

// parseRequirement parses a single label requirement into queryparser tokens.
// It resolves the field, operator, and values for the requirement.
func (ls *LabelSelector) parseRequirement(key string, values []string, operator selection.Operator) (queryparser.TokenSet, error) {
	// Create a token for the field
	fieldToken := queryparser.NewTokenSet().AddFunctionToken("K", func() queryparser.TokenSet {
		return queryparser.NewTokenSet().AddValueToken(ls.field.FieldName)
	})

	// Create an EXISTS token for the key
	existsToken := queryparser.NewTokenSet().AddFunctionToken("EXISTS", func() queryparser.TokenSet {
		return queryparser.NewTokenSet().Append(fieldToken, ls.createKeyToken(key))
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
				return queryparser.NewTokenSet().Append(fieldToken, ls.createPairToken(key, val))
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
		return nil, fmt.Errorf("unsupported operator %q for label selector", operator)
	}
}

// createKeyToken generates a token for a key in the label selector.
func (ls *LabelSelector) createKeyToken(key string) queryparser.TokenSet {
	return queryparser.NewTokenSet().AddFunctionToken("V", func() queryparser.TokenSet {
		return queryparser.NewTokenSet().AddValueToken(key)
	})
}

// createPairToken generates a token for a key-value pair in the label selector.
func (ls *LabelSelector) createPairToken(key, value string) queryparser.TokenSet {
	return queryparser.NewTokenSet().AddFunctionToken("V", func() queryparser.TokenSet {
		return queryparser.NewTokenSet().AddValueToken(fmt.Sprintf("{\"%s\": \"%s\"}", key, value))
	})
}

// This function was overridden to pass the column name verification of the infrastructure.
// It is safe since we have already performed all the checks before calling this function.
func (ls *LabelSelector) queryField(args ...string) (*sqljsonb.FunctionResult, error) {
	if len(args) != 1 {
		return nil, fmt.Errorf("expected one argument")
	}

	return &sqljsonb.FunctionResult{
		Query: args[0],
	}, nil
}
