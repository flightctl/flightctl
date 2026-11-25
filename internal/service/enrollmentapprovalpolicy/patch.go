package enrollmentapprovalpolicy

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/service/common"
	"github.com/flightctl/flightctl/pkg/jsonpatch"
	"github.com/getkin/kin-openapi/openapi3"
)

func applyPolicyPatch(ctx context.Context, stored, candidate *domain.EnrollmentApprovalPolicy, patch domain.PatchRequest, name string) error {
	patched, err := jsonpatch.ApplyToJSON(stored, patch)
	if err != nil {
		return err
	}
	var document any
	if err := json.Unmarshal(patched, &document); err != nil {
		return err
	}
	schema, err := domain.GetSwagger()
	if err != nil {
		return err
	}
	// Typed decoding coerces null scalars to their zero values. Check nulls first,
	// retaining the shared decoder's type errors and case-insensitive field names.
	if err := validatePolicyNulls(document, schema.Components.Schemas["EnrollmentApprovalPolicy"].Value); err != nil {
		return err
	}
	return common.DecodePatchedJSON(ctx, patched, candidate, "/enrollmentapprovalpolicies/"+name)
}

func validatePolicyNulls(value any, schema *openapi3.Schema) error {
	if schema == nil {
		return nil
	}
	if value == nil {
		return schema.VisitJSON(nil)
	}
	switch value := value.(type) {
	case map[string]any:
		for key, child := range value {
			ref := schema.AdditionalProperties.Schema
			for property, candidate := range schema.Properties {
				if strings.EqualFold(key, property) {
					ref = candidate
					break
				}
			}
			if ref != nil {
				if err := validatePolicyNulls(child, ref.Value); err != nil {
					return fmt.Errorf("%s: %w", key, err)
				}
			}
		}
	case []any:
		if schema.Items != nil {
			for i, child := range value {
				if err := validatePolicyNulls(child, schema.Items.Value); err != nil {
					return fmt.Errorf("item %d: %w", i, err)
				}
			}
		}
	}
	return nil
}
