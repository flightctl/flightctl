package selector

import (
	"context"
	"testing"

	"github.com/flightctl/flightctl/pkg/k8s/selector/fields"
	"github.com/flightctl/flightctl/pkg/queryparser"
)

func TestFieldTypes(t *testing.T) {
	testGoodStrings := []string{
		"model.field1=true",                                            // Boolean
		"model.field2=1",                                               // Integer
		"model.field5=3.14",                                            // Float
		"model.field6=Hello\\ World",                                   // Text
		"model.field7=2024-10-14T15:04:05Z",                            // Timestamp
		"model.field8 in (1,2,3)",                                      // Integer Array
		"model.field8[1] = 2",                                          // Integer Array
		"model.field9 in (1,2,3)",                                      // Small Integer Array
		"model.field9[1]= 2",                                           // Small Integer Array
		"model.field10 in (10000000000,20000000000)",                   // Big Integer Array
		"model.field10[1]= 20000000000",                                // Big Integer Array
		"model.field11 in (true,false)",                                // Boolean Array
		"model.field11[1]= false",                                      // Boolean Array
		"model.field12 in (First,Second)",                              // Text Array
		"model.field12[1]= Second",                                     // Text Array
		"model.field13 in (1.1,2.2,3.3)",                               // Float Array
		"model.field13[1]= 2.2",                                        // Float Array
		"model.field15 in (2024-10-14T15:04:05Z,2024-10-15T15:04:05Z)", // Timestamp Array
		"model.field15[1]= 2024-10-15T15:04:05Z",                       // Timestamp Array
		"model.field16={\"some\":\"json\"}",                            // JSONB
		"model.field16.array[0]={\"some\":\"json\"}",                   // JSONB
	}

	testBadStrings := []string{
		"model.field1=aa",  // Boolean
		"model.field2=aa",  // Integer
		"model.field3=aa",  // not exists
		"model.field5=aa",  // Float
		"model.field7=aa",  // Timestamp
		"model.field8=aa",  // Integer Array
		"model.field11=aa", // Boolean Array
		"model.field13=aa", // Float Array
		"model.field15=aa", // Timestamp Array
	}

	f, err := NewFieldSelector(&goodTestModel{})
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
		Contains            Operator = "@>"
		NotContains         Operator = "!@"
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
		"model.field1":                    "ISNOTNULL(K(field1))",                                    //Exists
		"!model.field1":                   "ISNULL(K(field1))",                                       //DoesNotExist
		"model.field1=true":               "EQ(K(field1),V(true))",                                   //Equals
		"model.field1==true":              "EQ(K(field1),V(true))",                                   //DoubleEquals
		"model.field1 in (true,false)":    "IN(K(field1),V(false),V(true))",                          //In
		"model.field1!=true":              "OR(ISNULL(K(field1)),NOTEQ(K(field1),V(true)))",          //NotEquals
		"model.field1 notin (true,false)": "OR(ISNULL(K(field1)),NOTIN(K(field1),V(false),V(true)))", //NotIn

		// Numbers
		"model.field2":             "ISNOTNULL(K(field2))",                             //Exists
		"!model.field2":            "ISNULL(K(field2))",                                //DoesNotExist
		"model.field2=1":           "EQ(K(field2),V(1))",                               //Equals
		"model.field2==1":          "EQ(K(field2),V(1))",                               //DoubleEquals
		"model.field2 in (1,2)":    "IN(K(field2),V(1),V(2))",                          //In
		"model.field2!=1":          "OR(ISNULL(K(field2)),NOTEQ(K(field2),V(1)))",      //NotEquals
		"model.field2 notin (1,2)": "OR(ISNULL(K(field2)),NOTIN(K(field2),V(1),V(2)))", //NotIn
		"model.field2>1":           "GT(K(field2),V(1))",                               //GreaterThan
		"model.field2>=1":          "GTE(K(field2),V(1))",                              //GreaterThanOrEquals
		"model.field2<1":           "LT(K(field2),V(1))",                               //LessThan
		"model.field2<=1":          "LTE(K(field2),V(1))",                              //LessThanOrEquals

		//Strings
		"model.field6":                     "ISNOTNULL(K(field6))",                                     //Exists
		"!model.field6":                    "ISNULL(K(field6))",                                        //DoesNotExist
		"model.field6=text":                "EQ(K(field6),V(text))",                                    //Equals
		"model.field6==text":               "EQ(K(field6),V(text))",                                    //DoubleEquals
		"model.field6 in (text1,text2)":    "IN(K(field6),V(text1),V(text2))",                          //In
		"model.field6@>text":               "LIKE(K(field6),V(%text%))",                                //Contains
		"model.field6!@text":               "NOTLIKE(K(field6),V(%text%))",                             //NotContains
		"model.field6!=text":               "OR(ISNULL(K(field6)),NOTEQ(K(field6),V(text)))",           //NotEquals
		"model.field6 notin (text1,text2)": "OR(ISNULL(K(field6)),NOTIN(K(field6),V(text1),V(text2)))", //NotIn

		// Timestamps
		"model.field7":                                   "ISNOTNULL(K(field7))",                                                                                           //Exists
		"!model.field7":                                  "ISNULL(K(field7))",                                                                                              //DoesNotExist
		"model.field7=2024-10-14T22:47:31+03:00":         "EQ(K(field7),V(2024-10-14T22:47:31+03:00))",                                                                     //Equals
		"model.field7 in (2024-10-14T22:47:31+03:00)":    "IN(K(field7),V(2024-10-14T22:47:31+03:00))",                                                                     //In
		"model.field7!=2024-10-14T22:47:31+03:00":        "OR(ISNULL(K(field7)),NOTEQ(K(field7),V(2024-10-14T22:47:31+03:00)))",                                            //NotEquals
		"model.field7 notin (2024-10-14T22:47:31+03:00)": "OR(ISNULL(K(field7)),NOTIN(K(field7),V(2024-10-14T22:47:31+03:00)))",                                            //NotIn
		"model.field7>2024-10-14T22:47:31+03:00":         "GT(K(field7),V(2024-10-14T22:47:31+03:00))",                                                                     //GreaterThan
		"model.field7>=2024-10-14T22:47:31+03:00":        "GTE(K(field7),V(2024-10-14T22:47:31+03:00))",                                                                    //GreaterThanOrEquals
		"model.field7<2024-10-14T22:47:31+03:00":         "LT(K(field7),V(2024-10-14T22:47:31+03:00))",                                                                     //LessThan
		"model.field7<=2024-10-14T22:47:31+03:00":        "LTE(K(field7),V(2024-10-14T22:47:31+03:00))",                                                                    //LessThanOrEquals
		"customfield2!=2024-10-14T22:47:31+03:00":        "OR(ISNULL(K(goodfield ->> 'key')),NOTEQ(CAST(K(goodfield ->> 'key'), timestamp),V(2024-10-14T22:47:31+03:00)))", //NotEquals + JSONB cast

		// Arrays
		"model.field12[0]":                            "ISNOTNULL(K(field12[1]))",                                         //Exists
		"model.field12[0]=text":                       "EQ(K(field12[1]),V(text))",                                        //Equals
		"!model.field12[1]":                           "ISNULL(K(field12[2]))",                                            //DoesNotExist
		"model.field8[2]>1":                           "GT(K(field8[3]),V(1))",                                            //GreaterThan
		"model.field15[0]>=2024-10-14T22:47:31+03:00": "GTE(K(field15[1]),V(2024-10-14T22:47:31+03:00))",                  //GreaterThanOrEquals
		"model.field12":                               "ISNOTNULL(K(field12))",                                            //Exists
		"!model.field12":                              "ISNULL(K(field12))",                                               //DoesNotExist
		"model.field12 in (text1,text2)":              "OVERLAPS(K(field12),V(text1),V(text2))",                           //In
		"model.field12 notin (text1,text2)":           "OR(ISNULL(K(field12)),NOTOVERLAPS(K(field12),V(text1),V(text2)))", //NotIn
		"model.field12@>text":                         "CONTAINS(K(field12),V(text))",                                     //Contains
		"model.field12!@text":                         "NOTCONTAINS(K(field12),V(text))",                                  //NotContains

		// JSONB
		"model.field16":                             "ISNOTNULL(K(field16))",                                              //Exists
		"model.field16.some.key":                    "ISNOTNULL(K(field16 -> 'some' -> 'key'))",                           //Exists
		"!model.field16":                            "ISNULL(K(field16))",                                                 //DoesNotExist
		"model.field16=\"text\"":                    "EQ(K(field16),V(\"text\"))",                                         //Equals
		"model.field16={\"some\":\"text\"}":         "EQ(K(field16),V({\"some\":\"text\"}))",                              //Equals
		"model.field16.some.key.val=\"text\"":       "EQ(K(field16 -> 'some' -> 'key' -> 'val'),V(\"text\"))",             //Equals
		"model.field16==\"text\"":                   "EQ(K(field16),V(\"text\"))",                                         //DoubleEquals
		"model.field16 in (\"text1\",\"text2\")":    "IN(K(field16),V(\"text1\"),V(\"text2\"))",                           //In
		"model.field16!=\"text\"":                   "OR(ISNULL(K(field16)),NOTEQ(K(field16),V(\"text\")))",               //NotEquals
		"model.field16 notin (\"text1\",\"text2\")": "OR(ISNULL(K(field16)),NOTIN(K(field16),V(\"text1\"),V(\"text2\")))", //NotIn
		"model.field16@>{\"a\":\"b\"}":              "JSONB_CONTAINS(K(field16),V({\"a\":\"b\"}))",                        //Contains
		"model.field16!@{\"a\":\"b\"}":              "JSONB_NOTCONTAINS(K(field16),V({\"a\":\"b\"}))",                     //NotContains
		"model.field16.some.array[0]":               "ISNOTNULL(K(field16 -> 'some' -> 'array' -> 0))",                    //Exists + array index
		"model.field16.some.array[12].val=\"text\"": "EQ(K(field16 -> 'some' -> 'array' -> 12 -> 'val'),V(\"text\"))",     //Equals + array index

		// Multiple requirements
		"model.field1, model.field1 notin (true,false)": "AND(ISNOTNULL(K(field1)),OR(ISNULL(K(field1)),NOTIN(K(field1),V(false),V(true))))",                     // Exists + NotIn
		"model.field2 >= 0, model.field2 <= 10":         "AND(GTE(K(field2),V(0)), LTE(K(field2),V(10)))",                                                        // GreaterThanOrEquals + LessThanOrEquals
		"model.field6 != text1, model.field6 != text2":  "AND(OR(ISNULL(K(field6)),NOTEQ(K(field6),V(text1))), OR(ISNULL(K(field6)),NOTEQ(K(field6),V(text2))))", // NotEquals

		// Manual resolved selectors
		"mappedselector=test":                    "OR(EQ(K(field6),V(test)),EQ(K(field17),V(test)))",
		"customfield1=text":                      "EQ(K(goodfield),V(text))",
		"customfield2=2024-10-14T22:47:31+03:00": "EQ(CAST(K(goodfield ->> 'key'), timestamp),V(2024-10-14T22:47:31+03:00))",
		"customfield3=\"text\"":                  "EQ(K(goodfield -> 'key'),V(\"text\"))",
		"!customfield4.some.array[5]":            "ISNULL(K(goodfield -> 'some' -> 'array' ->> 5))",
		"customfield5.approved = true":           "EQ(CAST(K(goodfield -> 'path' ->> 'approved'), boolean),V(true))",
	}

	testBadOperations := []string{
		// Booleans
		"model.field1@>true",  //Contains
		"model.field1!@true",  //NotContains
		"model.field1>1",      //GreaterThan
		"model.field1>=1",     //GreaterThanOrEquals
		"model.field1<1",      //LessThan
		"model.field1<=1",     //LessThanOrEquals
		"model.field1[0]",     //Not JSONB + array
		"model.field1.val[0]", //Not JSONB + array

		// Numbers
		"model.field2@>1", //Contains
		"model.field2!@1", //NotContains

		// Strings
		"model.field6>1",                     //GreaterThan
		"model.field6>=1",                    //GreaterThanOrEquals
		"model.field6<1",                     //LessThan
		"model.field6<=1",                    //LessThanOrEquals
		"customfield4.some.array[5] @> test", //Partial string matching when the field is of type JSONB

		// Timestamps
		"model.field7@>2024-10-14T22:47:31+03:00", //Contains
		"model.field7!@2024-10-14T22:47:31+03:00", //NotContains

		// Arrays
		"model.field12[-2]",      //Invalid index
		"model.field12[]",        //Invalid selector
		"model.field12[0",        //Invalid selector
		"[model.field12[0",       //Invalid selector
		"model.[field12]",        //Invalid selector
		"model.field12[0]@>text", //Partial string matching is not supported
		"model.field12=text",     //Equals
		"model.field12==text",    //DoubleEquals
		"model.field12!=text",    //NotEquals
		"model.field12>1",        //GreaterThan
		"model.field12>=1",       //GreaterThanOrEquals
		"model.field12<1",        //LessThan
		"model.field12<=1",       //LessThanOrEquals

		// JSONB
		"model.field16>1",              //GreaterThan
		"model.field16>=1",             //GreaterThanOrEquals
		"model.field16<1",              //LessThan
		"model.field16<=1",             //LessThanOrEquals
		"model.field16=notjson",        //LessThanOrEquals
		"model.field16.some.arr[ay[0]", //Invalid JSONB selector
		"[model.field16.some.array[0]", //Invalid JSONB selector
		"model.field16.some.array[0",   //Invalid JSONB selector

		// Explicit casting for JSONB fields is not supported
		"model.field16.test::",
		"model.field16.test::boolean",

		// Unknown selector
		"unknownfield=test",

		// Bad selectors
		"model.field16.badfield$$=text",
	}

	f, err := NewFieldSelector(&goodTestModel{})
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
