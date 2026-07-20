package tasks

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"syscall"
)

func isRetryableRenderError(err error) bool {
	if err == nil {
		return false
	}

	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return dnsErr.Temporary() //nolint:staticcheck
	}

	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}

	switch {
	case errors.Is(err, syscall.ECONNRESET),
		errors.Is(err, syscall.ETIMEDOUT):
		return true

	case errors.Is(err, io.EOF),
		errors.Is(err, io.ErrUnexpectedEOF):
		return true

	case errors.Is(err, context.DeadlineExceeded),
		errors.Is(err, context.Canceled):
		return true
	}

	msg := err.Error()
	for _, substr := range []string{
		"connection refused",
		"connection reset",
		"i/o timeout",
		"unexpected EOF",
	} {
		if strings.Contains(msg, substr) {
			return true
		}
	}

	return false
}
