package v1alpha1

import (
	"fmt"
	"net/http"
)

func NewSuccessStatus(code int32, reason string, message string) Status {
	return Status{
		ApiVersion: "v1alpha1",
		Kind:       "Status",
		Status:     "Success",
		Code:       code,
		Reason:     reason,
		Message:    message,
	}
}

func NewFailureStatus(code int32, reason string, message string) Status {
	return Status{
		ApiVersion: "v1alpha1",
		Kind:       "Status",
		Status:     "Failure",
		Code:       code,
		Reason:     reason,
		Message:    message,
	}
}

func StatusOK() Status {
	return NewSuccessStatus(http.StatusOK, http.StatusText(http.StatusOK), "")
}

func StatusBadRequest(message string) Status {
	return NewFailureStatus(http.StatusBadRequest, http.StatusText(http.StatusBadRequest), message)
}

func StatusUnauthorized(message string) Status {
	return NewFailureStatus(http.StatusUnauthorized, http.StatusText(http.StatusUnauthorized), message)
}

func StatusForbidden(message string) Status {
	return NewFailureStatus(http.StatusForbidden, http.StatusText(http.StatusForbidden), message)
}

func StatusResourceNotFound(kind, name string) Status {
	return NewFailureStatus(http.StatusNotFound, http.StatusText(http.StatusNotFound), fmt.Sprintf("%s of name %q not found.", kind, name))
}

func StatusConflict(message string) Status {
	return NewFailureStatus(http.StatusConflict, http.StatusText(http.StatusConflict), message)
}

func StatusResourceVersionConflict(message string) Status {
	return NewFailureStatus(http.StatusConflict, http.StatusText(http.StatusConflict), message)
}

func StatusInternalServerError(message string) Status {
	return NewFailureStatus(http.StatusInternalServerError, http.StatusText(http.StatusInternalServerError), message)
}

func StatusServiceUnavailableError(message string) Status {
	return NewFailureStatus(http.StatusServiceUnavailable, http.StatusText(http.StatusServiceUnavailable), message)
}

func StatusGatewayTimeoutError(message string) Status {
	return NewFailureStatus(http.StatusGatewayTimeout, http.StatusText(http.StatusGatewayTimeout), message)
}
