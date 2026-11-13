package jsonpatch

import (
	"bytes"
	"encoding/json"
	"fmt"

	jsonpatch "github.com/evanphx/json-patch"
	v1beta1 "github.com/flightctl/flightctl/api/core/v1beta1"
)

// Apply applies RFC 6902 JSON Patch operations to obj and unmarshals the result into newObj.
// Unknown fields in the patched JSON are rejected, so operations that silently introduce
// fields not present in the target type (e.g. "replace" acting as "add" on a missing path)
// are caught here rather than silently becoming no-ops.
func Apply[T any](obj T, newObj *T, ops v1beta1.PatchRequest) error {
	newJSON, err := ApplyToJSON(obj, ops)
	if err != nil {
		return err
	}

	decoder := json.NewDecoder(bytes.NewReader(newJSON))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(newObj); err != nil {
		return fmt.Errorf("patch produced an invalid resource: %w", err)
	}
	return nil
}

// ApplyToJSON applies RFC 6902 JSON Patch operations to the JSON encoding of obj and
// returns the patched JSON document, before it is decoded into any type.
func ApplyToJSON[T any](obj T, ops v1beta1.PatchRequest) ([]byte, error) {
	objJSON, err := json.Marshal(obj)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal resource: %w", err)
	}

	opsJSON, err := json.Marshal(ops)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal patch operations: %w", err)
	}

	decoded, err := jsonpatch.DecodePatch(opsJSON)
	if err != nil {
		return nil, fmt.Errorf("invalid JSON patch: %w", err)
	}

	newJSON, err := decoded.Apply(objJSON)
	if err != nil {
		return nil, fmt.Errorf("failed to apply JSON patch: %w", err)
	}
	return newJSON, nil
}
