package common

import "github.com/flightctl/flightctl/api/v1alpha1"

func NilOutManagedObjectMetaProperties(om *v1alpha1.ObjectMeta) {
	om.Generation = nil
	om.Owner = nil
	om.Annotations = nil
	om.CreationTimestamp = nil
	om.DeletionTimestamp = nil
}
