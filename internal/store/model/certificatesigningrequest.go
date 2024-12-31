package model

import (
	"encoding/json"
	"reflect"
	"strconv"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/google/uuid"
	"github.com/samber/lo"
)

type CertificateSigningRequest struct {
	Resource

	// The desired state of the certificate signing request, stored as opaque JSON object.
	Spec *JSONField[api.CertificateSigningRequestSpec] `gorm:"type:jsonb"`

	// The last reported state of the certificate signing request, stored as opaque JSON object.
	Status *JSONField[api.CertificateSigningRequestStatus] `gorm:"type:jsonb"`
}

type CertificateSigningRequestList []CertificateSigningRequest

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

func (csr *CertificateSigningRequest) ToApiResource(opts ...APIResourceOption) (*api.CertificateSigningRequest, error) {
	if csr == nil {
		return &api.CertificateSigningRequest{}, nil
	}

	status := api.CertificateSigningRequestStatus{Conditions: []api.Condition{}}
	if csr.Status != nil {
		status = csr.Status.Data
	}

	return &api.CertificateSigningRequest{
		ApiVersion: api.CertificateSigningRequestAPI,
		Kind:       api.CertificateSigningRequestKind,
		Metadata: api.ObjectMeta{
			Name:              util.StrToPtr(csr.Name),
			CreationTimestamp: util.TimeToPtr(csr.CreatedAt.UTC()),
			Labels:            lo.ToPtr(util.EnsureMap(csr.Resource.Labels)),
			Annotations:       lo.ToPtr(util.EnsureMap(csr.Resource.Annotations)),
			ResourceVersion:   lo.Ternary(csr.ResourceVersion != nil, lo.ToPtr(strconv.FormatInt(lo.FromPtr(csr.ResourceVersion), 10)), nil),
		},
		Spec:   csr.Spec.Data,
		Status: &status,
	}, nil
}

func (csrl *CertificateSigningRequestList) ToApiResource(cont *string, numRemaining *int64) (api.CertificateSigningRequestList, error) {
	if csrl == nil {
		return api.CertificateSigningRequestList{
			ApiVersion: api.CertificateSigningRequestAPI,
			Kind:       api.CertificateSigningRequestListKind,
			Items:      []api.CertificateSigningRequest{},
		}, nil
	}

	certificateSigningRequestList := make([]api.CertificateSigningRequest, len(*csrl))
	for i, certificateSigningRequest := range *csrl {
		apiResource, _ := certificateSigningRequest.ToApiResource()
		certificateSigningRequestList[i] = *apiResource
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
	return ret, nil
}

func CertificateSigningRequestPtrToCertificateSigningRequest(p *CertificateSigningRequest) *CertificateSigningRequest {
	return p
}

func (csr *CertificateSigningRequest) GetKind() string {
	return api.CertificateSigningRequestKind
}

func (csr *CertificateSigningRequest) GetName() string {
	return csr.Name
}

func (csr *CertificateSigningRequest) GetOrgID() uuid.UUID {
	return csr.OrgID
}

func (csr *CertificateSigningRequest) SetOrgID(orgId uuid.UUID) {
	csr.OrgID = orgId
}

func (csr *CertificateSigningRequest) GetResourceVersion() *int64 {
	return csr.ResourceVersion
}

func (csr *CertificateSigningRequest) SetResourceVersion(version *int64) {
	csr.ResourceVersion = version
}

func (csr *CertificateSigningRequest) GetGeneration() *int64 {
	return csr.Generation
}

func (csr *CertificateSigningRequest) SetGeneration(generation *int64) {
	csr.Generation = generation
}

func (csr *CertificateSigningRequest) GetOwner() *string {
	return csr.Owner
}

func (csr *CertificateSigningRequest) SetOwner(owner *string) {
	csr.Owner = owner
}

func (csr *CertificateSigningRequest) GetLabels() JSONMap[string, string] {
	return csr.Labels
}

func (csr *CertificateSigningRequest) SetLabels(labels JSONMap[string, string]) {
	csr.Labels = labels
}

func (csr *CertificateSigningRequest) GetAnnotations() JSONMap[string, string] {
	return csr.Annotations
}

func (csr *CertificateSigningRequest) SetAnnotations(annotations JSONMap[string, string]) {
	csr.Annotations = annotations
}

func (csr *CertificateSigningRequest) HasNilSpec() bool {
	return csr.Spec == nil
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

func (csrl *CertificateSigningRequestList) Length() int {
	return len(*csrl)
}

func (csrl *CertificateSigningRequestList) GetItem(i int) Generic {
	return &((*csrl)[i])
}

func (csrl *CertificateSigningRequestList) RemoveLast() {
	*csrl = (*csrl)[:len(*csrl)-1]
}
