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
			errs = append(errs, fmt.Errorf("patch[%d] cannot contain configResources or application/OS pullConfig", i))
		}
	}
	return errs
}

func authoredConfigField(path []string, value any) bool {
	fields, _ := value.(map[string]any)
	if len(path) == 0 {
		if _, present := fields["configResources"]; present {
			return true
		}
		return authoredConfigField([]string{"os"}, fields["os"]) || authoredConfigField([]string{"applications"}, fields["applications"])
	}
	switch path[0] {
	case "configResources":
		return true
	case "os":
		if len(path) > 1 {
			return path[1] == "pullConfig"
		}
		_, present := fields["pullConfig"]
		return present
	case "applications":
		if len(path) > 2 {
			return path[2] == "pullConfig"
		}
		if len(path) == 2 {
			_, present := fields["pullConfig"]
			return present
		}
		apps, _ := value.([]any)
		for _, app := range apps {
			if authoredConfigField([]string{"applications", "0"}, app) {
				return true
			}
		}
	}
	return false
}
