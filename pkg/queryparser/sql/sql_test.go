package sql

import (
	"context"
	"testing"
)

func TestSQLQueries(t *testing.T) {
	ctx := context.Background()
	/*
		AND, OR,
		EQ, NOTEQ,
		LT, LTE,
		GT, GTE,
		IN, NOTIN,
		LIKE, NOTLIKE,
		ISNULL, ISNOTNULL,
		CONTAINS, NOTCONTAINS,
		OVERLAPS, NOTOVERLAPS,
		CAST,
		K, V
	*/
	testGoodQueries := map[string][]string{
		"":                                               {""},
		"LT(K(a),V(1))":                                  {"a < ?", "1"},
		"LTE(K(a),V(1))":                                 {"a <= ?", "1"},
		"GT(K(a),V(1))":                                  {"a > ?", "1"},
		"GTE(K(a),V(1))":                                 {"a >= ?", "1"},
		"IN(K(a),V(1),V(2),V(3))":                        {"a IN (?, ?, ?)", "1", "2", "3"},
		"NOTIN(K(a),V(1),V(2),V(3))":                     {"a NOT IN (?, ?, ?)", "1", "2", "3"},
		"LIKE(K(a),V(%abc%))":                            {"a LIKE ?", "%abc%"},
		"NOTLIKE(K(a),V(%abc%))":                         {"a NOT LIKE ?", "%abc%"},
		"ISNULL(K(a))":                                   {"a IS NULL"},
		"ISNOTNULL(K(a))":                                {"a IS NOT NULL"},
		"CONTAINS(K(a),V(a),V(b),V(c))":                  {"a @> ARRAY[?, ?, ?]", "a", "b", "c"},
		"NOTCONTAINS(K(a),V(a),V(b),V(c))":               {"NOT (a @> ARRAY[?, ?, ?])", "a", "b", "c"},
		"OVERLAPS(K(a),V(a),V(b),V(c))":                  {"a && ARRAY[?, ?, ?]", "a", "b", "c"},
		"NOTOVERLAPS(K(a),V(a),V(b),V(c))":               {"NOT (a && ARRAY[?, ?, ?])", "a", "b", "c"},
		"EQ(CAST(K(a),INT), V(5))":                       {"CAST(a AS INT) = ?", "5"},
		"GT(CAST(K(b),FLOAT), V(3.14))":                  {"CAST(b AS FLOAT) > ?", "3.14"},
		"AND(EQ(K(a),V(b)),NOTEQ(K(c),V(d)))":            {"(a = ? AND c != ?)", "b", "d"},
		"AND(EQ(CAST(K(a), INT),V(5)),NOTEQ(K(c),V(d)))": {"(CAST(a AS INT) = ? AND c != ?)", "5", "d"},
		"OR(EQ(K(a),V(b)),NOTEQ(K(c),V(d)))":             {"(a = ? OR c != ?)", "b", "d"},
		"OR(EQ(K(a),V(b)),NOTEQ(K(c),CAST(V(5), INT)))":  {"(a = ? OR c != CAST(? AS INT))", "b", "5"},
	}
	// Define a list of bad queries that should produce errors based on function validity
	testBadQueries := []string{
		// AND and OR need at least two queries parameters
		"AND()",                             // No arguments
		"AND(EQ(K(a),V(b)))",                // One argument
		"AND(a,b)",                          // No values
		"AND(EQ(K(a),V(b)),val)",            // No values
		"AND(K(a),V(b))",                    // K and V cannot be used directly
		"AND(EQ(K(a),V(b)),V(val))",         // K and V cannot be used directly
		"AND(CAST(K(a),INT),EQ(K(a),V(b)))", // CAST cannot be used directly
		"OR()",                              // No arguments
		"OR(EQ(K(a),V(b)))",                 // One argument
		"OR(a,b)",                           // No values
		"OR(EQ(K(a),V(b)),val)",             // No values
		"OR(K(a),V(b))",                     // K and V cannot be used directly
		"OR(EQ(K(a),V(b)),V(val))",          // K and V cannot be used directly
		"OR(CAST(K(a),INT),EQ(K(a),V(b)))",  // CAST cannot be used directly

		// EQ and NOTEQ require exactly two queries parameters
		"EQ()",                  // No arguments
		"EQ(K(a))",              // One argument
		"EQ(K(a),V(b),V(c))",    // Three arguments
		"EQ(V(b),K(a))",         // First query must be K or CAST
		"EQ(val2,val2)",         // First query must be K or CAST
		"EQ(K(b),val)",          // No values
		"NOTEQ()",               // No arguments
		"NOTEQ(K(a))",           // One argument
		"NOTEQ(K(a),V(b),V(c))", // Three arguments
		"NOTEQ(V(b),K(a))",      // First query must be K or CAST
		"NOTEQ(val2,val2)",      // First query must be K or CAST
		"NOTEQ(K(b),val)",       // No values

		// LT, LTE, GT, GTE require exactly two queries parameters
		"LT()",                // No arguments
		"LT(K(a))",            // One argument
		"LT(K(a),V(b),V(c))",  // Three arguments
		"LT(V(b),K(a))",       // First query must be K or CAST
		"LT(val2,val2)",       // First query must be K or CAST
		"LT(K(b),val)",        // No values
		"LTE()",               // No arguments
		"LTE(K(a))",           // One argument
		"LTE(K(a),V(b),V(c))", // Three arguments
		"LTE(V(b),K(a))",      // First query must be K or CAST
		"LTE(val2,val2)",      // First query must be K or CAST
		"LTE(K(b),val)",       // No values
		"GT()",                // No arguments
		"GT(K(a))",            // One argument
		"GT(K(a),V(b),V(c))",  // Three arguments
		"GT(V(b),K(a))",       // First query must be K or CAST
		"GT(val2,val2)",       // First query must be K or CAST
		"GT(K(b),val)",        // No values
		"GTE()",               // No arguments
		"GTE(K(a))",           // One argument
		"GTE(K(a),V(b),V(c))", // Three arguments
		"GTE(V(b),K(a))",      // First query must be K or CAST
		"GTE(val2,val2)",      // First query must be K or CAST
		"GTE(K(b),val)",       // No values

		// IN and NOTIN require at least two queries parameters
		"IN()",             // No arguments
		"IN(K(a))",         // One argument
		"IN(V(b),K(a))",    // First query must be K or CAST
		"IN(val2,val2)",    // First query must be K or CAST
		"IN(K(b),val)",     // No values
		"NOTIN()",          // No arguments
		"NOTIN(K(a))",      // One argument
		"NOTIN(V(b),K(a))", // First query must be K or CAST
		"NOTIN(val2,val2)", // First query must be K or CAST
		"NOTIN(K(b),val)",  // No values

		// LIKE and NOTLIKE require exactly two queries parameters
		"LIKE()",                  // No arguments
		"LIKE(K(a))",              // One argument
		"LIKE(K(a),V(b),V(c))",    // Three arguments
		"LIKE(V(b),K(a))",         // First query must be K or CAST
		"LIKE(val2,val2)",         // First query must be K or CAST
		"LIKE(K(b),val)",          // No values
		"NOTLIKE()",               // No arguments
		"NOTLIKE(K(a))",           // One argument
		"NOTLIKE(K(a),V(b),V(c))", // Three arguments
		"NOTLIKE(V(b),K(a))",      // First query must be K or CAST
		"NOTLIKE(val2,val2)",      // First query must be K or CAST
		"NOTLIKE(K(b),val)",       // No values

		// ISNULL and ISNOTNULL require exactly one query parameter
		"ISNULL()",             // No arguments
		"ISNULL(K(a),V(b))",    // Two arguments
		"ISNULL(V(b))",         // First query must be K or CAST
		"ISNULL(val)",          // No values
		"ISNOTNULL()",          // No arguments
		"ISNOTNULL(K(a),V(b))", // Two arguments
		"ISNOTNULL(V(b))",      // First query must be K or CAST
		"ISNOTNULL(val)",       // No values

		// CONTAINS and NOTCONTAINS require at least two queries parameters
		"CONTAINS()",             // No arguments
		"CONTAINS(K(a))",         // One argument
		"CONTAINS(V(b),K(a))",    // First query must be K or CAST
		"CONTAINS(val2,val2)",    // First query must be K or CAST
		"CONTAINS(K(b),val)",     // No values
		"NOTCONTAINS()",          // No arguments
		"NOTCONTAINS(K(a))",      // One argument
		"NOTCONTAINS(V(b),K(a))", // First query must be K or CAST
		"NOTCONTAINS(val2,val2)", // First query must be K or CAST
		"NOTCONTAINS(K(b),val)",  // No values

		// OVERLAPS and NOTOVERLAPS require at least two queries parameters
		"OVERLAPS()",             // No arguments
		"OVERLAPS(K(a))",         // One argument
		"OVERLAPS(V(b),K(a))",    // First query must be K or CAST
		"OVERLAPS(val2,val2)",    // First query must be K or CAST
		"OVERLAPS(K(b),val)",     // No values
		"NOTOVERLAPS()",          // No arguments
		"NOTOVERLAPS(K(a))",      // One argument
		"NOTOVERLAPS(V(b),K(a))", // First query must be K or CAST
		"NOTOVERLAPS(val2,val2)", // First query must be K or CAST
		"NOTOVERLAPS(K(b),val)",  // No values

		// CAST requires exactly two queries parameters (K, V and type)
		"EQ(CAST(), V(5))",                   // No arguments
		"EQ(CAST(K(a)), V(5))",               // One argument
		"EQ(CAST(V(a)), V(5))",               // One argument
		"EQ(CAST(a,INT))",                    // No preceding value
		"EQ(CAST(CAST(K(a),INT),INT), V(5))", // Nested cast
		"EQ(CAST(K(a),MY.TYPE), V(5))",       // Invalid type

		// K and V require exactly one value parameter
		"EQ(K(), V(5))",          // No arguments
		"EQ(K(K(a)), V(5))",      // Nested
		"EQ(K(MY@COLUMN), V(5))", // Invalid column name [a-zA-Z_][a-zA-Z0-9_]*
		"EQ(K(;), V(5))",         // Invalid column name [a-zA-Z_][a-zA-Z0-9_]*
		"EQ(K(*), V(5))",         // Invalid column name [a-zA-Z_][a-zA-Z0-9_]*
		"EQ(K(a), V())",          // No arguments
		"EQ(K(a), V(V(5)))",      // Nested

		// Invalid context: cannot be used independently without being nested within another function
		"CAST(K(a), INT)",
		"K(a)",
		"v(a)",

		// Invalid nested function usage examples
		"EQ(AND(K(a), V(1)), V(2))",      // AND cannot be nested within EQ
		"EQ(OR(K(a), V(1)), V(2))",       // OR cannot be nested within EQ
		"CAST(AND(K(a), V(1)), INT)",     // AND cannot be nested within CAST
		"ISNULL(OVERLAPS(K(a), K(b)))",   // OVERLAPS cannot be nested within ISNULL
		"CONTAINS(LIKE(K(a), V(1)))",     // LIKE cannot be nested within CONTAINS
		"LT(AND(K(a), V(1)))",            // AND cannot be nested within LT
		"LTE(NOTEQ(K(a), V(1)))",         // NOTEQ cannot be nested within LTE
		"GTE(NOTIN(K(a), V(1)))",         // NOTIN cannot be nested within GTE
		"NOTLIKE(AND(K(a), V(1)))",       // AND cannot be nested within NOTLIKE
		"NOTCONTAINS(NOTEQ(K(a), V(1)))", // NOTEQ cannot be nested within NOTCONTAINS
		"OVERLAPS(ISNULL(K(a)))",         // ISNULL cannot be nested within OVERLAPS
		"OVERLAPS(ISNOTNULL(K(a)))",      // ISNOTNULL cannot be nested within OVERLAPS

	}

	p, err := NewSQLParser()
	if err != nil {
		t.Errorf("Error initializing parser: %v", err)
		return
	}

	for test, expected := range testGoodQueries {
		q, params, err := p.Parse(ctx, test)
		if err != nil {
			t.Errorf("Error parsing %v: %v", test, err)
			continue
		}

		if q != expected[0] {
			t.Errorf("Expected query %v, got %v", expected[0], q)
			continue
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
