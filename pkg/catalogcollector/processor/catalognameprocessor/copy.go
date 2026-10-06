package catalognameprocessor

import (
	apiv1alpha1 "github.com/flightctl/flightctl/api/core/v1alpha1"
	apiv1beta1 "github.com/flightctl/flightctl/api/core/v1beta1"
)

// shallowCopyObjectMeta returns a copy of m. The copy shares all pointer
// fields with the original except Name, which the caller replaces.
func shallowCopyObjectMeta(m apiv1beta1.ObjectMeta) apiv1beta1.ObjectMeta {
	return m // struct copy — all pointer fields are shared
}

// shallowCopyCatalogItemMeta returns a copy of m. The copy shares all pointer
// fields with the original except Catalog, which the caller replaces.
func shallowCopyCatalogItemMeta(m apiv1alpha1.CatalogItemMeta) apiv1alpha1.CatalogItemMeta {
	return m // struct copy — all pointer fields are shared
}
