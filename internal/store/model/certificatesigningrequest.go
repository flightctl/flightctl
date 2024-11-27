package model

import (
	"encoding/json"
	"strconv"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/samber/lo"
)

type CertificateSigningRequest struct {
	Resource

	// The desired state of the enrollment request, stored as opaque JSON object.
	Spec *JSONField[api.CertificateSigningRequestSpec] `gorm:"type:jsonb"`

	// The last reported state of the enrollment request, stored as opaque JSON object.
	Status *JSONField[api.CertificateSigningRequestStatus] `gorm:"type:jsonb"`
}

type CertificateSigningRequestList []CertificateSigningRequest

func (e CertificateSigningRequest) String() string {
	val, _ := json.Marshal(e)
	return string(val)
}

func NewCertificateSigningRequestFromApiResource(resource *api.CertificateSigningRequest) (*CertificateSigningRequest, error) {
	if resource == nil || resource.Metadata.Name == nil {
		return &CertificateSigningRequest{}, nil
	}

	status := api.CertificateSigningRequestStatus{Conditions: []api.Condition{}}
	if resource.Status != nil {
		status = *resource.Status
	}
	var resourceVersion *int64
	if resource.Metadata.ResourceVersion != nil {
		i, err := strconv.ParseInt(lo.FromPtr(resource.Metadata.ResourceVersion), 10, 64)
		if err != nil {
			return nil, flterrors.ErrIllegalResourceVersionFormat
		}
		resourceVersion = &i
	}
	return &CertificateSigningRequest{
		Resource: Resource{
			Name:            *resource.Metadata.Name,
			Labels:          util.LabelMapToArray(resource.Metadata.Labels),
			ResourceVersion: resourceVersion,
		},
		Spec:   MakeJSONField(resource.Spec),
		Status: MakeJSONField(status),
	}, nil
}

func (csr *CertificateSigningRequest) ToApiResource() api.CertificateSigningRequest {
	if csr == nil {
		return api.CertificateSigningRequest{}
	}

	status := api.CertificateSigningRequestStatus{Conditions: []api.Condition{}}
	if csr.Status != nil {
		status = csr.Status.Data
	}

	metadataLabels := util.LabelArrayToMap(csr.Resource.Labels)

	return api.CertificateSigningRequest{
		ApiVersion: api.CertificateSigningRequestAPI,
		Kind:       api.CertificateSigningRequestKind,
		Metadata: api.ObjectMeta{
			Name:              util.StrToPtr(csr.Name),
			CreationTimestamp: util.TimeToPtr(csr.CreatedAt.UTC()),
			Labels:            &metadataLabels,
			ResourceVersion:   lo.Ternary(csr.ResourceVersion != nil, lo.ToPtr(strconv.FormatInt(lo.FromPtr(csr.ResourceVersion), 10)), nil),
		},
		Spec:   csr.Spec.Data,
		Status: &status,
	}
}

func (csrl CertificateSigningRequestList) ToApiResource(cont *string, numRemaining *int64) api.CertificateSigningRequestList {
	if csrl == nil {
		return api.CertificateSigningRequestList{
			ApiVersion: api.CertificateSigningRequestAPI,
			Kind:       api.CertificateSigningRequestListKind,
			Items:      []api.CertificateSigningRequest{},
		}
	}

	certificateSigningRequestList := make([]api.CertificateSigningRequest, len(csrl))
	for i, certificateSigningRequest := range csrl {
		certificateSigningRequestList[i] = certificateSigningRequest.ToApiResource()
	}
	ret := api.CertificateSigningRequestList{
		ApiVersion: api.CertificateSigningRequestAPI,
		Kind:       api.CertificateSigningRequestListKind,
		Items:      certificateSigningRequestList,
		Metadata:   api.ListMeta{},
	}
	if cont != nil {
		ret.Metadata.Continue = cont
		ret.Metadata.RemainingItemCount = numRemaining
	}
	return ret
}
