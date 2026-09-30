package bearertokenauthextension

import (
	"fmt"
	"strings"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
)

// Config holds the provider-specific configuration for the bearer token
// authentication extension.
type Config struct {
	Token     api.SecureString `json:"token,omitempty"`
	TokenFile string           `json:"tokenFile,omitempty"`
}

func (c *Config) Validate() error {
	hasToken := strings.TrimSpace(c.Token.Value()) != ""
	hasFile := strings.TrimSpace(c.TokenFile) != ""
	if hasToken && hasFile {
		return fmt.Errorf("\"token\" and \"tokenFile\" are mutually exclusive")
	}
	if !hasToken && !hasFile {
		return fmt.Errorf("one of \"token\" or \"tokenFile\" must be set")
	}
	return nil
}
