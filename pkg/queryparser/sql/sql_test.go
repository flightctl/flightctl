package sql

import (
	"context"
	"testing"
)

func TestFuncs(t *testing.T) {
	ctx := context.Background()

	/*
		AND, OR,
		EQ, NEQ,
		LT, LTE,
		GT, GTE,
		IN, NOTIN,
		LIKE, NLIKE,
		ISNULL, ISNOTNULL,
		CONTAINS, NCONTAINS,
		OVERLAPS, NOVERLAPS,
		ANY, NANY,
		ALL, NALL,
		CAST,
		K, V
	*/

	testGoodQueries := map[string][]string{
		"AND(EQ(K(a),V(b)),NEQ(K(c),V(d)))": {"(a = ? AND c != ?)", "b", "d"},
		"OR(EQ(K(a),V(b)),NEQ(K(c),V(d)))":  {"(a = ? OR c != ?)", "b", "d"},
		"LT(K(a),V(1))":                     {"a < ?", "1"},
		"LTE(K(a),V(1))":                    {"a <= ?", "1"},
		"GT(K(a),V(1))":                     {"a > ?", "1"},
		"GTE(K(a),V(1))":                    {"a >= ?", "1"},
		"IN(K(a),V(1),V(2),V(3))":           {"a IN (?, ?, ?)", "1", "2", "3"},
		"NOTIN(K(a),V(1),V(2),V(3))":        {"a NOT IN (?, ?, ?)", "1", "2", "3"},
		"LIKE(K(a),V(%abc%))":               {"a LIKE ?", "%abc%"},
		"NLIKE(K(a),V(%abc%))":              {"a NOT LIKE ?", "%abc%"},
		"ISNULL(K(a))":                      {"a IS NULL"},
		"ISNOTNULL(K(a))":                   {"a IS NOT NULL"},
		"CONTAINS(K(a),V(a),V(b),V(c))":     {"a @> ARRAY[?, ?, ?]", "a", "b", "c"},
		"NCONTAINS(K(a),V(a),V(b),V(c))":    {"NOT (a @> ARRAY[?, ?, ?])", "a", "b", "c"},
		"OVERLAPS(K(a),V(a),V(b),V(c))":     {"a && ARRAY[?, ?, ?]", "a", "b", "c"},
		"NOVERLAPS(K(a),V(a),V(b),V(c))":    {"NOT (a && ARRAY[?, ?, ?])", "a", "b", "c"},
		"ANY(K(a),V(1))":                    {"? = ANY(a)", "1"},
		"NANY(K(a),V(1))":                   {"? != ANY(a)", "1"},
		"ALL(K(a),V(1),V(2),V(3))":          {"ARRAY[?, ?, ?] <@ a", "1", "2", "3"},
		"NALL(K(a),V(1),V(2),V(3))":         {"NOT (ARRAY[?, ?, ?] <@ a)", "1", "2", "3"},
		"EQ(CAST(K(a),INT), V(5))":          {"CAST(a AS INT) = ?", "5"},
		"GT(CAST(K(b),FLOAT), V(3.14))":     {"CAST(b AS FLOAT) > ?", "3.14"},
	}
	// Define a list of bad queries that should produce errors based on function validity
	testBadQueries := []string{
		// AND and OR need at least two parameters
		"AND()",     // No arguments
		"AND(K(a))", // One argument
		"OR()",      // No arguments
		"OR(K(a))",  // One argument

		// EQ and NEQ require exactly two arguments
		"EQ()",      // No arguments
		"EQ(K(a))",  // One argument
		"NEQ()",     // No arguments
		"NEQ(K(a))", // One argument

		// LT, LTE, GT, GTE require exactly two arguments
		"LT()",      // No arguments
		"LT(K(a))",  // One argument
		"GT()",      // No arguments
		"GT(K(a))",  // One argument
		"LTE()",     // No arguments
		"LTE(K(a))", // One argument
		"GTE()",     // No arguments
		"GTE(K(a))", // One argument

		// IN and NOTIN require at least two arguments (one for column and at least one for values)
		"IN(K(a))",    // One argument
		"NOTIN(K(a))", // One argument
		"IN()",        // No arguments
		"NOTIN()",     // No arguments

		// LIKE and NLIKE require exactly two arguments
		"LIKE()",      // No arguments
		"LIKE(K(a))",  // One argument
		"NLIKE()",     // No arguments
		"NLIKE(K(a))", // One argument

		// ISNULL and ISNOTNULL require exactly one argument
		"ISNULL()",    // No arguments
		"ISNOTNULL()", // No arguments

		// CONTAINS and NCONTAINS require at least two arguments (one for the array and one for at least one value)
		"CONTAINS()",      // No arguments
		"CONTAINS(K(a))",  // One argument
		"NCONTAINS()",     // No arguments
		"NCONTAINS(K(a))", // One argument

		// OVERLAPS and NOVERLAPS require exactly two arguments (one for the array and one for another array)
		"OVERLAPS()",      // No arguments
		"OVERLAPS(K(a))",  // One argument
		"NOVERLAPS()",     // No arguments
		"NOVERLAPS(K(a))", // One argument

		// ANY and NANY require at least one argument
		"ANY()",      // No arguments
		"NANY()",     // No arguments
		"ANY(K(a))",  // One argument
		"NANY(K(a))", // One argument

		// ALL and NALL require at least one argument
		"ALL()",      // No arguments
		"NALL()",     // No arguments
		"ALL(K(a))",  // One argument
		"NALL(K(a))", // One argument

		// CAST requires exactly two arguments (expression and type)
		"CAST()",          // No arguments
		"CAST(K(a))",      // One argument
		"CAST(K(a), INT)", // Correct usage but used in invalid context (only for validation)

		// Invalid nested function usage examples
		"EQ(AND(K(a), V(1)), V(2))",       // AND cannot be nested within EQ
		"OR(EQ(K(a), V(1)))",              // EQ cannot be nested within OR
		"ANY(ALL(K(a), V(1)))",            // ALL cannot be nested within ANY
		"ALL(EQ(K(a), V(1)))",             // EQ cannot be nested within ALL
		"OVERLAPS(K(a), ANY(V(1), V(2)))", // ANY cannot be used inside OVERLAPS
	}

	p, err := NewSQLParser()
	if err != nil {
		t.Errorf("Error initializing parser: %v", err)
	}

	for test, expected := range testGoodQueries {
		q, params, err := p.Parse(ctx, test)
		if err != nil {
			t.Errorf("Error parsing %v: %v", test, err)
			continue
		}

		if q != expected[0] {
			t.Errorf("Expected query %v, got %v", expected[0], q)
		}

		for i, param := range params {
			if param.(string) != expected[i+1] {
				t.Errorf("Expected param %v, got %v", expected[i+1], param)
			}
		}
	}

	for _, test := range testBadQueries {
		_, _, err := p.Parse(ctx, test)
		if err == nil {
			t.Errorf("Expected error for bad query %v, but got none", test)
		}
	}
}
