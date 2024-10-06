package queryparser

import (
	"fmt"
	"reflect"
	"strings"
	"sync"
)

// Set is a generic set implementation using a map where the keys are of type T
// and the values are empty structs to save memory. It includes a mutex for thread safety.
type Set[T comparable] struct {
	set map[T]struct{}
	mu  sync.RWMutex
}

// NewSet creates and returns a new empty Set of type T.
func NewSet[T comparable]() *Set[T] {
	return &Set[T]{
		set: make(map[T]struct{}),
	}
}

// Add inserts one or more values into the Set.
// It returns the modified Set to allow for method chaining.
func (s *Set[T]) Add(values ...T) *Set[T] {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, v := range values {
		s.set[v] = struct{}{}
	}
	return s
}

// Contains checks if a value exists in the Set.
func (s *Set[T]) Contains(value T) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, exists := s.set[value]
	return exists
}

// Remove deletes a value from the Set.
func (s *Set[T]) Remove(value T) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.set, value)
}

// Print returns a string representation of the Set,
// with elements separated by commas. It assumes T implements fmt.Stringer.
func (s *Set[T]) Print() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var sb strings.Builder
	for value := range s.set {
		if sb.Len() > 0 {
			sb.WriteString(", ")
		}
		sb.WriteString(fmt.Sprintf("%v", value))
	}
	return sb.String()
}

// Size returns the number of elements in the Set.
func (s *Set[T]) Size() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.set)
}

// List returns a slice containing all elements in the Set.
func (s *Set[T]) List() []T {
	s.mu.RLock()
	defer s.mu.RUnlock()

	res := make([]T, 0, len(s.set))
	for value := range s.set {
		res = append(res, value)
	}
	return res
}

// AssertType attempts to assert the input to the specified type T
func AssertType[T any](input any) (T, error) {
	var zeroValue T // declare a zero value of type T
	strInput, ok := input.(T)
	if !ok {
		return zeroValue, fmt.Errorf("expected input of type %s, got %s",
			reflect.TypeOf(zeroValue).String(), reflect.TypeOf(input).String())
	}
	return strInput, nil
}

// AssertSliceType asserts that each element of the input slice is of type T and returns a slice of type T.
func AssertSliceType[T any](input any) ([]T, error) {
	inputValue := reflect.ValueOf(input)
	if inputValue.Kind() != reflect.Slice {
		return nil, fmt.Errorf("expected a slice, got %s", inputValue.Kind())
	}

	// Create a slice to hold the result
	result := make([]T, inputValue.Len())

	// Iterate over the input slice and assert each element's type
	for i := 0; i < inputValue.Len(); i++ {
		elem := inputValue.Index(i).Interface()
		typedElem, ok := elem.(T)
		if !ok {
			return nil, fmt.Errorf("expected element of type %s, got %s",
				reflect.TypeOf(result).Elem(), reflect.TypeOf(elem))
		}
		result[i] = typedElem
	}

	return result, nil
}
