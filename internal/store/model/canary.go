package model

import (
	"time"

	"github.com/flightctl/flightctl/internal/instrumentation/encryption"
)

// EncryptionCanary is a known-plaintext record used to verify that an encryption
// key can still decrypt data. One canary is stored per (strategy, keyID) pair;
// on startup every canary is decrypted and compared to the expected plaintext.
// A mismatch or decryption failure means the key material has changed or been
// lost, and the process refuses to start.
type EncryptionCanary struct {
	// Encryption strategy name, e.g. "v1".
	Strategy string `gorm:"primaryKey"`

	// Key identifier within the strategy, e.g. "default".
	KeyID string `gorm:"primaryKey;column:key_id"`

	// The canary plaintext encrypted with the corresponding key.
	EncryptedValue []byte `gorm:"not null"`

	CreatedAt time.Time
}

func (c *EncryptionCanary) ToEncryptionCanary() *encryption.Canary {
	return &encryption.Canary{
		Strategy:       c.Strategy,
		KeyID:          c.KeyID,
		EncryptedValue: c.EncryptedValue,
		CreatedAt:      c.CreatedAt,
	}
}

func EncryptionCanaryFrom(c *encryption.Canary) *EncryptionCanary {
	return &EncryptionCanary{
		Strategy:       c.Strategy,
		KeyID:          c.KeyID,
		EncryptedValue: c.EncryptedValue,
		CreatedAt:      c.CreatedAt,
	}
}
