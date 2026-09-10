package validation

import (
	"fmt"
	"slices"
	"strings"

	"github.com/flightctl/flightctl/internal/config/standalone"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

// NormalizeStandaloneConfig canonicalizes config values in-place.
// DNS names are case-insensitive per RFC 1123, so baseDomain is lowercased
// to ensure consistent behavior across certificates, service URLs, and TLS verification.
func NormalizeStandaloneConfig(config *standalone.Config) {
	if config == nil {
		return
	}
	config.Global.BaseDomain = strings.ToLower(config.Global.BaseDomain)
}

// ValidateStandaloneConfig validates the standalone service configuration.
// Callers should call NormalizeStandaloneConfig first to canonicalize values.
func ValidateStandaloneConfig(config *standalone.Config) []error {
	if config == nil {
		return []error{fmt.Errorf("config cannot be nil")}
	}

	allErrs := []error{}

	baseDomain := config.Global.BaseDomain
	allErrs = append(allErrs, ValidateHostnameOrFQDN(&baseDomain, "global.baseDomain")...)
	allErrs = append(allErrs, validateAuthType(config.Global.Auth, "global.auth")...)

	return allErrs
}

func validateAuthType(authConfig standalone.AuthConfig, path string) []error {
	validAuthTypes := []string{
		standalone.AuthTypeOIDC,
		standalone.AuthTypeAAP,
		standalone.AuthTypeOAuth2,
		standalone.AuthTypeNone,
	}

	errs := field.ErrorList{}

	if authConfig.Type == "" {
		errs = append(errs, field.Required(fieldPathFor(path+".type"), ""))
	} else if !slices.Contains(validAuthTypes, authConfig.Type) {
		errs = append(errs, field.Invalid(fieldPathFor(path+".type"), authConfig.Type, fmt.Sprintf("must be one of: %s", strings.Join(validAuthTypes, ", "))))
	}

	return asErrors(errs)
}
