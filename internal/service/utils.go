package service

import (
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/service/common"
	"github.com/flightctl/flightctl/internal/store"
)

const MaxConcurrentAgents = 15

// MaxRecordsPerListRequest forwards internal/service/common.MaxRecordsPerListRequest so the
// untouched per-resource files in this package that reference it unqualified keep compiling.
const MaxRecordsPerListRequest = common.MaxRecordsPerListRequest

// prepareListParams is a thin forwarding wrapper over internal/service/common.PrepareListParams,
// kept here (unexported, as before) so the untouched per-resource files in this package that
// call it unqualified keep compiling unchanged.
func prepareListParams(cont *string, lSelector *string, fSelector *string, limit *int32) (*store.ListParams, domain.Status) {
	return common.PrepareListParams(cont, lSelector, fSelector, limit)
}
