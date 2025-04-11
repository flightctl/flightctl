package model

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/samber/lo"
)

type CertificateSigningRequest struct {
	Resource

	// The desired state of the certificate signing request, stored as opaque JSON object.
	Spec *JSONField[api.CertificateSigningRequestSpec] `gorm:"type:jsonb"`

	// The last reported state of the certificate signing request, stored as opaque JSON object.
	Status *JSONField[api.CertificateSigningRequestStatus] `gorm:"type:jsonb"`
}

func (csr CertificateSigningRequest) String() string {
	val, _ := json.Marshal(csr)
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
			Labels:          lo.FromPtrOr(resource.Metadata.Labels, make(map[string]string)),
			Annotations:     lo.FromPtrOr(resource.Metadata.Annotations, make(map[string]string)),
			ResourceVersion: resourceVersion,
		},
		Spec:   MakeJSONField(resource.Spec),
		Status: MakeJSONField(status),
	}, nil
}

func CertificateSigningRequestAPIVersion() string {
	return fmt.Sprintf("%s/%s", api.APIGroup, api.CertificateSigningRequestAPIVersion)
}

func (csr *CertificateSigningRequest) ToApiResource(opts ...APIResourceOption) (*api.CertificateSigningRequest, error) {
	if csr == nil {
		return &api.CertificateSigningRequest{}, nil
	}

	spec := api.CertificateSigningRequestSpec{}
	if csr.Spec != nil {
		spec = csr.Spec.Data
	}

	status := api.CertificateSigningRequestStatus{Conditions: []api.Condition{}}
	if csr.Status != nil {
		status = csr.Status.Data
	}

	return &api.CertificateSigningRequest{
		ApiVersion: CertificateSigningRequestAPIVersion(),
		Kind:       api.CertificateSigningRequestKind,
		Metadata: api.ObjectMeta{
			Name:              lo.ToPtr(csr.Name),
			CreationTimestamp: lo.ToPtr(csr.CreatedAt.UTC()),
			Labels:            lo.ToPtr(util.EnsureMap(csr.Resource.Labels)),
			Annotations:       lo.ToPtr(util.EnsureMap(csr.Resource.Annotations)),
			ResourceVersion:   lo.Ternary(csr.ResourceVersion != nil, lo.ToPtr(strconv.FormatInt(lo.FromPtr(csr.ResourceVersion), 10)), nil),
		},
		Spec:   spec,
		Status: &status,
	}, nil
}

func CertificateSigningRequestsToApiResource(csrs []CertificateSigningRequest, cont *string, numRemaining *int64) (api.CertificateSigningRequestList, error) {
	certificateSigningRequestList := make([]api.CertificateSigningRequest, len(csrs))
	for i, certificateSigningRequest := range csrs {
		apiResource, _ := certificateSigningRequest.ToApiResource()
		certificateSigningRequestList[i] = *apiResource
	}
	ret := api.CertificateSigningRequestList{
		ApiVersion: CertificateSigningRequestAPIVersion(),
		Kind:       api.CertificateSigningRequestListKind,
		Items:      certificateSigningRequestList,
		Metadata:   api.ListMeta{},
	}
	if cont != nil {
		ret.Metadata.Continue = cont
		ret.Metadata.RemainingItemCount = numRemaining
	}
	return ret, nil
}

func CertificateSigningRequestPtrToCertificateSigningRequest(p *CertificateSigningRequest) *CertificateSigningRequest {
	return p
}

func (csr *CertificateSigningRequest) GetKind() string {
	return api.CertificateSigningRequestKind
}

func (csr *CertificateSigningRequest) HasNilSpec() bool {
	return csr.Spec == nil
}

func (csr *CertificateSigningRequest) GetSpec() any {
	return csr.Spec.Data
}

func (csr *CertificateSigningRequest) HasSameSpecAs(otherResource any) bool {
	otherDev, ok := otherResource.(*CertificateSigningRequest) // Assert that the other resource is a *CertificateSigningRequest
	if !ok {
		return false // Not the same type, so specs cannot be the same
	}
	if otherDev == nil || otherDev.Spec == nil {
		return false
	}
	return reflect.DeepEqual(csr.Spec.Data, otherDev.Spec.Data)
}

func (csr *CertificateSigningRequest) GetStatusAsJson() ([]byte, error) {
	return csr.Status.MarshalJSON()
}
