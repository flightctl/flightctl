package display

import (
	"encoding/json"
	"fmt"
)

// NameFormatter handles name-only output formatting
type NameFormatter struct{}

type metadata struct {
	Name string `json:"name"`
}

type resource struct {
	Metadata metadata `json:"metadata"`
}

// Format outputs only the names of resources
func (f *NameFormatter) Format(data interface{}, options FormatOptions) error {
	jsonBytes, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("marshalling JSON200: %w", err)
	}

	// For single resources
	if len(options.Name) > 0 {
		var singleResource resource
		if err := json.Unmarshal(jsonBytes, &singleResource); err != nil {
			return fmt.Errorf("unmarshalling resource: %w", err)
		}
		fmt.Fprintln(options.Writer, singleResource.Metadata.Name)
		return nil
	}

	// For list resources
	var listResponse struct {
		Items []resource `json:"items"`
	}
	if err := json.Unmarshal(jsonBytes, &listResponse); err != nil {
		return fmt.Errorf("unmarshalling list response: %w", err)
	}

	// Print names from all items
	for _, item := range listResponse.Items {
		fmt.Fprintln(options.Writer, item.Metadata.Name)
	}

	return nil
}
