package selector

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/flightctl/flightctl/pkg/queryparser"
	"k8s.io/apimachinery/pkg/fields"
)

type Model struct {
	Field1  bool        `selector:"field1"`                          // Boolean
	Field2  int         `selector:"field2"`                          // Integer
	Field5  float64     `selector:"field5"`                          // Float
	Field6  string      `selector:"field6"`                          // Text
	Field7  time.Time   `selector:"field7"`                          // Timestamp
	Field8  []int       `gorm:"type:integer[]" selector:"field8"`    // Integer Array
	Field9  []int16     `gorm:"type:smallint[]" selector:"field9"`   // Small Integer Array
	Field10 []int64     `gorm:"type:bigint[]" selector:"field10"`    // Big Integer Array
	Field11 []bool      `gorm:"type:boolean[]" selector:"field11"`   // Boolean Array
	Field12 []string    `gorm:"type:text[]" selector:"field12"`      // Text Array
	Field13 []float64   `gorm:"type:real[]" selector:"field13"`      // Float Array
	Field15 []time.Time `gorm:"type:timestamp[]" selector:"field15"` // Timestamp Array
	Field16 string      `gorm:"type:jsonb" selector:"field16"`       // JSONB
}

func (m *Model) ResolveCustomSelector(field SelectorFieldName) []SelectorFieldName {
	if strings.EqualFold("manualfield", string(field)) {
		return []SelectorFieldName{"field6", "field16.val::string"}
	}
	return nil
}

func (m *Model) ListCustomSelectors() []SelectorFieldName {
	return []SelectorFieldName{"manualfield"}
}

func TestFieldTypes(t *testing.T) {
	testGoodStrings := []string{
		"field1=true",                 // Boolean
		"field2=1",                    // Integer
		"field5=3.14",                 // Float
		"field6=Hello World",          // Text
		"field7=2024-10-14T15:04:05Z", // Timestamp
	}

	testBadStrings := []string{
		"field1=aa",  // Boolean
		"field2=aa",  // Integer
		"field3=aa",  // not exists
		"field5=aa",  // Float
		"field7=aa",  // Timestamp
		"field8=aa",  // Integer Array
		"field11=aa", // Boolean Array
		"field13=aa", // Float Array
		"field15=aa", // Timestamp Array
	}

	f, err := NewFieldSelector(&Model{})
	if err != nil {
		t.Errorf("%v: error %v (%#v)\n", f, err, err)
	}

	for _, test := range testGoodStrings {
		_, _, err := f.ParseFromString(context.Background(), test)
		if err != nil {
			t.Errorf("%v: error %v (%#v)\n", test, err, err)
		}
	}

	for _, test := range testBadStrings {
		_, _, err := f.ParseFromString(context.Background(), test)
		if err == nil {
			t.Errorf("%v: did not get expected error\n", test)
		}
	}
}

func TestOperations(t *testing.T) {
	ctx := context.Background()
	/*
		Equals              Operator = "="
		DoubleEquals        Operator = "=="
		NotEquals           Operator = "!="
	*/
	testGoodOperations := map[string]string{
		// Booleans
		"field1=true":  "EQ(K(field1),V(true))",                          //Equals
		"field1==true": "EQ(K(field1),V(true))",                          //DoubleEquals
		"field1!=true": "OR(ISNULL(K(field1)),NOTEQ(K(field1),V(true)))", //NotEquals

		// Numbers
		"field2=1":  "EQ(K(field2),V(1))",                          //Equals
		"field2==1": "EQ(K(field2),V(1))",                          //DoubleEquals
		"field2!=1": "OR(ISNULL(K(field2)),NOTEQ(K(field2),V(1)))", //NotEquals

		//Strings
		"field6=text":  "EQ(K(field6),V(text))",                          //Equals
		"field6==text": "EQ(K(field6),V(text))",                          //DoubleEquals
		"field6!=text": "OR(ISNULL(K(field6)),NOTEQ(K(field6),V(text)))", //NotEquals

		// Timestamps
		"field7=2024-10-14T22:47:31+03:00":  "EQ(K(field7),V(2024-10-14T22:47:31+03:00))",                          //Equals
		"field7==2024-10-14T22:47:31+03:00": "EQ(K(field7),V(2024-10-14T22:47:31+03:00))",                          //DoubleEquals
		"field7!=2024-10-14T22:47:31+03:00": "OR(ISNULL(K(field7)),NOTEQ(K(field7),V(2024-10-14T22:47:31+03:00)))", //NotEquals

		// JSONB
		"field16=\"text\"":              "EQ(K(field16),V(\"text\"))",                             //Equals
		"field16={\"some\":\"text\"}":   "EQ(K(field16),V({\"some\":\"text\"}))",                  //Equals
		"field16.some.key.val=\"text\"": "EQ(K(field16 -> 'some' -> 'key' -> 'val'),V(\"text\"))", //Equals
		"field16==\"text\"":             "EQ(K(field16),V(\"text\"))",                             //DoubleEquals
		"field16!=\"text\"":             "OR(ISNULL(K(field16)),NOTEQ(K(field16),V(\"text\")))",   //NotEquals

		// JSONB casting
		"field16.test::boolean=true":  "EQ(CAST(K(field16 ->> 'test'), boolean),V(true))",                                      //Equals
		"field16.test::boolean==true": "EQ(CAST(K(field16 ->> 'test'), boolean),V(true))",                                      //DoubleEquals
		"field16.test::boolean!=true": "OR(ISNULL(K(field16 ->> 'test')),NOTEQ(CAST(K(field16 ->> 'test'), boolean),V(true)))", //NotEquals
		"field16.test::string=text":   "EQ(K(field16 ->> 'test'),V(text))",                                                     //Equals
		"field16.test::string==text":  "EQ(K(field16 ->> 'test'),V(text))",                                                     //DoubleEquals
		"field16.test::string!=text":  "OR(ISNULL(K(field16 ->> 'test')),NOTEQ(K(field16 ->> 'test'),V(text)))",                //NotEquals

		// Multiple requirements
		"field6!=text1,field6!=text2": "AND(OR(ISNULL(K(field6)),NOTEQ(K(field6),V(text1))), OR(ISNULL(K(field6)),NOTEQ(K(field6),V(text2))))", // NotEquals

		// Manual resolved fields
		"manualfield=test": "OR(EQ(K(field6),V(test)),EQ(K(field16 ->> 'val'),V(test)))",
	}

	testBadOperations := []string{
		// JSONB casting
		"field16.test::",
		"field16.test::unknown",

		// Arrays
		"field12=text",  //Equals
		"field12==text", //DoubleEquals
		"field12!=text", //NotEquals
	}

	f, err := NewFieldSelector(&Model{})
	if err != nil {
		t.Errorf("%v: error %v (%#v)\n", f, err, err)
		return
	}

	for k8s, qp := range testGoodOperations {
		selector, err := fields.ParseSelector(k8s)
		if err != nil {
			t.Errorf("%v: error %v (%#v)\n", k8s, err, err)
			continue
		}

		set1, err := f.Tokenize(ctx, selector)
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

	for _, test := range testBadOperations {
		_, _, err := f.ParseFromString(context.Background(), test)
		if err == nil {
			t.Errorf("%v: did not get expected error\n", test)
		}
	}
}
