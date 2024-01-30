package util

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"k8s.io/klog/v2"
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

func Int64ToPtr(i int64) *int64 {
	return &i
}

func TimeStampStringPtr() *string {
	return StrToPtr(time.Now().Format(time.RFC3339))
}

func SingleQuote(input []string) []string {
	output := make([]string, len(input))
	for i, val := range input {
		output[i] = fmt.Sprintf("'%s'", val)
	}
	return output
}

func LabelMapToArray(labels *map[string]string) []string {
	if labels == nil {
		return []string{}
	}
	output := make([]string, len(*labels))
	i := 0
	for key, val := range *labels {
		output[i] = fmt.Sprintf("%s=%s", key, val)
		i++
	}
	return output
}

func splitLabel(label string) (string, string, error) {
	parts := strings.SplitN(label, "=", 2)

	switch {
	case len(parts) > 2:
		return "", "", fmt.Errorf("invalid label: %s", label)
	case len(parts) == 1:
		return parts[0], "", nil
	case len(parts) == 2:
		return parts[0], parts[1], nil
	}
	return "", "", nil
}

func LabelArrayToMap(labels []string) map[string]string {
	output := make(map[string]string)
	for _, label := range labels {
		key, val, err := splitLabel(label)
		// if our serialized labels have a weird format (DB has been manipulated directly), skip them
		// eventually will get overwriten by correct labels on next update.
		if err != nil {
			klog.Errorf("invalid label in array: %q, must be in the xxxx=yyy or xxxx, or xxxx= format", label)
		}
		if key == "" && val == "" {
			continue
		}
		output[key] = val
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

func MergeLabels(labels ...map[string]string) map[string]string {
	result := make(map[string]string)
	for _, l := range labels {
		for k, v := range l {
			result[k] = v
		}
	}
	return result
}
