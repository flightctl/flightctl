package selector

import (
	"fmt"
	"sort"

	gormschema "gorm.io/gorm/schema"
)

// EmptyResolver provides a no-op implementation of the Resolver
type EmptyResolver struct{}

// ResolveNames returns an empty slice of field names
func (r EmptyResolver) ResolveNames(name SelectorName) ([]string, error) {
	return []string{}, nil
}

// ResolveFields returns an empty slice of selector fields
func (r EmptyResolver) ResolveFields(name SelectorName) ([]*SelectorField, error) {
	return []*SelectorField{}, nil
}

// List returns an empty slice of selector names
func (r EmptyResolver) List() []SelectorName {
	return []SelectorName{}
}

// CompositeSelectorResolver combines multiple resolvers to support multiple models
type CompositeSelectorResolver struct {
	resolvers map[string]Resolver
}

// NewCompositeSelectorResolver initializes a resolver that can handle multiple models
func NewCompositeSelectorResolver(dest ...any) (*CompositeSelectorResolver, error) {
	resolvers := make(map[string]Resolver)

	for _, model := range dest {
		// Parse schema to retrieve the table name
		schema, err := gormschema.ParseWithSpecialTableName(
			model, cacheStore, gormschema.NamingStrategy{IdentifierMaxLength: 64}, "",
		)
		if err != nil {
			return nil, err
		}

		// Get field resolver
		fs, err := SelectorFieldResolver(model)
		if err != nil {
			return nil, err
		}

		resolvers[schema.Table] = fs
	}

	return &CompositeSelectorResolver{resolvers: resolvers}, nil
}

// ResolveNames retrieves field names from all resolvers and prefixes them with the table name
func (r *CompositeSelectorResolver) ResolveNames(name SelectorName) ([]string, error) {
	var fields []string

	for table, resolver := range r.resolvers {
		names, err := resolver.ResolveNames(name)
		if err != nil {
			return nil, err
		}
		for _, n := range names {
			fields = append(fields, fmt.Sprintf("%s.%s", table, n))
		}
	}

	return fields, nil
}

// ResolveFields retrieves field metadata from resolvers, prefixing them with the table name
func (r *CompositeSelectorResolver) ResolveFields(name SelectorName) ([]*SelectorField, error) {
	for table, resolver := range r.resolvers {
		fields, err := resolver.ResolveFields(name)
		if err != nil {
			return nil, err
		}
		if len(fields) > 0 {
			for _, field := range fields {
				field.FieldName = fmt.Sprintf("%s.%s", table, field.FieldName)
			}
			return fields, nil
		}
	}

	return []*SelectorField{}, nil
}

// List aggregates all selector names from the registered resolvers
func (r *CompositeSelectorResolver) List() []SelectorName {
	set := NewSelectorFieldNameSet()

	for _, resolver := range r.resolvers {
		set.Add(resolver.List()...)
	}

	list := set.List()
	sort.Slice(list, func(i, j int) bool {
		return list[i].String() < list[j].String()
	})

	return list
}
