package selector

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/flightctl/flightctl/pkg/queryparser"
	"github.com/flightctl/flightctl/pkg/selector/fields"
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

func (m *Model) ResolveFieldName(field SelectorFieldName) []SelectorFieldName {
	if strings.EqualFold("manualfield", string(field)) {
		return []SelectorFieldName{"field6", "field16"}
	}
	return nil
}

func TestFieldTypes(t *testing.T) {
	testGoodStrings := []string{
		"field1=true",                          // Boolean
		"field2=1",                             // Integer
		"field5=3.14",                          // Float
		"field6=Hello\\ World",                 // Text
		"field7=2024-10-14T15:04:05Z",          // Timestamp
		"field8 in (1,2,3)",                    // Integer Array
		"field9 in (1,2,3)",                    // Small Integer Array
		"field10 in (10000000000,20000000000)", // Big Integer Array
		"field11 in (true,false)",              // Boolean Array
		"field12 in (First,Second)",            // Text Array
		"field13 in (1.1,2.2,3.3)",             // Float Array
		"field15 in (2024-10-14T15:04:05Z,2024-10-15T15:04:05Z)", // Timestamp Array
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
		DoesNotExist        Operator = "!"
		Equals              Operator = "="
		DoubleEquals        Operator = "=="
		In                  Operator = "in"
		Like                Operator = "~="
		NotLike             Operator = "!~"
		NotEquals           Operator = "!="
		NotIn               Operator = "notin"
		Exists              Operator = "exists"
		GreaterThan         Operator = "gt"
		GreaterThanOrEquals Operator = "gte"
		LessThan            Operator = "lt"
		LessThanOrEquals    Operator = "lte"
	*/
	testGoodOperations := map[string]string{
		// Booleans
		"field1":                    "ISNOTNULL(K(field1))",                                    //Exists
		"!field1":                   "ISNULL(K(field1))",                                       //DoesNotExist
		"field1=true":               "EQ(K(field1),V(true))",                                   //Equals
		"field1==true":              "EQ(K(field1),V(true))",                                   //DoubleEquals
		"field1 in (true,false)":    "IN(K(field1),V(false),V(true))",                          //In
		"field1!=true":              "OR(ISNULL(K(field1)),NOTEQ(K(field1),V(true)))",          //NotEquals
		"field1 notin (true,false)": "OR(ISNULL(K(field1)),NOTIN(K(field1),V(false),V(true)))", //NotIn

		// Numbers
		"field2":             "ISNOTNULL(K(field2))",                             //Exists
		"!field2":            "ISNULL(K(field2))",                                //DoesNotExist
		"field2=1":           "EQ(K(field2),V(1))",                               //Equals
		"field2==1":          "EQ(K(field2),V(1))",                               //DoubleEquals
		"field2 in (1,2)":    "IN(K(field2),V(1),V(2))",                          //In
		"field2!=1":          "OR(ISNULL(K(field2)),NOTEQ(K(field2),V(1)))",      //NotEquals
		"field2 notin (1,2)": "OR(ISNULL(K(field2)),NOTIN(K(field2),V(1),V(2)))", //NotIn
		"field2>1":           "GT(K(field2),V(1))",                               //GreaterThan
		"field2>=1":          "GTE(K(field2),V(1))",                              //GreaterThanOrEquals
		"field2<1":           "LT(K(field2),V(1))",                               //LessThan
		"field2<=1":          "LTE(K(field2),V(1))",                              //LessThanOrEquals

		//Strings
		"field6":                     "ISNOTNULL(K(field6))",                                     //Exists
		"!field6":                    "ISNULL(K(field6))",                                        //DoesNotExist
		"field6=text":                "EQ(K(field6),V(text))",                                    //Equals
		"field6==text":               "EQ(K(field6),V(text))",                                    //DoubleEquals
		"field6 in (text1,text2)":    "IN(K(field6),V(text1),V(text2))",                          //In
		"field6~=%text%":             "LIKE(K(field6),V(%text%))",                                //Like
		"field6!~%text%":             "NOTLIKE(K(field6),V(%text%))",                             //NotLike
		"field6!=text":               "OR(ISNULL(K(field6)),NOTEQ(K(field6),V(text)))",           //NotEquals
		"field6 notin (text1,text2)": "OR(ISNULL(K(field6)),NOTIN(K(field6),V(text1),V(text2)))", //NotIn
		"field6>text":                "GT(K(field6),V(text))",                                    //GreaterThan
		"field6>=text":               "GTE(K(field6),V(text))",                                   //GreaterThanOrEquals
		"field6<text":                "LT(K(field6),V(text))",                                    //LessThan
		"field6<=text":               "LTE(K(field6),V(text))",                                   //LessThanOrEquals

		// Timestamps
		"field7":                                   "ISNOTNULL(K(field7))",                                                //Exists
		"!field7":                                  "ISNULL(K(field7))",                                                   //DoesNotExist
		"field7=2024-10-14T22:47:31+03:00":         "EQ(K(field7),V(2024-10-14T22:47:31+03:00))",                          //Equals
		"field7 in (2024-10-14T22:47:31+03:00)":    "IN(K(field7),V(2024-10-14T22:47:31+03:00))",                          //In
		"field7!=2024-10-14T22:47:31+03:00":        "OR(ISNULL(K(field7)),NOTEQ(K(field7),V(2024-10-14T22:47:31+03:00)))", //NotEquals
		"field7 notin (2024-10-14T22:47:31+03:00)": "OR(ISNULL(K(field7)),NOTIN(K(field7),V(2024-10-14T22:47:31+03:00)))", //NotIn
		"field7>2024-10-14T22:47:31+03:00":         "GT(K(field7),V(2024-10-14T22:47:31+03:00))",                          //GreaterThan
		"field7>=2024-10-14T22:47:31+03:00":        "GTE(K(field7),V(2024-10-14T22:47:31+03:00))",                         //GreaterThanOrEquals
		"field7<2024-10-14T22:47:31+03:00":         "LT(K(field7),V(2024-10-14T22:47:31+03:00))",                          //LessThan
		"field7<=2024-10-14T22:47:31+03:00":        "LTE(K(field7),V(2024-10-14T22:47:31+03:00))",                         //LessThanOrEquals

		// Arrays
		"field12":                     "ISNOTNULL(K(field12))",                                            //Exists
		"!field12":                    "ISNULL(K(field12))",                                               //DoesNotExist
		"field12=text":                "CONTAINS(K(field12),V(text))",                                     //Equals
		"field12==text":               "CONTAINS(K(field12),V(text))",                                     //DoubleEquals
		"field12 in (text1,text2)":    "OVERLAPS(K(field12),V(text1),V(text2))",                           //In
		"field12!=text":               "OR(ISNULL(K(field12)),NOTCONTAINS(K(field12),V(text)))",           //NotEquals
		"field12 notin (text1,text2)": "OR(ISNULL(K(field12)),NOTOVERLAPS(K(field12),V(text1),V(text2)))", //NotIn

		// JSONB
		"field16":                     "ISNOTNULL(K(field16))",                                      //Exists
		"field16.some.key":            "ISNOTNULL(K(field16.some.key))",                             //Exists
		"!field16":                    "ISNULL(K(field16))",                                         //DoesNotExist
		"field16=text":                "EQ(K(field16),V(text))",                                     //Equals
		"field16.some.key.val=text":   "EQ(K(field16.some.key.val),V(text))",                        //Equals
		"field16==text":               "EQ(K(field16),V(text))",                                     //DoubleEquals
		"field16 in (text1,text2)":    "IN(K(field16),V(text1),V(text2))",                           //In
		"field16~=%text%":             "LIKE(K(field16),V(%text%))",                                 //Like
		"field16!~%text%":             "NOTLIKE(K(field16),V(%text%))",                              //NotLike
		"field16!=text":               "OR(ISNULL(K(field16)),NOTEQ(K(field16),V(text)))",           //NotEquals
		"field16 notin (text1,text2)": "OR(ISNULL(K(field16)),NOTIN(K(field16),V(text1),V(text2)))", //NotIn
		"field16>text":                "GT(K(field16),V(text))",                                     //GreaterThan
		"field16>=text":               "GTE(K(field16),V(text))",                                    //GreaterThanOrEquals
		"field16<text":                "LT(K(field16),V(text))",                                     //LessThan
		"field16<=text":               "LTE(K(field16),V(text))",                                    //LessThanOrEquals

		// JSONB casting
		"field16.test::boolean":                    "ISNOTNULL(K(field16.test))",                                                         //Exists
		"!field16.test::boolean":                   "ISNULL(K(field16.test))",                                                            //DoesNotExist
		"field16.test::boolean=true":               "EQ(CAST(K(field16.test), boolean),V(true))",                                         //Equals
		"field16.test::boolean==true":              "EQ(CAST(K(field16.test), boolean),V(true))",                                         //DoubleEquals
		"field16.test::boolean in (true,false)":    "IN(CAST(K(field16.test), boolean),V(false),V(true))",                                //In
		"field16.test::boolean!=true":              "OR(ISNULL(K(field16.test)),NOTEQ(CAST(K(field16.test), boolean),V(true)))",          //NotEquals
		"field16.test::boolean notin (true,false)": "OR(ISNULL(K(field16.test)),NOTIN(CAST(K(field16.test), boolean),V(false),V(true)))", //NotIn

		// Multiple requirements
		"field1, field1 notin (true,false)": "AND(ISNOTNULL(K(field1)),OR(ISNULL(K(field1)),NOTIN(K(field1),V(false),V(true))))",                     // Exists + NotIn
		"field2 >= 0, field2 <= 10":         "AND(GTE(K(field2),V(0)), LTE(K(field2),V(10)))",                                                        // GreaterThanOrEquals + LessThanOrEquals
		"field6 != text1, field6 != text2":  "AND(OR(ISNULL(K(field6)),NOTEQ(K(field6),V(text1))), OR(ISNULL(K(field6)),NOTEQ(K(field6),V(text2))))", // NotEquals

		// Manual resolved fields
		"manualfield=test": "OR(EQ(K(field6),V(test)),EQ(K(field16),V(test)))",
	}

	testBadStrings := []string{
		// Booleans
		"field1~=true", //Like
		"field1!~true", //NotLike
		"field1>true",  //GreaterThan
		"field1>=true", //GreaterThanOrEquals
		"field1<true",  //LessThan
		"field1<=true", //LessThanOrEquals

		// Numbers
		"field2~=1", //Like
		"field2!~1", //NotLike

		// Timestamps
		"field7~=2024-10-14T22:47:31+03:00", //Like
		"field7!~2024-10-14T22:47:31+03:00", //NotLike

		// Arrays
		"field12~=text", //Like
		"field12!~text", //NotLike
		"field12>text",  //GreaterThan
		"field12>=text", //GreaterThanOrEquals
		"field12<text",  //LessThan
		"field12<=text", //LessThanOrEquals

		// JSONB casting
		"field16.test::",
		"field16.test::unknown",
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

	for _, test := range testBadStrings {
		_, _, err := f.ParseFromString(context.Background(), test)
		if err == nil {
			t.Errorf("%v: did not get expected error\n", test)
		}
	}
}
