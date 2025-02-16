package selector

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	gormschema "gorm.io/gorm/schema"
)

type goodTestModel struct {
	Field1  bool        `selector:"model.field1"`                          // Boolean
	Field2  int         `selector:"model.field2"`                          // Integer
	Field5  float64     `selector:"model.field5"`                          // Float
	Field6  string      `selector:"model.field6"`                          // Text
	Field7  time.Time   `selector:"model.field7"`                          // Timestamp
	Field8  []int       `gorm:"type:integer[]" selector:"model.field8"`    // Integer Array
	Field9  []int16     `gorm:"type:smallint[]" selector:"model.field9"`   // Small Integer Array
	Field10 []int64     `gorm:"type:bigint[]" selector:"model.field10"`    // Big Integer Array
	Field11 []bool      `gorm:"type:boolean[]" selector:"model.field11"`   // Boolean Array
	Field12 []string    `gorm:"type:text[]" selector:"model.field12"`      // Text Array
	Field13 []float64   `gorm:"type:real[]" selector:"model.field13"`      // Float Array
	Field15 []time.Time `gorm:"type:timestamp[]" selector:"model.field15"` // Timestamp Array
	Field16 string      `gorm:"type:jsonb" selector:"model.field16"`       // JSONB
	Field17 string      `selector:"model.field17"`                         // Text
	Field18 uuid.UUID   `gorm:"type:uuid" selector:"model.field18"`        // UUID
}

func (m *goodTestModel) MapSelectorName(selector SelectorName) []SelectorName {
	if strings.EqualFold("mappedselector", selector.String()) {
		return []SelectorName{
			NewSelectorName("model.field6"),
			NewSelectorName("model.field17"),
		}
	}
	return nil
}

func (m *goodTestModel) ResolveSelector(selector SelectorName) (*SelectorField, error) {
	if strings.EqualFold("customfield1", selector.String()) {
		return &SelectorField{
			Type:      String,
			FieldName: "goodfield",
			FieldType: gormschema.String,
		}, nil
	}
	if strings.EqualFold("customfield2", selector.String()) {
		return &SelectorField{
			Type:      Timestamp,
			FieldName: "goodfield.key",
			FieldType: "jsonb",
		}, nil
	}
	if strings.EqualFold("customfield3", selector.String()) {
		return &SelectorField{
			Type:      Jsonb,
			FieldName: "goodfield.key",
			FieldType: "jsonb",
		}, nil
	}
	if strings.EqualFold("customfield4.some.array[5]", selector.String()) {
		return &SelectorField{
			Type:      String,
			FieldName: "goodfield.some.array[5]",
			FieldType: "jsonb",
		}, nil
	}
	if strings.EqualFold("customfield5.approved", selector.String()) {
		return &SelectorField{
			Type:      Bool,
			FieldName: "goodfield.path.approved",
			FieldType: "jsonb",
		}, nil
	}
	return nil, nil
}

func (m *goodTestModel) ListSelectors() SelectorNameSet {
	return NewSelectorFieldNameSet().Add(
		NewSelectorName("mappedselector"),
		NewSelectorName("customfield1"),
		NewSelectorName("customfield2"),
		NewSelectorName("customfield3"),
		NewSelectorName("customfield4.some.array[5]"),
		NewSelectorName("customfield5.approved"),
	)
}

type badTestModel struct {
	GoodSelector string `selector:"model.good.selector"` // Text
}

func (m *badTestModel) MapSelectorName(selector SelectorName) []SelectorName {
	if strings.EqualFold("mappedselector", selector.String()) {
		return []SelectorName{
			NewSelectorName("model.good.selector"),
			NewSelectorName("unknownselector"),
		}
	}
	return nil
}

func (m *badTestModel) ResolveSelector(selector SelectorName) (*SelectorField, error) {
	if strings.EqualFold("customfield4", selector.String()) {
		return &SelectorField{
			Type:      TextArray, //Not supported
			FieldName: "badfield.key",
			FieldType: "jsonb",
		}, nil
	}
	if strings.EqualFold("customfield5", selector.String()) {
		return &SelectorField{
			Type:      100, //Not supported
			FieldName: "badfield.key",
			FieldType: "jsonb",
		}, nil
	}
	return nil, nil
}

func (m *badTestModel) ListSelectors() SelectorNameSet {
	return NewSelectorFieldNameSet().Add(
		NewSelectorName("mappedselector"),
		NewSelectorName("customfield4"),
		NewSelectorName("customfield5"),
	)
}

type conflictTestModel struct {
	Field1 bool `selector:"model.field"` // conflict
	Field2 bool `selector:"model.field.mine"`
}

func TestResolveSchemaFields(t *testing.T) {
	_, err := ResolveFieldsFromSchema(&goodTestModel{})
	if err != nil {
		t.Errorf("good model got error %v (%#v)\n", err, err)
	}

	_, err = ResolveFieldsFromSchema(&conflictTestModel{})
	if err == nil {
		t.Errorf("conflict model did not get expected error\n")
	}
}

func TestResolveFields(t *testing.T) {
	goodmodel := &goodTestModel{}
	fr, err := SelectorFieldResolver(goodmodel)
	if err != nil {
		t.Errorf("%v: error %v (%#v)\n", fr, err, err)
		return
	}

	selectors := fr.List()
	for _, selector := range selectors {
		if fields, err := fr.ResolveFields(selector); err != nil {
			t.Errorf("%s: error %v (%#v)\n", selector, err, err)
			continue
		} else if len(fields) == 0 {
			t.Errorf("%s: error expected resolved fields\n", selector)
			continue
		}

		if names, err := fr.ResolveNames(selector); err != nil {
			t.Errorf("%s: error %v (%#v)\n", selector, err, err)
		} else if len(names) == 0 {
			t.Errorf("%s: error expected resolved names\n", selector)
			continue
		}
	}

	if f, _ := fr.ResolveFields(NewSelectorName("unknownselector")); len(f) > 0 {
		t.Errorf("unknownselector: did not get expected error\n")
	}

	badmodel := &badTestModel{}
	fr, err = SelectorFieldResolver(badmodel)
	if err != nil {
		t.Errorf("%v: error %v (%#v)\n", fr, err, err)
		return
	}

	selectors = slices.DeleteFunc(fr.List(), func(sn SelectorName) bool {
		return strings.EqualFold(sn.String(), "model.good.selector")
	})
	for _, selector := range selectors {
		if fields, err := fr.ResolveFields(selector); err == nil && len(fields) > 0 {
			t.Errorf("%s: did not get expected error\n", selector)
			continue
		}
	}
}
