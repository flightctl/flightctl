package domain

import (
	"reflect"

	"github.com/flightctl/flightctl/internal/util"
)

// FleetSpecsAreEqual compares two FleetSpec objects for semantic equality,
// with special handling for union types (discriminated unions).
func FleetSpecsAreEqual(f1, f2 FleetSpec) bool {
	return util.DeepEqualWithUnionHandling(reflect.ValueOf(f1), reflect.ValueOf(f2))
}

// DeviceSpecsAreEqual compares two DeviceSpec objects for semantic equality,
// with special handling for union types (discriminated unions).
func DeviceSpecsAreEqual(d1, d2 DeviceSpec) bool {
	return util.DeepEqualWithUnionHandling(reflect.ValueOf(d1), reflect.ValueOf(d2))
}
