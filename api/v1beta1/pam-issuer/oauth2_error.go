package pam_issuer

import "fmt"

// Error implements the error interface for OAuth2Error
// This allows OAuth2Error to be returned as a standard Go error
func (o *OAuth2Error) Error() string {
	errorCode := string(o.Code)
	if o.ErrorDescription != nil && *o.ErrorDescription != "" {
		return fmt.Sprintf("%s: %s", errorCode, *o.ErrorDescription)
	}
	return errorCode
}

// IsOAuth2Error checks if an error is an OAuth2Error
func IsOAuth2Error(err error) (*OAuth2Error, bool) {
	if oauth2Err, ok := err.(*OAuth2Error); ok {
		return oauth2Err, true
	}
	return nil, false
}
