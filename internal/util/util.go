package util

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/samber/lo"
	"golang.org/x/exp/constraints"
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

func DefaultString(s string, defaultS string) string {
	if s == "" {
		return defaultS
	}
	return s
}

func StringsAreEqual(a, b *string) bool {
	if a == nil || b == nil {
		return a == b // returns true if both are nil, false if only one is nil
	}
	return *a == *b // dereference and compare the actual string values
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

func IsEmptyString(s *string) bool {
	if s == nil {
		return true
	}
	return len(*s) == 0
}

func DefaultBoolIfNil(b *bool, defaultB bool) bool {
	if b == nil {
		return defaultB
	}
	return *b
}

func ToPtrWithNilDefault[T comparable](i T) *T {
	if lo.IsEmpty(i) {
		return nil
	}
	return &i
}

func SliceToPtrWithNilDefault(s []string) *[]string {
	var defaultSlice []string
	if len(s) == 0 {
		return nil
	}
	if len(s) == len(defaultSlice) {
		return nil
	}
	return &s
}

func TimeStampString() string {
	return time.Now().Format("20060102-150405-999999999")
}

func TimeStampStringPtr() *string {
	return lo.ToPtr(TimeStampString())
}

func BoolToStr(b bool, ifTrue string, ifFalse string) string {
	if b {
		return ifTrue
	}
	return ifFalse
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

func StructToMap(obj interface{}) (map[string]interface{}, error) {
	data, err := json.Marshal(obj)
	if err != nil {
		return nil, err
	}

	var result map[string]interface{}
	err = json.Unmarshal(data, &result)
	if err != nil {
		return nil, err
	}

	return result, nil
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

func (d Duration) String() string {
	return time.Duration(d).String()
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

func ResourceOwner(kind string, name string) string {
	return fmt.Sprintf("%s/%s", kind, name)
}

func SetResourceOwner(kind string, name string) *string {
	return lo.ToPtr(ResourceOwner(kind, name))
}

func GetResourceOwner(owner *string) (string, string, error) {
	if owner == nil {
		return "", "", fmt.Errorf("owner string is nil")
	}
	parts := strings.Split(*owner, "/")
	if len(parts) != 2 {
		return "", "", fmt.Errorf("invalid owner string: %s", *owner)
	}

	return parts[0], parts[1], nil
}

func LabelsMatchLabelSelector(labels map[string]string, labelSelector map[string]string) bool {
	// Empty label selector matches nothing (we do not want the kubernetes behavior here)
	if len(labelSelector) == 0 {
		return false
	}
	for selectorKey, selectorVal := range labelSelector {
		labelVal, ok := labels[selectorKey]
		if !ok {
			return false
		}
		if labelVal != selectorVal {
			return false
		}
	}
	return true
}

func OwnerQueryParamsToArray(ownerQueryParam *string) []string {
	owners := []string{}
	if ownerQueryParam != nil && len(*ownerQueryParam) > 0 {
		owners = strings.Split(*ownerQueryParam, ",")
	}
	return owners
}

func DefaultIfNotInMap(m map[string]string, key string, def string) string {
	val, ok := m[key]
	if !ok {
		return def
	}
	return val
}

// EnsureMap takes a map as input and guarantees that it is not nil.
// If the input map is nil, it initializes and returns a new empty map of the same type.
// If the input map is already initialized, it simply returns the map unchanged.
func EnsureMap[T comparable, U any](m map[T]U) map[T]U {
	if m == nil {
		return make(map[T]U)
	}
	return m
}

type Number interface {
	constraints.Integer | constraints.Float
}

func Min[N Number](n1, n2 N) N {
	return lo.Ternary(n1 < n2, n1, n2)
}

func Max[N Number](n1, n2 N) N {
	return lo.Ternary(n1 > n2, n1, n2)
}

func Abs[N Number](n N) N {
	return lo.Ternary(n > 0, n, -n)
}

func GetFromMap[K comparable, V any](in map[K]V, key K) (V, bool) {
	if in == nil {
		return lo.Empty[V](), false
	}
	v, ok := in[key]
	if !ok {
		return lo.Empty[V](), false
	}
	return v, true
}

type Singleton[T any] struct {
	value atomic.Pointer[T]
}

func (s *Singleton[T]) Instance() *T {
	var empty T
	return s.GetOrInit(&empty)
}

func (s *Singleton[T]) GetOrInit(t *T) *T {
	_ = s.value.CompareAndSwap(nil, t)
	return s.value.Load()
}

func Clone[T any](t *T) *T {
	if t == nil {
		return nil
	}
	ret := *t
	return &ret
}
