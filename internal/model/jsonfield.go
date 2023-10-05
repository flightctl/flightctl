package model

import (
	"bytes"
	"database/sql/driver"
	"encoding/json"
	"errors"
)

// from https://www.terminateandstayresident.com/2022-07-13/orm-json

// JSONField wraps an arbitrary struct so that it can be included in a GORM model, for use in a JSON/JSONB field
type JSONField[T any] struct {
	Data T
}

// Return a copy of 'data', wrapped in a JSONField object
func MakeJSONField[T any](data T) *JSONField[T] {
	return &JSONField[T]{
		Data: data,
	}
}

func (j *JSONField[T]) Scan(src any) error {
	if src == nil {
		var empty T
		j.Data = empty
		return nil
	}
	srcByte, ok := src.([]byte)
	if !ok {
		return errors.New("JSONField underlying type must be []byte (some kind of Blob/JSON/JSONB field)")
	}
	if err := json.Unmarshal(srcByte, &j.Data); err != nil {
		return err
	}
	return nil
}

func (j JSONField[T]) Value() (driver.Value, error) {
	return json.Marshal(j.Data)
}

func (j JSONField[T]) MarshalJSON() ([]byte, error) {
	return json.Marshal(j.Data)
}

func (j *JSONField[T]) UnmarshalJSON(b []byte) error {
	if bytes.Equal(b, []byte("null")) {
		// According to docs, this is a no-op by convention
		//var empty T
		//j.Data = empty
		return nil
	}
	if err := json.Unmarshal(b, &j.Data); err != nil {
		return err
	}
	return nil
}
