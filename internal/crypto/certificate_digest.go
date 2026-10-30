package crypto

import (
	"crypto/sha256"
	"encoding/hex"
)

func CertificateDigest(leafDER []byte) string {
	digest := sha256.Sum256(leafDER)
	return hex.EncodeToString(digest[:])
}
