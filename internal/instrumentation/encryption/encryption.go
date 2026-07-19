package encryption

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/stoewer/go-strcase"
)

// Operation names
const (
	OpEncrypt = "encrypt"
	OpDecrypt = "decrypt"
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
	ErrCanaryMismatch   = errors.New("canary mismatch")
)

// MetricsRecorder is an interface for recording encryption metrics.
// This allows the encryption package to record metrics without depending on the metrics package.
type MetricsRecorder interface {
	RecordOperation(operation, strategy, keyID, status string, duration time.Duration)
	RecordError(operation, strategy, keyID, errorType string)
	RecordCanaryValidation(strategy, keyID, status string)
}

// Manager orchestrates multiple encryption strategies and routes operations
// based on version prefixes in encrypted data.
//
// Thread safety: Manager is safe for concurrent use. All public methods are protected
type Manager struct {
	mu             sync.RWMutex
	strategies     map[string]Strategy
	activeStrategy string // Which strategy to use for new encryptions
	metrics        MetricsRecorder
}

// ParsedEncrypted contains the parsed components of a strategy-specific
// encrypted body.
//
// The Manager parses only the outer Flightctl envelope:
//
//	enc:<version>:<body>
//
// The Strategy parses <body>. For v1, the body format is:
//
//	<keyID>:<base64(nonce||ciphertext||tag)>
//
// Future strategies may use a different body format while keeping the same
// Manager-level envelope.
type ParsedEncrypted struct {
	// KeyID identifies the key that encrypted this value.
	KeyID string

	// Payload contains the strategy-specific encrypted payload.
	// For v1, this is nonce||ciphertext||tag after base64 decoding.
	Payload []byte

	// Metadata contains optional non-sensitive strategy-specific metadata.
	// It must not contain plaintext, ciphertext, key material, nonces,
	// authentication tags, or customer/resource identifiers.
	Metadata map[string]string
}

// Strategy defines one encryption format version.
//
// A Strategy does not decide whether an input value is plaintext or encrypted.
// The Manager owns encrypted-state detection using the outer
// "enc:<version>:<body>" envelope and calls the Strategy only with plaintext
// or with a body already routed to this strategy version.
type Strategy interface {
	// Version returns the immutable version identifier (e.g., "v1", "v2").
	// This should be a simple version string, not include key IDs or key source types.
	Version() string

	// String returns human-readable status information about this strategy.
	// Should include: algorithm, active key ID, configured keys, etc.
	// NEVER include actual key material.
	// Example: "AES-256-GCM, active_key=default, keys=[default, key2]"
	// Used by encryption status endpoints and diagnostics.
	String() string

	// ActiveKeyID returns the identifier of the currently active encryption key.
	// This is the key used for all new encryptions.
	ActiveKeyID() string

	// Algorithm returns the algorithm name (e.g., "AES-256-GCM").
	Algorithm() string

	// ConfiguredKeys returns all configured key IDs for this strategy.
	ConfiguredKeys() []string

	// EncryptPlaintext encrypts plaintext using the strategy's active key and
	// returns the strategy-specific body to be stored after "enc:<version>:".
	//
	// For v1, the returned body is:
	//
	//	<keyID>:<base64(nonce||ciphertext||tag)>
	//
	// The returned body must not include the outer "enc:<version>:" prefix.
	// The strategy must not try to detect already-encrypted input.
	EncryptPlaintext(ctx context.Context, plaintext []byte) ([]byte, error)

	// ParseBody parses the strategy-specific body from the outer envelope.
	//
	// For v1:
	//
	//	<keyID>:<base64data>
	//
	// becomes:
	//
	//	ParsedEncrypted{KeyID: keyID, Payload: decodedBase64}
	//
	// ParseBody must perform strict format validation. It must not treat
	// malformed strategy bodies as plaintext.
	ParseBody(body []byte) (*ParsedEncrypted, error)

	// DecryptParsed decrypts a value previously parsed by ParseBody.
	//
	// The strategy uses parsed.KeyID to select the configured key. It must fail
	// if the key is unavailable, the payload is malformed, or authentication
	// fails.
	DecryptParsed(ctx context.Context, parsed *ParsedEncrypted) ([]byte, error)
}

// IsEncrypted checks if data has the encryption version prefix "enc:<version>:..."
func IsEncrypted(data []byte) bool {
	_, _, ok := parseEncryptedFormat(data)
	return ok
}

// CategorizeError returns a safe error category without sensitive data.
// Returns standardized error types for metrics and tracing.
func CategorizeError(err error) string {
	if err == nil {
		return ""
	}

	// Use errors.Is for proper error type checking
	switch {
	case errors.Is(err, ErrInvalidFormat):
		return "invalid_format"
	case errors.Is(err, ErrStrategyNotFound):
		return "unsupported_strategy"
	case errors.Is(err, ErrKeyNotFound):
		return "missing_key"
	case errors.Is(err, ErrDecryptionFailed):
		return "decrypt_failed"
	case errors.Is(err, ErrEncryptionFailed):
		return "encrypt_failed"
	case errors.Is(err, ErrCanaryMismatch):
		return "canary_mismatch"
	case errors.Is(err, ErrNoActiveStrategy):
		return "unsupported_strategy"
	case errors.Is(err, ErrParseFailed):
		return "invalid_format"
	case errors.Is(err, ErrInvalidKey):
		return "missing_key"
	default:
		return "operation_failed"
	}
}

// parseEncryptedFormat parses "enc:<version>:<payload>" format.
// Returns (version, payload, true) if valid, ("", nil, false) otherwise.
// The version is normalized to kebab-case for consistent lookup.
func parseEncryptedFormat(data []byte) (version string, payload []byte, ok bool) {
	str := string(data)
	if !strings.HasPrefix(str, "enc:") {
		return "", nil, false
	}

	parts := strings.SplitN(str, ":", 3)
	if len(parts) < 3 {
		return "", nil, false
	}

	// Normalize version to kebab-case for consistent strategy lookup
	version = strcase.KebabCase(parts[1])
	payload = []byte(parts[2])
	return version, payload, true
}
