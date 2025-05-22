package resources

import "fmt"

type FieldSelectorOperator int

const (
	Exists FieldSelectorOperator = iota + 1
	DoesNotExist
	Equals
	DoubleEquals
	NotEquals
	GreaterThan
	GreaterThanOrEquals
	LessThan
	LessThanOrEquals
	In
	NotIn
	Contains
	NotContains
)

func (o FieldSelectorOperator) String() string {
	return [...]string{
		" exists ",
		"!",
		"=",
		"==",
		"!=",
		">",
		">=",
		"<",
		"<=",
		" in ",
		" notin ",
		" contains ",
		" notcontains ",
	}[o-1]
}

func (o FieldSelectorOperator) EnumIndex() int {
	return int(o)
}

var operatorMap = map[string]FieldSelectorOperator{
	"Exists":              Exists,
	"DoesNotExist":        DoesNotExist,
	"Equals":              Equals,
	"DoubleEquals":        DoubleEquals,
	"NotEquals":           NotEquals,
	"GreaterThan":         GreaterThan,
	"GreaterThanOrEquals": GreaterThanOrEquals,
	"LessThan":            LessThan,
	"LessThanOrEquals":    LessThanOrEquals,
	"In":                  In,
	"NotIn":               NotIn,
	"Contains":            Contains,
	"NotContains":         NotContains,
}

func ToFieldSelectorOperator(operator string) (FieldSelectorOperator, error) {
	if op, found := operatorMap[operator]; found {
		return op, nil
	}
	return -1, fmt.Errorf("unknown operator: %s", operator)
}
