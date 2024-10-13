package selector

import (
	"reflect"
	"regexp"

	"github.com/flightctl/flightctl/pkg/k8s/selector/selection"
	gormschema "gorm.io/gorm/schema"
)

const (
	Unknown = iota
	Bool
	Int
	SmallInt
	BigInt
	Float
	String
	Timestamp
	BoolArray
	IntArray
	SmallIntArray
	BigIntArray
	FloatArray
	TextArray
	TimestampArray
	Jsonb
)

var schemaTypeResolution = map[gormschema.DataType]SelectorFieldType{
	gormschema.Bool:   Bool,
	gormschema.Int:    Int,
	gormschema.Float:  Float,
	gormschema.String: String,
	gormschema.Time:   Timestamp,
	"boolean[]":       BoolArray,
	"integer[]":       IntArray,
	"smallint[]":      SmallIntArray,
	"bigint[]":        BigIntArray,
	"real[]":          FloatArray,
	"text[]":          TextArray,
	"timestamp[]":     TimestampArray,
	"jsonb":           Jsonb,
}

var operatorsMap = map[selection.Operator]string{
	selection.Exists:              "ISNOTNULL",
	selection.DoesNotExist:        "ISNULL",
	selection.Equals:              "EQ",
	selection.DoubleEquals:        "EQ",
	selection.NotEquals:           "NOTEQ",
	selection.Contains:            "LIKE",
	selection.NotContains:         "NOTLIKE",
	selection.In:                  "IN",
	selection.NotIn:               "NOTIN",
	selection.LessThan:            "LT",
	selection.LessThanOrEquals:    "LTE",
	selection.GreaterThan:         "GT",
	selection.GreaterThanOrEquals: "GTE",
}

var arrayPattern = regexp.MustCompile(`^[A-Za-z0-9_.]+\[\d+\]$`)

// SelectorFieldName represents the name of a field used in a selector.
type SelectorFieldName string

func (sf SelectorFieldName) String() string {
	return string(sf)
}

type SelectorFieldType int

func (t SelectorFieldType) IsArray() bool {
	switch t {
	case BoolArray, IntArray, SmallIntArray, BigIntArray, FloatArray, TextArray, TimestampArray:
		return true
	default:
		return false
	}
}

func (t SelectorFieldType) ArrayType() SelectorFieldType {
	if !t.IsArray() {
		return Unknown
	}

	switch t {
	case BoolArray:
		return Bool
	case IntArray:
		return Int
	case SmallIntArray:
		return SmallInt
	case BigIntArray:
		return BigInt
	case FloatArray:
		return Float
	case TextArray:
		return String
	case TimestampArray:
		return Timestamp
	default:
		return Unknown
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
	case Timestamp:
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

// IsArrayElement returns true if the field is an element within an array.
func (sf *SelectorField) IsArrayElement() bool {
	// Check if the schema type exists in the resolution map
	t, exists := schemaTypeResolution[sf.DataType]
	if !exists {
		return false
	}

	// Check if the schema type is an array and the array type matches the field's type
	return t.IsArray() && t.ArrayType() == sf.Type
}
