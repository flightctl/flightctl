package display

import (
	"fmt"

	"sigs.k8s.io/yaml"
)

// YAMLFormatter handles YAML output formatting
type YAMLFormatter struct{}

// Format outputs the data in YAML format
func (f *YAMLFormatter) Format(data interface{}, options FormatOptions) error {
	marshalled, err := yaml.Marshal(data)
	if err != nil {
		return fmt.Errorf("marshalling resource: %w", err)
	}
	_, err = fmt.Fprintf(options.Writer, "%s\n", string(marshalled))
	return err
}
