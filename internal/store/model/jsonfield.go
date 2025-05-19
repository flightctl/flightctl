package model

import (
	"bytes"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
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

// JSONMap represents a generic map that can be stored as JSONB in PostgreSQL.
type JSONMap[K comparable, V any] map[K]V

// MakeJSONMap initializes a JSONMap from a standard map.
func MakeJSONMap[K comparable, V any](src map[K]V) JSONMap[K, V] {
	if src == nil {
		return make(JSONMap[K, V])
	}
	return JSONMap[K, V](src)
}

// Scan implements the sql.Scanner interface for reading from PostgreSQL.
func (m *JSONMap[K, V]) Scan(src interface{}) error {
	switch src := src.(type) {
	case []byte:
		return m.scanBytes(src)
	case string:
		return m.scanBytes([]byte(src))
	case nil:
		*m = nil
		return nil
	}

	return fmt.Errorf("pq: cannot convert %T to JSONMap[%T, %T]", src, new(K), new(V))
}

func (m *JSONMap[K, V]) scanBytes(src []byte) error {
	var result map[K]V
	if err := json.Unmarshal(src, &result); err != nil {
		return fmt.Errorf("pq: could not parse JSONB: %w", err)
	}

	*m = result
	return nil
}

// Value implements the driver.Valuer interface for writing to PostgreSQL.
func (m JSONMap[K, V]) Value() (driver.Value, error) {
	if m == nil {
		return nil, nil
	}

	jsonData, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("pq: could not marshal map to JSONB: %w", err)
	}

	return jsonData, nil
}

func (m JSONMap[K, V]) Equals(other JSONMap[K, V]) bool {
	return reflect.DeepEqual(m, other)
}
