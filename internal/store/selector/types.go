package selector

import (
	"regexp"
	"strings"

	"github.com/flightctl/flightctl/pkg/k8s/selector/selection"
	"github.com/flightctl/flightctl/pkg/queryparser"
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

var schemaTypeResolution = map[gormschema.DataType]SelectorType{
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

// SelectorNameMapping defines an interface for mapping a custom selector
// to one or more selectors defined for the model.
type SelectorNameMapping interface {
	// MapSelectorName maps a custom selector to one or more selectors.
	MapSelectorName(selector SelectorName) []SelectorName

	// ListSelectors returns all custom selectors.
	ListSelectors() SelectorNameSet
}

// SelectorResolver defines an interface for manually resolving a selector to specific
// SelectorField instances, enabling direct use of fields.
type SelectorResolver interface {
	// ResolveSelector manually resolves a selector to a SelectorField instance.
	ResolveSelector(selector SelectorName) (*SelectorField, error)

	// ListSelectors returns all custom selectors.
	ListSelectors() SelectorNameSet
}

// SelectorName represents the name of a selector.
type SelectorName string

func (sf SelectorName) TrimSpace() SelectorName {
	return SelectorName(strings.TrimSpace(sf.String()))
}

func (sf SelectorName) String() string {
	return string(sf)
}

// SelectorType represents the type of a selector.
type SelectorType int

func (t SelectorType) IsArray() bool {
	switch t {
	case BoolArray, IntArray, SmallIntArray, BigIntArray, FloatArray, TextArray, TimestampArray:
		return true
	default:
		return false
	}
}

func (t SelectorType) ArrayType() SelectorType {
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

func (t SelectorType) String() string {
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
	Name      SelectorName
	Type      SelectorType
	FieldName string
	FieldType gormschema.DataType
}

// IsJSONBCast returns true if the field's data type is 'jsonb' and the expected type is not Jsonb.
func (sf *SelectorField) IsJSONBCast() bool {
	return sf.FieldType == "jsonb" && sf.Type != Jsonb
}

// IsArrayElement returns true if the selector is an element within an array.
func (sf *SelectorField) IsArrayElement() bool {
	// Check if the schema type exists in the resolution map
	t, exists := schemaTypeResolution[sf.FieldType]
	if !exists {
		return false
	}

	// Check if the schema type is an array and the array type matches the selector's type
	return t.IsArray() && t.ArrayType() == sf.Type
}

type SelectorNameSet struct {
	*queryparser.Set[SelectorName]
}

// NewSelectorFieldNameSet initializes a new SelectorNameSet.
func NewSelectorFieldNameSet() SelectorNameSet {
	return SelectorNameSet{queryparser.NewSet[SelectorName]()}
}

// Add is a wrapper for the embedded Set's Add method that returns SelectorNameSet.
func (s SelectorNameSet) Add(items ...SelectorName) SelectorNameSet {
	s.Set.Add(items...)
	return s
}
