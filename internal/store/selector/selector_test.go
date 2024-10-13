package selector

import (
	"strings"
	"testing"
	"time"
)

type Model struct {
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
}

type BadModel struct {
	Field1 bool `selector:"model.field"` // conflict
	Field2 bool `selector:"model.field.mine"`
}

func (m *Model) ResolveCustomSelector(field SelectorFieldName) []SelectorFieldName {
	if strings.EqualFold("manualfield", string(field)) {
		return []SelectorFieldName{"model.field6", "model.field16.val::string"}
	}
	return nil
}

func (m *Model) ListCustomSelectors() []SelectorFieldName {
	return []SelectorFieldName{"manualfield"}
}

func TestResolveSchemaFields(t *testing.T) {
	_, err := ResolveFieldsFromSchema(&Model{})
	if err != nil {
		t.Errorf("good model got error %v (%#v)\n", err, err)
	}

	_, err = ResolveFieldsFromSchema(&BadModel{})
	if err == nil {
		t.Errorf("bad model did not get expected error\n")
	}
}

func TestResolveFields(t *testing.T) {
	fr, err := SelectorFieldResolver(&Model{})
	if err != nil {
		t.Errorf("%v: error %v (%#v)\n", fr, err, err)
		return
	}

	selectors := fr.ListSelectors()
	for _, selector := range selectors {
		if _, err := fr.ResolveFields(selector); err != nil {
			t.Errorf("%s: error %v (%#v)\n", selector, err, err)
			continue
		}

		if _, err := fr.ResolveNames(selector); err != nil {
			t.Errorf("%s: error %v (%#v)\n", selector, err, err)
		}
	}
}
