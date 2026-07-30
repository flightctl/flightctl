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

// CatalogSpecsAreEqual compares two CatalogSpec objects for semantic equality.
func CatalogSpecsAreEqual(c1, c2 CatalogSpec) bool {
	return util.DeepEqualWithUnionHandling(reflect.ValueOf(c1), reflect.ValueOf(c2))
}

// CatalogItemSpecsAreEqual compares two CatalogItemSpec objects for semantic
// equality, normalizing JSON round-trip artifacts such as nil vs. empty slices.
func CatalogItemSpecsAreEqual(c1, c2 CatalogItemSpec) bool {
	return util.DeepEqualWithUnionHandling(reflect.ValueOf(c1), reflect.ValueOf(c2))
}

// CertificateSigningRequestSpecsAreEqual compares two CertificateSigningRequestSpec
// objects for semantic equality, normalizing JSON round-trip artifacts such as nil
// vs. empty maps (e.g. Extra).
func CertificateSigningRequestSpecsAreEqual(c1, c2 CertificateSigningRequestSpec) bool {
	return util.DeepEqualWithUnionHandling(reflect.ValueOf(c1), reflect.ValueOf(c2))
}
