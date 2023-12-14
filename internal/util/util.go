package util

import (
	"encoding/json"
	"fmt"
	"time"
)

type StringerWithError func() (string, error)

func Must(err error) {
	if err != nil {
		panic(fmt.Errorf("internal error: %w", err))
	}
}

func MustString(fn StringerWithError) string {
	s, err := fn()
	if err != nil {
		panic(fmt.Errorf("internal error: %w", err))
	}
	return s
}

func Default(s string, defaultS string) string {
	if s == "" {
		return defaultS
	}
	return s
}

func DefaultIfError(fn StringerWithError, defaultS string) string {
	s, err := fn()
	if err != nil {
		return defaultS
	}
	return s
}

func DefaultIfNil(s *string, defaultS string) string {
	if s == nil {
		return defaultS
	}
	return *s
}

func StrToPtr(s string) *string {
	return &s
}

func SingleQuote(input []string) []string {
	output := make([]string, len(input))
	for i, val := range input {
		output[i] = fmt.Sprintf("'%s'", val)
	}
	return output
}

type Duration time.Duration

func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	err := json.Unmarshal(b, &s)
	if err != nil {
		return err
	}

	duration, err := time.ParseDuration(s)
	if err != nil {
		return err
	}

	*d = Duration(duration)
	return nil
}
