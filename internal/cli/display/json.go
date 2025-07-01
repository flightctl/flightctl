package display

import (
	"encoding/json"
	"fmt"
)

// JSONFormatter handles JSON output formatting
type JSONFormatter struct{}

// Format outputs the data in JSON format
func (f *JSONFormatter) Format(data interface{}, options FormatOptions) error {
	marshalled, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("marshalling resource: %w", err)
	}
	_, err = fmt.Fprintf(options.Writer, "%s\n", string(marshalled))
	return err
}
