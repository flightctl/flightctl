package common

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/pkg/jsonpatch"
	"github.com/getkin/kin-openapi/openapi3"
)

// JSON Patch paths of the desired firmware of a device and of the device
// template of a fleet.
const (
	DeviceFirmwarePatchPath = "/spec/firmware"
	FleetFirmwarePatchPath  = "/spec/template/spec/firmware"
)

const (
	firmwareSpecSchemaName   = "FirmwareSpec"
	firmwareStatusSchemaName = "FirmwareStatus"
)

// PreserveOmittedFirmware returns the device spec to store when a write
// replaces spec: a spec that omits firmware keeps the stored firmware, because
// only an explicit JSON Patch removal withdraws firmware management. spec is
// never modified; a shallow copy carries the stored firmware instead.
func PreserveOmittedFirmware(spec, stored *domain.DeviceSpec) *domain.DeviceSpec {
	if stored == nil || stored.Firmware == nil || (spec != nil && spec.Firmware != nil) {
		return spec
	}
	var effective domain.DeviceSpec
	if spec != nil {
		effective = *spec
	}
	effective.Firmware = stored.Firmware
	return &effective
}

// FirmwareRemovedByPatch reports whether a JSON Patch request explicitly
// removes the desired firmware at path, the only request that withdraws
// firmware management. Setting the firmware, or an object that encloses it, to
// null or to a value without firmware is not a removal.
func FirmwareRemovedByPatch(patch domain.PatchRequest, path string) bool {
	for _, operation := range patch {
		if operation.Op == "remove" && operation.Path == path {
			return true
		}
	}
	return false
}

// ValidatePatchedFirmware checks the firmware that a JSON Patch request leaves
// at path against the API schema while it is still raw JSON. Decoding into the
// typed spec matches field names regardless of case and keeps one value per
// field, so a key that differs from a firmware field only in case would be
// dropped instead of rejected. The updater configuration is an open object in
// the API schema; the closed schema of the selected release validates it.
// Requests that write neither at, inside nor above path are not applied again.
func ValidatePatchedFirmware[T any](current T, patch domain.PatchRequest, path string) error {
	if !patchWritesPath(patch, path) {
		return nil
	}
	patched, err := jsonpatch.ApplyToJSON(current, patch)
	if err != nil {
		return err
	}
	return ValidatePatchedFirmwareJSON(patched, path)
}

// ValidatePatchedFirmwareJSON checks raw firmware without applying a patch again.
func ValidatePatchedFirmwareJSON(patched []byte, path string) error {
	var document any
	if err := json.Unmarshal(patched, &document); err != nil {
		return fmt.Errorf("patch produced an invalid resource: %w", err)
	}
	firmware, found := jsonPointerValue(document, path)
	if !found || firmware == nil {
		return nil
	}
	return validateRawFirmware(firmware, path, firmwareSpecSchemaName)
}

// validateRawFirmware checks a decoded raw JSON value at path against the
// named API schema.
func validateRawFirmware(value any, path, schemaName string) error {
	swagger, err := domain.GetSwagger()
	if err != nil {
		return fmt.Errorf("failed to load the API schema: %w", err)
	}
	schema := swagger.Components.Schemas[schemaName]
	if schema == nil || schema.Value == nil {
		return fmt.Errorf("the API schema has no %s definition", schemaName)
	}
	if err := schema.Value.VisitJSON(value, openapi3.MultiErrors(), openapi3.VisitAsRequest()); err != nil {
		return fmt.Errorf("patch produced an invalid resource: %s", firmwareSchemaErrors(path, err))
	}
	return nil
}

// patchWritesPath reports whether an operation of patch writes at, inside or
// above the JSON Pointer path.
func patchWritesPath(patch domain.PatchRequest, path string) bool {
	for _, operation := range patch {
		target := operation.Path
		if target == "" || target == path || strings.HasPrefix(target, path+"/") || strings.HasPrefix(path, target+"/") {
			return true
		}
	}
	return false
}

// jsonPointerValue returns the value of a decoded JSON document at a JSON
// Pointer made of object member names.
func jsonPointerValue(document any, pointer string) (any, bool) {
	current := document
	for _, token := range strings.Split(strings.TrimPrefix(pointer, "/"), "/") {
		object, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		token = strings.ReplaceAll(strings.ReplaceAll(token, "~1", "/"), "~0", "~")
		if current, ok = object[token]; !ok {
			return nil, false
		}
	}
	return current, true
}

// firmwareSchemaErrors formats API schema violations under the firmware at
// path as field paths and reasons, without echoing the schema or the value.
func firmwareSchemaErrors(path string, err error) string {
	base := strings.ReplaceAll(strings.TrimPrefix(path, "/"), "/", ".")
	var messages []string
	var collect func(error)
	collect = func(err error) {
		var multi openapi3.MultiError
		var schemaErr *openapi3.SchemaError
		switch {
		case errors.As(err, &multi):
			for _, inner := range multi {
				collect(inner)
			}
		case errors.As(err, &schemaErr):
			messages = append(messages, fmt.Sprintf("%s: %s", schemaFieldPath(base, schemaErr.JSONPointer()), schemaErr.Reason))
		default:
			messages = append(messages, fmt.Sprintf("%s: %v", base, err))
		}
	}
	collect(err)
	return strings.Join(messages, "; ")
}

func schemaFieldPath(base string, pointer []string) string {
	var path strings.Builder
	path.WriteString(base)
	for _, token := range pointer {
		if token != "" && strings.Trim(token, "0123456789") == "" {
			fmt.Fprintf(&path, "[%s]", token)
			continue
		}
		path.WriteString(".")
		path.WriteString(token)
	}
	return path.String()
}
