package selector

import (
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

type compositeResolver struct {
	table    string
	resolver Resolver
}

// CompositeSelectorResolver combines multiple resolvers to support multiple models
type CompositeSelectorResolver struct {
	resolvers []compositeResolver
}

// NewCompositeSelectorResolver initializes a resolver that can handle multiple models
func NewCompositeSelectorResolver(dest ...any) (*CompositeSelectorResolver, error) {
	resolvers := make([]compositeResolver, 0, len(dest))

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

		resolvers = append(resolvers, compositeResolver{schema.Table, fs})
	}

	return &CompositeSelectorResolver{resolvers: resolvers}, nil
}

// ResolveNames retrieves field names from all resolvers and prefixes them with the table name
func (r *CompositeSelectorResolver) ResolveNames(name SelectorName) ([]string, error) {
	var fields []string

	for _, cr := range r.resolvers {
		names, err := cr.resolver.ResolveNames(name)
		if err != nil {
			return nil, err
		}
		for _, n := range names {
			fields = append(fields, cr.table+"."+n)
		}
	}

	return fields, nil
}

// ResolveFields retrieves field metadata from resolvers, prefixing them with the table name
func (r *CompositeSelectorResolver) ResolveFields(name SelectorName) ([]*SelectorField, error) {
	for _, cr := range r.resolvers {
		fields, err := cr.resolver.ResolveFields(name)
		if err != nil {
			return nil, err
		}
		if len(fields) > 0 {
			for i := range fields {
				fields[i].FieldName = cr.table + "." + fields[i].FieldName
			}
			return fields, nil
		}
	}

	return []*SelectorField{}, nil
}

// List aggregates all selector names from the registered resolvers
func (r *CompositeSelectorResolver) List() []SelectorName {
	set := NewSelectorFieldNameSet()

	for _, cr := range r.resolvers {
		set.Add(cr.resolver.List()...)
	}

	list := set.List()
	sort.Slice(list, func(i, j int) bool {
		return list[i].String() < list[j].String()
	})

	return list
}
