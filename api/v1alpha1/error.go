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

func StatusCreated() Status {
	return NewSuccessStatus(http.StatusCreated, http.StatusText(http.StatusCreated), "")
}

func StatusNoContent() Status {
	return NewSuccessStatus(http.StatusNoContent, http.StatusText(http.StatusNoContent), "")
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

func StatusNotImplemented(message string) Status {
	return NewFailureStatus(http.StatusNotImplemented, http.StatusText(http.StatusNotImplemented), message)
}

func StatusAuthNotConfigured(message string) Status {
	return NewFailureStatus(http.StatusTeapot, "Auth not configured", message)
}
