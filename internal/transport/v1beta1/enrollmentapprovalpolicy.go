package transportv1beta1

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"

	apiv1beta1 "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/transport"
)

var errPolicyStatusProtected = errors.New("status is server-owned")

func decodePolicy(body io.Reader, target any) error {
	var raw json.RawMessage
	decoder := json.NewDecoder(body)
	if err := decoder.Decode(&raw); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("exactly one JSON document is required")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	if _, exists := fields["status"]; exists {
		return errPolicyStatusProtected
	}
	strict := json.NewDecoder(bytes.NewReader(raw))
	strict.DisallowUnknownFields()
	return strict.Decode(target)
}

func (handler *TransportHandler) quotaAllowed(w http.ResponseWriter, request *http.Request) bool {
	allowed, err := handler.authZ.CheckPermission(request.Context(), "enrollmentapprovalpolicies/approvallimit", "update")
	if err != nil {
		if flterrors.IsClientAuthError(err) {
			handler.SetResponse(w, nil, domain.StatusBadRequest("Unable to verify request"))
		} else {
			handler.SetResponse(w, nil, domain.StatusServiceUnavailable("Authorization server unavailable"))
		}
		return false
	}
	if allowed {
		return true
	}
	handler.SetResponse(w, nil, domain.StatusForbidden("approval limit changes require quota permission"))
	return false
}

func (handler *TransportHandler) CreateEnrollmentApprovalPolicy(w http.ResponseWriter, request *http.Request) {
	var policy apiv1beta1.EnrollmentApprovalPolicy
	if err := decodePolicy(request.Body, &policy); err != nil {
		if errors.Is(err, errPolicyStatusProtected) {
			handler.SetResponse(w, nil, domain.NewFailureStatus(400, "ProtectedField", err.Error()))
		} else {
			handler.SetResponse(w, nil, domain.StatusBadRequest(err.Error()))
		}
		return
	}
	if policy.Spec.ApprovalLimit != nil && !handler.quotaAllowed(w, request) {
		return
	}
	result, status := handler.enrollmentapprovalpolicy.CreateEnrollmentApprovalPolicy(request.Context(), transport.OrgIDFromContext(request.Context()), handler.converter.EnrollmentApprovalPolicy().ToDomain(policy))
	handler.SetResponse(w, handler.converter.EnrollmentApprovalPolicy().FromDomain(result), status)
}

func (handler *TransportHandler) ListEnrollmentApprovalPolicies(w http.ResponseWriter, request *http.Request, params apiv1beta1.ListEnrollmentApprovalPoliciesParams) {
	result, status := handler.enrollmentapprovalpolicy.ListEnrollmentApprovalPolicies(request.Context(), transport.OrgIDFromContext(request.Context()), handler.converter.EnrollmentApprovalPolicy().ListParamsToDomain(params))
	handler.SetResponse(w, handler.converter.EnrollmentApprovalPolicy().ListFromDomain(result), status)
}

func (handler *TransportHandler) GetEnrollmentApprovalPolicy(w http.ResponseWriter, request *http.Request, name string) {
	result, status := handler.enrollmentapprovalpolicy.GetEnrollmentApprovalPolicy(request.Context(), transport.OrgIDFromContext(request.Context()), name)
	handler.SetResponse(w, handler.converter.EnrollmentApprovalPolicy().FromDomain(result), status)
}

func (handler *TransportHandler) ReplaceEnrollmentApprovalPolicy(w http.ResponseWriter, request *http.Request, name string) {
	var policy apiv1beta1.EnrollmentApprovalPolicy
	if err := decodePolicy(request.Body, &policy); err != nil {
		if errors.Is(err, errPolicyStatusProtected) {
			handler.SetResponse(w, nil, domain.NewFailureStatus(400, "ProtectedField", err.Error()))
		} else {
			handler.SetResponse(w, nil, domain.StatusBadRequest(err.Error()))
		}
		return
	}
	current, status := handler.enrollmentapprovalpolicy.GetEnrollmentApprovalPolicy(request.Context(), transport.OrgIDFromContext(request.Context()), name)
	if current == nil {
		handler.SetResponse(w, nil, status)
		return
	}
	if policy.Metadata.ResourceVersion == nil || current.Metadata.ResourceVersion == nil || *policy.Metadata.ResourceVersion != *current.Metadata.ResourceVersion {
		handler.SetResponse(w, nil, domain.NewFailureStatus(409, "ResourceVersionConflict", "current resourceVersion is required"))
		return
	}
	if !reflect.DeepEqual(current.Spec.ApprovalLimit, policy.Spec.ApprovalLimit) && !handler.quotaAllowed(w, request) {
		return
	}
	result, status := handler.enrollmentapprovalpolicy.ReplaceEnrollmentApprovalPolicy(request.Context(), transport.OrgIDFromContext(request.Context()), name, handler.converter.EnrollmentApprovalPolicy().ToDomain(policy))
	handler.SetResponse(w, handler.converter.EnrollmentApprovalPolicy().FromDomain(result), status)
}

// decodePolicyPatch preserves explicit null separately from a missing value.
func decodePolicyPatch(body io.Reader) (apiv1beta1.PatchRequest, error) {
	var operations []struct {
		Op    apiv1beta1.PatchRequestOp `json:"op"`
		Path  string                    `json:"path"`
		Value json.RawMessage           `json:"value"`
	}
	decoder := json.NewDecoder(body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&operations); err != nil {
		return nil, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("exactly one JSON document is required")
	}
	patch := make(apiv1beta1.PatchRequest, len(operations))
	for i, operation := range operations {
		patch[i].Op, patch[i].Path = operation.Op, operation.Path
		if len(operation.Value) == 0 {
			if operation.Op != "remove" {
				return nil, fmt.Errorf("patch value is required for %s", operation.Op)
			}
			continue
		}
		if bytes.Equal(bytes.TrimSpace(operation.Value), []byte("null")) {
			patch[i].Value = json.RawMessage("null")
			continue
		}
		valueDecoder := json.NewDecoder(bytes.NewReader(operation.Value))
		valueDecoder.UseNumber()
		if err := valueDecoder.Decode(&patch[i].Value); err != nil {
			return nil, err
		}
	}
	return patch, nil
}

func (handler *TransportHandler) PatchEnrollmentApprovalPolicy(w http.ResponseWriter, request *http.Request, name string) {
	patch, err := decodePolicyPatch(request.Body)
	if err != nil {
		handler.SetResponse(w, nil, domain.StatusBadRequest(err.Error()))
		return
	}
	for _, operation := range patch {
		if operation.Op == "test" {
			continue
		}
		segments := strings.Split(operation.Path, "/")
		if operation.Path == "/spec" || (len(segments) >= 3 && segments[1] == "spec" && strings.EqualFold(segments[2], "approvalLimit")) {
			if !handler.quotaAllowed(w, request) {
				return
			}
		}
	}
	result, status := handler.enrollmentapprovalpolicy.PatchEnrollmentApprovalPolicy(request.Context(), transport.OrgIDFromContext(request.Context()), name, handler.converter.Common().PatchRequestToDomain(patch))
	handler.SetResponse(w, handler.converter.EnrollmentApprovalPolicy().FromDomain(result), status)
}

func (handler *TransportHandler) DeleteEnrollmentApprovalPolicy(w http.ResponseWriter, request *http.Request, name string) {
	status := handler.enrollmentapprovalpolicy.DeleteEnrollmentApprovalPolicy(request.Context(), transport.OrgIDFromContext(request.Context()), name)
	handler.SetResponse(w, nil, status)
}
