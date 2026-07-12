package encryption

import (
	"context"
	"errors"

	"github.com/flightctl/flightctl/internal/instrumentation/tracing"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

const (
	tracerName = "flightctl/encryption"

	// Span names
	spanProcess        = "process"
	spanEncrypt        = "encrypt"
	spanDecrypt        = "decrypt"
	spanCanaryValidate = "canary-validate"

	// Attribute keys
	attrOperation = "encryption.operation"
	attrStrategy  = "encryption.strategy"
	attrKeyID     = "encryption.key_id"
	attrResult    = "encryption.result"
	attrAction    = "encryption.action"

	// Result values
	resultSuccess = "success"
	resultError   = "error"

	// Action values (for process operations)
	actionEncryptPlaintext = "encrypt_plaintext"
	actionReencrypt        = "reencrypt"
	actionUnchanged        = "unchanged"
)

// Sentinel errors for categorization
var (
	ErrNoActiveStrategy = errors.New("no active encryption strategy set")
	ErrStrategyNotFound = errors.New("strategy not found")
	ErrKeyNotFound      = errors.New("key not found")
	ErrEncryptionFailed = errors.New("encryption failed")
	ErrDecryptionFailed = errors.New("decryption failed")
	ErrParseFailed      = errors.New("parse failed")
	ErrInvalidFormat    = errors.New("invalid format")
	ErrInvalidKey       = errors.New("invalid key")
)

// startProcessSpan starts a span for ProcessEncryption operation.
// Returns context with span and the span itself. Caller must call span.End().
func startProcessSpan(ctx context.Context) (context.Context, trace.Span) {
	ctx, span := tracing.StartSpan(ctx, tracerName, spanProcess)
	span.SetAttributes(attribute.String(attrOperation, "process"))
	return ctx, span
}

// startEncryptSpan starts a span for Encrypt operation.
// Returns context with span and the span itself. Caller must call span.End().
func startEncryptSpan(ctx context.Context, strategy, keyID string) (context.Context, trace.Span) {
	ctx, span := tracing.StartSpan(ctx, tracerName, spanEncrypt)
	span.SetAttributes(
		attribute.String(attrOperation, "encrypt"),
		attribute.String(attrStrategy, strategy),
		attribute.String(attrKeyID, keyID),
	)
	return ctx, span
}

// startDecryptSpan starts a span for Decrypt operation.
// Returns context with span and the span itself. Caller must call span.End().
func startDecryptSpan(ctx context.Context, strategy, keyID string) (context.Context, trace.Span) {
	ctx, span := tracing.StartSpan(ctx, tracerName, spanDecrypt)
	span.SetAttributes(
		attribute.String(attrOperation, "decrypt"),
		attribute.String(attrStrategy, strategy),
		attribute.String(attrKeyID, keyID),
	)
	return ctx, span
}

// startCanaryValidateSpan starts a span for canary validation.
// Returns context with span and the span itself. Caller must call span.End().
func startCanaryValidateSpan(ctx context.Context, strategy, keyID string) (context.Context, trace.Span) {
	ctx, span := tracing.StartSpan(ctx, tracerName, spanCanaryValidate)
	span.SetAttributes(
		attribute.String(attrOperation, "canary_validate"),
		attribute.String(attrStrategy, strategy),
		attribute.String(attrKeyID, keyID),
	)
	return ctx, span
}

// recordSuccess marks the span as successful.
func recordSuccess(span trace.Span) {
	span.SetAttributes(attribute.String(attrResult, resultSuccess))
	span.SetStatus(codes.Ok, "")
}

// recordError marks the span as failed with a sanitized error category.
// Does NOT include plaintext, ciphertext, keys, nonces, tags, or resource IDs.
func recordError(span trace.Span, err error) {
	span.SetAttributes(attribute.String(attrResult, resultError))
	span.SetStatus(codes.Error, categorizeError(err))
}

// recordProcessAction records the action taken by ProcessEncryption.
func recordProcessAction(span trace.Span, action string) {
	span.SetAttributes(attribute.String(attrAction, action))
}

// categorizeError returns a safe error category without sensitive data.
func categorizeError(err error) string {
	if err == nil {
		return ""
	}

	// Use errors.Is for proper error type checking
	switch {
	case errors.Is(err, ErrNoActiveStrategy):
		return "no_active_strategy"
	case errors.Is(err, ErrStrategyNotFound):
		return "strategy_not_found"
	case errors.Is(err, ErrKeyNotFound):
		return "key_not_found"
	case errors.Is(err, ErrDecryptionFailed):
		return "decryption_failed"
	case errors.Is(err, ErrEncryptionFailed):
		return "encryption_failed"
	case errors.Is(err, ErrParseFailed):
		return "parse_failed"
	case errors.Is(err, ErrInvalidFormat):
		return "invalid_format"
	case errors.Is(err, ErrInvalidKey):
		return "invalid_key"
	default:
		return "operation_failed"
	}
}
