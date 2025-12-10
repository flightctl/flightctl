package validation

import (
	"fmt"
	"strings"

	"github.com/flightctl/flightctl/internal/config/standalone"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

func ValidateStandaloneConfig(config *standalone.Config) []error {
	if config == nil {
		return []error{fmt.Errorf("config cannot be nil")}
	}

	errs := field.ErrorList{}

	errs = append(errs, validateGlobalConfig(&config.Global)...)

	return asErrors(errs)
}

func validateGlobalConfig(global *standalone.GlobalConfig) field.ErrorList {
	errs := field.ErrorList{}
	basePath := field.NewPath("global")

	// baseDomain is required
	if global.BaseDomain == "" {
		errs = append(errs, field.Required(basePath.Child("baseDomain"), "baseDomain must be set"))
	}

	errs = append(errs, validateAuthType(&global.Auth, basePath.Child("auth"))...)

	return errs
}

func validateAuthType(authConfig *standalone.AuthConfig, basePath *field.Path) field.ErrorList {
	errs := field.ErrorList{}

	validAuthTypes := map[string]bool{
		standalone.AuthTypeOIDC:   true,
		standalone.AuthTypeAAP:    true,
		standalone.AuthTypeOAuth2: true,
		standalone.AuthTypeNone:   true,
	}

	if authConfig.Type == "" {
		errs = append(errs, field.Required(basePath.Child("type"), "auth type must be set"))
	} else if !validAuthTypes[authConfig.Type] {

		keys := make([]string, 0, len(validAuthTypes))
		for k := range validAuthTypes {
			keys = append(keys, k)
		}
		errs = append(errs, field.Invalid(
			basePath.Child("type"),
			authConfig.Type,
			"must be one of: "+strings.Join(keys, ", "),
		))
	}

	return errs
}
