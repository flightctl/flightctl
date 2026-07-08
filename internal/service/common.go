package service

import (
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/service/common"
)

// The functions below are thin forwarding wrappers over internal/service/common, kept here
// with identical signatures so external callers in other packages keep compiling unchanged.
// See internal/service/common/http.go for the real implementations.

func NilOutManagedObjectMetaProperties(om *domain.ObjectMeta) {
	common.NilOutManagedObjectMetaProperties(om)
}

func StoreErrorToApiStatus(err error, created bool, kind string, name *string) domain.Status {
	return common.StoreErrorToApiStatus(err, created, kind, name)
}

func ApiStatusToErr(status domain.Status) error {
	return common.ApiStatusToErr(status)
}
