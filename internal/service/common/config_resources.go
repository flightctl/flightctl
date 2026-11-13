package common

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/flightctl/flightctl/internal/domain"
)

// ValidateAuthoredConfigPatch rejects rendered-only fields before typed patch decoding.
func ValidateAuthoredConfigPatch(patch domain.PatchRequest, fleetTemplate bool) []error {
	root := []string{"spec"}
	if fleetTemplate {
		root = []string{"spec", "template", "spec"}
	}
	var errs []error
	for i, operation := range patch {
		var path []string
		if operation.Path != "" {
			path = strings.Split(strings.TrimPrefix(operation.Path, "/"), "/")
			for j, part := range path {
				path[j] = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
			}
		}
		data, err := json.Marshal(operation.Value)
		if err != nil {
			errs = append(errs, fmt.Errorf("patch[%d] has an invalid value", i))
			continue
		}
		var value any
		if err := json.Unmarshal(data, &value); err != nil {
			errs = append(errs, fmt.Errorf("patch[%d] has an invalid value", i))
			continue
		}
		remaining := root
		for len(remaining) > 0 && len(path) > 0 && remaining[0] == path[0] {
			remaining, path = remaining[1:], path[1:]
		}
		// Ancestor replacements carry the remaining spec path in their value.
		for len(remaining) > 0 && len(path) == 0 {
			fields, _ := value.(map[string]any)
			value = fields[remaining[0]]
			remaining = remaining[1:]
		}
		if len(remaining) == 0 && authoredConfigField(path, value) {
			errs = append(errs, fmt.Errorf("patch[%d] cannot contain configResources", i))
		}
	}
	return errs
}

func authoredConfigField(path []string, value any) bool {
	if len(path) > 0 {
		return path[0] == "configResources"
	}
	fields, _ := value.(map[string]any)
	_, present := fields["configResources"]
	return present
}
