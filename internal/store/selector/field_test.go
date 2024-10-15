package selector

import (
	"context"
	"reflect"
	"strconv"
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

func TestFieldTypes(t *testing.T) {
	testGoodStrings := []string{
		"field1=true",                  // Boolean
		"field2=1",                     // Integer
		"field5=3.14",                  // Float
		"field6=Hello World",           // Text
		"field7=2024-10-14T15:04:05Z",  // Timestamp
		"field8=1",                     // Integer Array
		"field9=1",                     // Small Integer Array
		"field10=10000000000",          // Big Integer Array
		"field11=true",                 // Boolean Array
		"field12=First",                // Text Array
		"field13=1.1",                  // Float Array
		"field15=2024-10-14T15:04:05Z", // Timestamp Array
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
		"field1=true":  "EQ(K(field1),V(true))",                        //Equals
		"field1==true": "EQ(K(field1),V(true))",                        //DoubleEquals
		"field1!=true": "OR(ISNULL(K(field1)),NEQ(K(field1),V(true)))", //NotEquals

		// Numbers
		"field2=1":  "EQ(K(field2),V(1))",                        //Equals
		"field2==1": "EQ(K(field2),V(1))",                        //DoubleEquals
		"field2!=1": "OR(ISNULL(K(field2)),NEQ(K(field2),V(1)))", //NotEquals

		//Strings
		"field6=text":  "EQ(K(field6),V(text))",                        //Equals
		"field6==text": "EQ(K(field6),V(text))",                        //DoubleEquals
		"field6!=text": "OR(ISNULL(K(field6)),NEQ(K(field6),V(text)))", //NotEquals

		// Timestamps
		"field7=2024-10-14T22:47:31+03:00":  "EQ(K(field7),V(2024-10-14T22:47:31+03:00))",                        //Equals
		"field7==2024-10-14T22:47:31+03:00": "EQ(K(field7),V(2024-10-14T22:47:31+03:00))",                        //DoubleEquals
		"field7!=2024-10-14T22:47:31+03:00": "OR(ISNULL(K(field7)),NEQ(K(field7),V(2024-10-14T22:47:31+03:00)))", //NotEquals

		// Arrays
		"field12=text":  "CONTAINS(K(field12),V(text))",                         //Equals
		"field12==text": "CONTAINS(K(field12),V(text))",                         //DoubleEquals
		"field12!=text": "OR(ISNULL(K(field12)),NCONTAINS(K(field12),V(text)))", //NotEquals

		//JSONB
		"field16=text":  "EQ(K(field16),V(text))",                         //Equals
		"field16==text": "EQ(K(field16),V(text))",                         //DoubleEquals
		"field16!=text": "OR(ISNULL(K(field16)),NEQ(K(field16),V(text)))", //NotEquals

		// JSONB casting
		"field16.test::boolean=true":  "EQ(CAST(K(field16.test), boolean),V(true))",                              //Equals
		"field16.test::boolean==true": "EQ(CAST(K(field16.test), boolean),V(true))",                              //DoubleEquals
		"field16.test::boolean!=true": "OR(ISNULL(K(field16.test)),NEQ(CAST(K(field16.test), boolean),V(true)))", //NotEquals
	}

	testBadStrings := []string{
		// JSONB casting
		"field16.test::",
		"field16.test::unknown",
	}
	f, err := NewFieldSelector(&Model{})
	if err != nil {
		t.Errorf("%v: error %v (%#v)\n", f, err, err)
	}

	for k8s, qp := range testGoodOperations {
		selector, err := fields.ParseSelector(k8s)
		if err != nil {
			t.Errorf("%v: error %v (%#v)\n", k8s, err, err)
		}

		set1, err := f.Tokenize(ctx, selector)
		if err != nil {
			t.Errorf("%v: error %v (%#v)\n", k8s, err, err)
		}

		set2, err := queryparser.Tokenize(ctx, qp)
		if err != nil {
			t.Errorf("%v: error %v (%#v)\n", qp, err, err)
		}

		if !matchTokenset(set1, set2) {
			t.Errorf("%v not match %v\n", set1, set2)
		}
	}

	for _, test := range testBadStrings {
		_, _, err := f.ParseFromString(context.Background(), test)
		if err == nil {
			t.Errorf("%v: did not get expected error\n", test)
		}
	}

}

func matchTokenset(set1 queryparser.TokenSet, set2 queryparser.TokenSet) bool {
	if len(set1) != len(set2) {
		return false
	}

	for i, token := range set1 {
		switch token.Type {
		case queryparser.TokenFunc:
			if set2[i].Type != queryparser.TokenFunc {
				return false
			}
			if set2[i].Value.(string) != token.Value.(string) {
				return false
			}

		case queryparser.TokenFuncClose:
			if set2[i].Type != queryparser.TokenFuncClose {
				return false
			}

		case queryparser.TokenValue:
			if set2[i].Type != queryparser.TokenValue {
				return false
			}
			if toString(set1[i].Value) != toString(set2[i].Value) {
				return false
			}
		}
	}
	return true
}

func toString(v interface{}) string {
	val := reflect.ValueOf(v)
	switch val.Kind() {
	case reflect.Bool:
		return strconv.FormatBool(val.Bool())
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return strconv.FormatInt(val.Int(), 10)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return strconv.FormatUint(val.Uint(), 10)
	case reflect.Float32, reflect.Float64:
		return strconv.FormatFloat(val.Float(), 'f', -1, 64)
	case reflect.Struct:
		if val.Type() == reflect.TypeOf(time.Time{}) {
			return val.Interface().(time.Time).Format(time.RFC3339)
		}
	default:
		return val.String()
	}
	return ""
}
