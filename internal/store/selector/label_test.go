package selector

import (
	"context"
	"testing"

	"github.com/flightctl/flightctl/pkg/queryparser"
)

func TestLabelSelectorOperations(t *testing.T) {
	testGoodStrings := []string{
		"key",
		"!key",
		"key=val",
		"key!=val",
		"key in (val1,val2)",
		"key notin (val1,val2)",
		"(k1, k2)",
		"!(k1, k2)",
		"(k1, k2)=(v1, v2)",
		"(k1, k2)!=(v1, v2)",
		"(k1, k2) in ((v1, v2),(v3, v4))",
		"(k1, k2) notin ((v1, v2))",
	}

	testBadStrings := []string{
		"ke@y",
		"key=v@al",
		"(k1, k2)=val",
		"(k1, k2) in (v1, v2)",
	}

	for _, test := range testGoodStrings {
		ls, err := NewLabelSelector(test)
		if err != nil {
			t.Errorf("%v: error %v (%#v)\n", test, err, err)
			continue
		}

		_, _, err = ls.Parse(context.Background(), &goodTestModel{}, NewSelectorName("model.field16"))
		if err != nil {
			t.Errorf("%v: error %v (%#v)\n", test, err, err)
		}
	}

	for _, test := range testBadStrings {
		_, err := NewLabelSelector(test)
		if err == nil {
			t.Errorf("%v: did not get expected error\n", test)
			continue
		}
	}
}

func TestLabelSelectorQueries(t *testing.T) {
	ctx := context.Background()
	/*
		DoesNotExist        Operator = "!"
		Equals              Operator = "="
		DoubleEquals        Operator = "=="
		In                  Operator = "in"
		NotEquals           Operator = "!="
		NotIn               Operator = "notin"
		Exists              Operator = "exists"

	*/
	testGoodOperations := map[string]string{
		"key":                   "EXISTS(K(field16),V(key))",                                                                                                                              //Exists
		"!key":                  "OR(ISNULL(K(field16)),NOT(EXISTS(K(field16),V(key))))",                                                                                                  //DoesNotExist
		"key=val":               "AND(EXISTS(K(field16),V(key)),CONTAINS(K(field16),V({\"key\": \"val\"})))",                                                                              //Equals
		"key==val":              "AND(EXISTS(K(field16),V(key)),CONTAINS(K(field16),V({\"key\": \"val\"})))",                                                                              //DoubleEquals
		"key in (val1,val2)":    "AND(EXISTS(K(field16),V(key)),OR(CONTAINS(K(field16),V({\"key\": \"val1\"})),CONTAINS(K(field16),V({\"key\": \"val2\"}))))",                             //In
		"key!=val":              "OR(ISNULL(K(field16)),NOT(AND(EXISTS(K(field16),V(key)),CONTAINS(K(field16),V({\"key\": \"val\"})))))",                                                  //NotEquals
		"key notin (val1,val2)": "OR(ISNULL(K(field16)),NOT(AND(EXISTS(K(field16),V(key)),OR(CONTAINS(K(field16),V({\"key\": \"val1\"})),CONTAINS(K(field16),V({\"key\": \"val2\"}))))))", //NotIn
		"key=val1, key2!=val": "AND(" +
			"  AND(EXISTS(K(field16),V(key)),CONTAINS(K(field16),V({\"key\": \"val1\"})))," +
			"  OR(ISNULL(K(field16)),NOT(AND(EXISTS(K(field16),V(key2)),CONTAINS(K(field16),V({\"key2\": \"val\"})))))" +
			")",
		"(key1, key2) = (val1,val2)": "AND(" +
			"ALLEXISTS(K(field16),V(key1),V(key2)),CONTAINS(K(field16),V({\"key1\": \"val1\"\\,\"key2\": \"val2\"}))" +
			")",
		"(key1, key2) != (val1,val2)": "OR(" +
			"ISNULL(K(field16))," +
			"NOT(" +
			"    AND(ALLEXISTS(K(field16),V(key1),V(key2)),CONTAINS(K(field16),V({\"key1\": \"val1\"\\,\"key2\": \"val2\"})))" +
			"  )" +
			")",
		"(key1, key2) != (,val2)": "OR(" +
			"ISNULL(K(field16))," +
			"NOT(" +
			"    AND(ALLEXISTS(K(field16),V(key1),V(key2)),CONTAINS(K(field16),V({\"key1\": \"\"\\,\"key2\": \"val2\"})))" +
			"  )" +
			")",
		"(key1, key2) != (,)": "OR(" +
			"ISNULL(K(field16))," +
			"NOT(" +
			"    AND(ALLEXISTS(K(field16),V(key1),V(key2)),CONTAINS(K(field16),V({\"key1\": \"\"\\,\"key2\": \"\"})))" +
			"  )" +
			")",
	}

	fr, err := SelectorFieldResolver(&goodTestModel{})
	if err != nil {
		t.Errorf("error %v (%#v)\n", err, err)
		return
	}

	resolvedFields, err := fr.ResolveFields(NewSelectorName("model.field16"))
	if err != nil {
		t.Errorf("error %v (%#v)\n", err, err)
		return
	}

	for k8s, qp := range testGoodOperations {
		ls, err := NewLabelSelector(k8s)
		if err != nil {
			t.Errorf("%v: error %v (%#v)\n", k8s, err, err)
			continue
		}

		ls.field = resolvedFields[0]
		set1, err := ls.Tokenize(ctx, ls.selector)
		if err != nil {
			t.Errorf("%v: error %v (%#v)\n", k8s, err, err)
			continue
		}

		set2, err := queryparser.Tokenize(ctx, qp)
		if err != nil {
			t.Errorf("%v: error %v (%#v)\n", qp, err, err)
			continue
		}

		if !set1.Matches(set2) {
			t.Errorf("%v: %v not match %v\n", k8s, set1, set2)
		}
	}
}

type testMapOperation struct {
	Input    map[string]string
	Expected string
}

func TestLabelSelectorMap(t *testing.T) {
	ctx := context.Background()

	testCases := []testMapOperation{
		{
			Input:    map[string]string{"key": "val"},
			Expected: "AND(EXISTS(K(field16),V(key)),CONTAINS(K(field16),V({\"key\": \"val\"})))",
		},
		{
			Input: map[string]string{"key1": "val1", "key2": "val2"},
			Expected: "AND(" +
				"AND(EXISTS(K(field16),V(key1)),CONTAINS(K(field16),V({\"key1\": \"val1\"})))," +
				"AND(EXISTS(K(field16),V(key2)),CONTAINS(K(field16),V({\"key2\": \"val2\"})))" +
				")",
		},
		{
			Input: map[string]string{"region": "us", "env": "prod"},
			Expected: "AND(" +
				"AND(EXISTS(K(field16),V(env)),CONTAINS(K(field16),V({\"env\": \"prod\"})))," +
				"AND(EXISTS(K(field16),V(region)),CONTAINS(K(field16),V({\"region\": \"us\"})))" +
				")",
		},
		{
			Input:    map[string]string{"foo": "bar"},
			Expected: "AND(EXISTS(K(field16),V(foo)),CONTAINS(K(field16),V({\"foo\": \"bar\"})))",
		},
		{
			Input:    map[string]string{},
			Expected: "",
		},
	}

	fr, err := SelectorFieldResolver(&goodTestModel{})
	if err != nil {
		t.Errorf("error %v (%#v)\n", err, err)
		return
	}

	resolvedFields, err := fr.ResolveFields(NewSelectorName("model.field16"))
	if err != nil {
		t.Errorf("error %v (%#v)\n", err, err)
		return
	}

	for _, op := range testCases {
		ls, err := NewLabelSelectorFromMap(op.Input)
		if err != nil {
			t.Errorf("%v: error %v (%#v)\n", op, err, err)
			continue
		}

		ls.field = resolvedFields[0]
		set1, err := ls.Tokenize(ctx, ls.selector)
		if err != nil {
			t.Errorf("%v: error %v (%#v)\n", op, err, err)
			continue
		}

		set2, err := queryparser.Tokenize(ctx, op.Expected)
		if err != nil {
			t.Errorf("%v: error %v (%#v)\n", op, err, err)
			continue
		}

		if !set1.Matches(set2) {
			t.Errorf("%v: %v not match %v\n", op, set1, set2)
		}
	}
}
