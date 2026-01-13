package domain

import v1beta1 "github.com/flightctl/flightctl/api/core/v1beta1"

// ========== List Params ==========

type ListAuthProvidersParams = v1beta1.ListAuthProvidersParams
type ListCertificateSigningRequestsParams = v1beta1.ListCertificateSigningRequestsParams
type ListDevicesParams = v1beta1.ListDevicesParams
type ListEnrollmentRequestsParams = v1beta1.ListEnrollmentRequestsParams
type ListEventsParams = v1beta1.ListEventsParams
type ListFleetsParams = v1beta1.ListFleetsParams
type ListLabelsParams = v1beta1.ListLabelsParams
type ListOrganizationsParams = v1beta1.ListOrganizationsParams
type ListRepositoriesParams = v1beta1.ListRepositoriesParams
type ListResourceSyncsParams = v1beta1.ListResourceSyncsParams
type ListTemplateVersionsParams = v1beta1.ListTemplateVersionsParams

// ========== Get Params ==========

type GetEnrollmentConfigParams = v1beta1.GetEnrollmentConfigParams
type GetFleetParams = v1beta1.GetFleetParams
type GetRenderedDeviceParams = v1beta1.GetRenderedDeviceParams

// ========== Order Types ==========

type ListEventsParamsOrder = v1beta1.ListEventsParamsOrder

const (
	Asc  = v1beta1.Asc
	Desc = v1beta1.Desc
)
