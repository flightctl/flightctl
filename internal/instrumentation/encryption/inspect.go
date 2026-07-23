package encryption

import (
	"fmt"
)

// InspectEncrypted returns the strategy version and key ID when data uses the
// enc:<version>:<body> envelope. Plaintext returns encrypted=false with empty
// version/keyID and a nil error. Malformed encrypted envelopes return an error
// (fail closed — never treated as plaintext).
func InspectEncrypted(data []byte, mgr *Manager) (version, keyID string, encrypted bool, err error) {
	ver, body, ok := parseEncryptedFormat(data)
	if !ok {
		if len(data) >= 4 && string(data[:4]) == "enc:" {
			return "", "", false, fmt.Errorf("%w: incomplete encryption envelope", ErrInvalidFormat)
		}
		return "", "", false, nil
	}

	if mgr == nil {
		return "", "", false, fmt.Errorf("%w: encryption manager is nil", ErrNoActiveStrategy)
	}

	strategy, exists := mgr.GetStrategy(ver)
	if !exists {
		return "", "", false, fmt.Errorf("%w: %s", ErrStrategyNotFound, ver)
	}

	parsed, err := strategy.ParseBody(body)
	if err != nil {
		return "", "", false, fmt.Errorf("%w: %v", ErrParseFailed, err)
	}

	return ver, parsed.KeyID, true, nil
}
