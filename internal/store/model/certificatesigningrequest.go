package model

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/samber/lo"
)

type CertificateSigningRequest struct {
	Resource

	// The desired state of the certificate signing request, stored as opaque JSON object.
	Spec *JSONField[domain.CertificateSigningRequestSpec] `gorm:"type:jsonb"`

	// The last reported state of the certificate signing request, stored as opaque JSON object.
	Status *JSONField[domain.CertificateSigningRequestStatus] `gorm:"type:jsonb"`
}

func (csr CertificateSigningRequest) String() string {
	val, _ := json.Marshal(csr)
	return string(val)
}

func NewCertificateSigningRequestFromApiResource(resource *domain.CertificateSigningRequest) (*CertificateSigningRequest, error) {
	if resource == nil || resource.Metadata.Name == nil {
		return &CertificateSigningRequest{}, nil
	}

	status := domain.CertificateSigningRequestStatus{Conditions: []domain.Condition{}}
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
			Owner:           resource.Metadata.Owner,
			ResourceVersion: resourceVersion,
		},
		Spec:   MakeJSONField(resource.Spec),
		Status: MakeJSONField(status),
	}, nil
}

func CertificateSigningRequestAPIVersion() string {
	return fmt.Sprintf("%s/%s", domain.APIGroup, domain.CertificateSigningRequestAPIVersion)
}

func (csr *CertificateSigningRequest) ToApiResource(opts ...APIResourceOption) (*domain.CertificateSigningRequest, error) {
	if csr == nil {
		return &domain.CertificateSigningRequest{}, nil
	}

	spec := domain.CertificateSigningRequestSpec{}
	if csr.Spec != nil {
		spec = csr.Spec.Data
	}

	status := domain.CertificateSigningRequestStatus{Conditions: []domain.Condition{}}
	if csr.Status != nil {
		status = csr.Status.Data
	}

	return &domain.CertificateSigningRequest{
		ApiVersion: CertificateSigningRequestAPIVersion(),
		Kind:       domain.CertificateSigningRequestKind,
		Metadata: domain.ObjectMeta{
			Name:              lo.ToPtr(csr.Name),
			CreationTimestamp: lo.ToPtr(csr.CreatedAt.UTC()),
			Labels:            lo.ToPtr(util.EnsureMap(csr.Resource.Labels)),
			Annotations:       lo.ToPtr(util.EnsureMap(csr.Resource.Annotations)),
			Owner:             csr.Owner,
			ResourceVersion:   lo.Ternary(csr.ResourceVersion != nil, lo.ToPtr(strconv.FormatInt(lo.FromPtr(csr.ResourceVersion), 10)), nil),
		},
		Spec:   spec,
		Status: &status,
	}, nil
}

func CertificateSigningRequestsToApiResource(csrs []CertificateSigningRequest, cont *string, numRemaining *int64) (domain.CertificateSigningRequestList, error) {
	certificateSigningRequestList := make([]domain.CertificateSigningRequest, len(csrs))
	for i, certificateSigningRequest := range csrs {
		apiResource, _ := certificateSigningRequest.ToApiResource()
		certificateSigningRequestList[i] = *apiResource
	}
	ret := domain.CertificateSigningRequestList{
		ApiVersion: CertificateSigningRequestAPIVersion(),
		Kind:       domain.CertificateSigningRequestListKind,
		Items:      certificateSigningRequestList,
		Metadata:   domain.ListMeta{},
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
	return domain.CertificateSigningRequestKind
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
