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

// NewLabelSelectorFromMapOrDie creates a LabelSelector from a map of labels.
// It panics if the creation fails.
//
// Example:
//
//	labels := map[string]string{"env": "prod", "tier": "backend"}
//	selector := NewLabelSelectorFromMapOrDie(labels)
//	// selector represents: "env=prod,tier=backend"
func NewLabelSelectorFromMapOrDie(labels map[string]string) *LabelSelector {
	ls, err := NewLabelSelectorFromMap(labels)
	if err != nil {
		panic(err)
	}
	return ls
}

// NewLabelSelectorFromMap creates a LabelSelector from a map of labels.
//
// Example:
//
//	labels := map[string]string{"env": "prod", "tier": "backend"}
//	selector, err := NewLabelSelectorFromMap(labels)
//	// selector represents: "env=prod,tier=backend"
func NewLabelSelectorFromMap(labels map[string]string) (*LabelSelector, error) {
	if len(labels) == 0 {
		return NewLabelSelector("")
	}

	var parts []string
	for key, val := range labels {
		parts = append(parts, key+string(selection.Equals)+val)
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
// The method uses a provided resolver to determine the correct labels field.
//
// Parameters:
//
//	ctx      - The context for managing operation lifecycle.
//	name     - The selector name to resolve the labels field.
//	resolver - A Resolver instance used to resolve the selector fields.
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
//	query, args, err := ls.Parse(ctx, "labels", myResolver)
//	if err != nil {
//	    log.Fatalf("Failed to parse label selector: %v", err)
//	}
//	fmt.Printf("Query: %s, Args: %v\n", query, args)
func (ls *LabelSelector) Parse(ctx context.Context, name SelectorName, resolver Resolver) (string, []any, error) {
	if resolver == nil {
		return "", nil, NewSelectorError(flterrors.ErrLabelSelectorParseFailed,
			fmt.Errorf("resolver is not defined"))
	}

	// Resolve selector fields using the provided resolver
	resolvedFields, err := resolver.ResolveFields(name)
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
		if IsSelectorError(err) {
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

		reTokens, err := ls.parseRequirement(req.Key(), req.Values(), req.Operator())
		if err != nil {
			return nil, NewSelectorError(flterrors.ErrLabelSelectorParseFailed,
				fmt.Errorf("failed to resolve operation for key %q: %w", req.Key(), err))
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
func (ls *LabelSelector) parseRequirement(key selector.Tuple, values []selector.Tuple, operator selection.Operator) (queryparser.TokenSet, error) {
	// Create a token for the field
	fieldToken := queryparser.NewTokenSet().AddFunctionToken("K", func() queryparser.TokenSet {
		return queryparser.NewTokenSet().AddValueToken(ls.field.FieldName)
	})

	// Create an EXISTS token for the key(s)
	existsToken := queryparser.NewTokenSet()
	if len(key) > 1 {
		existsToken = existsToken.AddFunctionToken("ALLEXISTS", func() queryparser.TokenSet {
			ts := queryparser.NewTokenSet()
			for _, k := range key {
				ts = ts.AddFunctionToken("V", func() queryparser.TokenSet {
					return queryparser.NewTokenSet().AddValueToken(k)
				})
			}
			return fieldToken.Append(ts)
		})
	} else {
		existsToken = existsToken.AddFunctionToken("EXISTS", func() queryparser.TokenSet {
			return fieldToken.AddFunctionToken("V", func() queryparser.TokenSet {
				return queryparser.NewTokenSet().AddValueToken(key[0])
			})
		})
	}

	// Helper to wrap tokens with ISNULL and NOT logic
	addISNULLAndNot := func(tokens queryparser.TokenSet) queryparser.TokenSet {
		return queryparser.NewTokenSet().AddFunctionToken("OR", func() queryparser.TokenSet {
			return queryparser.NewTokenSet().
				AddFunctionToken("ISNULL", func() queryparser.TokenSet {
					return fieldToken
				}).
				AddFunctionToken("NOT", func() queryparser.TokenSet {
					return tokens
				})
		})
	}

	// Operator-specific token generation
	switch operator {
	case selection.Exists:
		return existsToken, nil

	case selection.DoesNotExist:
		return addISNULLAndNot(existsToken), nil

	case selection.Equals, selection.DoubleEquals, selection.NotEquals, selection.In, selection.NotIn:
		valuesTokens := queryparser.NewTokenSet()
		for _, val := range values {
			valuesTokens = valuesTokens.Append(queryparser.NewTokenSet().AddFunctionToken("CONTAINS", func() queryparser.TokenSet {
				return queryparser.NewTokenSet().Append(fieldToken, ls.createPairToken(key, val))
			}))
		}

		// Wrap multiple values in an OR token if necessary
		if len(values) > 1 {
			valuesTokens = queryparser.NewTokenSet().AddFunctionToken("OR", func() queryparser.TokenSet {
				return valuesTokens
			})
		}

		// Combine EXISTS and value tokens with an AND token
		tokens := queryparser.NewTokenSet().AddFunctionToken("AND", func() queryparser.TokenSet {
			return existsToken.Append(valuesTokens)
		})

		// Wrap NotEquals and NotIn with NOT and ISNULL logic
		if operator == selection.NotEquals || operator == selection.NotIn {
			tokens = addISNULLAndNot(tokens)
		}

		return tokens, nil

	default:
		return nil, fmt.Errorf("unsupported operator %q for label selector", operator)
	}
}

// createPairToken generates a token for a key-value pair in the label selector.
func (ls *LabelSelector) createPairToken(key selector.Tuple, value selector.Tuple) queryparser.TokenSet {
	// Create JSON-like key-value pairs
	pairs := make([]string, len(key))
	for i, k := range key {
		pairs[i] = fmt.Sprintf("\"%s\": \"%s\"", k, value[i])
	}

	// Build and return the token set
	return queryparser.NewTokenSet().AddFunctionToken("V", func() queryparser.TokenSet {
		jsonPairs := fmt.Sprintf("{%s}", strings.Join(pairs, ","))
		return queryparser.NewTokenSet().AddValueToken(jsonPairs)
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
