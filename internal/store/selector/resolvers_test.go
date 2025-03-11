package selector

import (
	"context"
	"slices"
	"sort"
	"testing"

	"github.com/flightctl/flightctl/pkg/queryparser"
)

type compositeTestModel struct {
	Field1 string `selector:"model.fieldc1"`
	Field2 int    `selector:"model.fieldc2"`
	Field3 bool   `selector:"model.fieldc3"`
	Field4 string `gorm:"type:jsonb" selector:"model.fieldc4"`
}

func TestCompositeResolverQueries(t *testing.T) {
	ctx := context.Background()
	testCases := map[string]string{
		"model.field1":           "ISNOTNULL(K(good_test_models.field1))",
		"model.field16":          "ISNOTNULL(K(good_test_models.field16))",
		"model.field16.some.key": "ISNOTNULL(K(good_test_models.field16 -> 'some' -> 'key'))",
		"model.fieldc4.some.key": "ISNOTNULL(K(composite_test_models.field4 -> 'some' -> 'key'))",
		"model.field1, model.fieldc3 notin (true,false)": "AND(ISNOTNULL(K(good_test_models.field1))," +
			"OR(ISNULL(K(composite_test_models.field3)),NOTIN(K(composite_test_models.field3),V(true),V(false))))",
		"model.field2 >= 0, model.fieldc2 <= 10": "AND(GTE(K(good_test_models.field2),V(0)), LTE(K(composite_test_models.field2),V(10)))",
		"model.field6 != text1, model.fieldc1 != text2": "AND(OR(ISNULL(K(good_test_models.field6)),NOTEQ(K(good_test_models.field6),V(text1)))," +
			"OR(ISNULL(K(composite_test_models.field1)),NOTEQ(K(composite_test_models.field1),V(text2))))",
		"customfield5.approved = true": "EQ(CAST(K(good_test_models.goodfield -> 'path' ->> 'approved'), boolean),V(true))",
	}

	resolver, err := NewCompositeSelectorResolver(&goodTestModel{}, &compositeTestModel{})
	if err != nil {
		t.Fatalf("failed to create CompositeSelectorResolver: %v", err)
	}

	for input, expectedQuery := range testCases {
		fieldSelector, err := NewFieldSelector(input)
		if err != nil {
			t.Fatalf("failed to parse field selector for %q: %v", input, err)
		}

		actualTokens, err := fieldSelector.Tokenize(ctx, selectorParserSession{selector: fieldSelector.selector, resolver: resolver})
		if err != nil {
			t.Fatalf("failed to tokenize input %q: %v", input, err)
		}

		expectedTokens, err := queryparser.Tokenize(ctx, expectedQuery)
		if err != nil {
			t.Fatalf("failed to tokenize expected query %q: %v", expectedQuery, err)
		}

		if !actualTokens.Matches(expectedTokens) {
			t.Errorf("unexpected tokenization result for %q:\nexpected: %v\nactual:   %v", input, expectedTokens, actualTokens)
		}
	}
}

func TestCompositeResolverList(t *testing.T) {
	r1, err := SelectorFieldResolver(&goodTestModel{})
	if err != nil {
		t.Fatalf("failed to create resolver for goodTestModel: %v", err)
	}

	r2, err := SelectorFieldResolver(&compositeTestModel{})
	if err != nil {
		t.Fatalf("failed to create resolver for compositeTestModel: %v", err)
	}

	set := NewSelectorFieldNameSet()
	set.Add(r1.List()...)
	set.Add(r2.List()...)
	expectedList := set.List()
	sort.Slice(expectedList, func(i, j int) bool {
		return expectedList[i].String() < expectedList[j].String()
	})

	cr, err := NewCompositeSelectorResolver(&goodTestModel{}, &compositeTestModel{})
	if err != nil {
		t.Fatalf("failed to create CompositeSelectorResolver: %v", err)
	}

	actualList := cr.List()
	sort.Slice(actualList, func(i, j int) bool {
		return actualList[i].String() < actualList[j].String()
	})

	if !slices.Equal(expectedList, actualList) {
		t.Errorf("resolved field lists do not match:\nexpected: %v\nactual:   %v", expectedList, actualList)
	}
}
