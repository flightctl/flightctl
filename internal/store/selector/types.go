package selector

import (
	"reflect"

	gormschema "gorm.io/gorm/schema"
	"k8s.io/apimachinery/pkg/selection"
)

const (
	Bool = iota
	Int
	SmallInt
	BigInt
	Float
	String
	Time
	BoolArray
	IntArray
	SmallIntArray
	BigIntArray
	FloatArray
	TextArray
	TimestampArray
	Jsonb
)

var operatorsMap = map[selection.Operator]string{
	selection.Exists:       "ISNOTNULL",
	selection.DoesNotExist: "ISNULL",
	selection.Equals:       "EQ",
	selection.DoubleEquals: "EQ",
	selection.NotEquals:    "NOTEQ",
	selection.In:           "IN",
	selection.NotIn:        "NOTIN",
	selection.LessThan:     "LT",
	selection.GreaterThan:  "GT",
}

// SelectorFieldName represents the name of a field used in a selector.
type SelectorFieldName string

type SelectorFieldType int

func (t SelectorFieldType) IsArray() bool {
	switch t {
	case BoolArray, IntArray, SmallIntArray, BigIntArray, FloatArray, TextArray, TimestampArray:
		return true
	default:
		return false
	}
}

func (t SelectorFieldType) String() string {
	switch t {
	case Bool:
		return "boolean"
	case Int:
		return "integer"
	case SmallInt:
		return "smallint"
	case BigInt:
		return "bigint"
	case Float:
		return "real"
	case String:
		return "text"
	case Time:
		return "timestamp"
	case BoolArray:
		return "boolean[]"
	case IntArray:
		return "integer[]"
	case SmallIntArray:
		return "smallint[]"
	case BigIntArray:
		return "bigint[]"
	case FloatArray:
		return "real[]"
	case TextArray:
		return "text[]"
	case TimestampArray:
		return "timestamp[]"
	case Jsonb:
		return "jsonb"
	default:
		return "unknown"
	}
}

type SelectorField struct {
	DBName      string
	Type        SelectorFieldType
	DataType    gormschema.DataType
	StructField reflect.StructField
}

// IsJSONBCast returns true if the field's data type is 'jsonb' in the database and the expected type is not Jsonb.
func (sf *SelectorField) IsJSONBCast() bool {
	return sf.DataType == "jsonb" && sf.Type != Jsonb
}
