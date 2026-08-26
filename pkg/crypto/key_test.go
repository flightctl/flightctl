package crypto

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"encoding/hex"
	"math/big"
	"testing"
)

// TestHashPublicKey_ECDSAGoldenValues pins hashECDSAKey's output for fixed P-256
// public keys. hashECDSAKey feeds into device identity (see
// internal/agent/identity.generateDeviceName), which is re-derived from a
// persisted key on every agent restart and compared against the CN embedded in
// existing certificates — so the hash algorithm must never change for a given
// key, even when its implementation is refactored (e.g. to avoid deprecated
// ecdsa.PublicKey.X/.Y access). These vectors must keep matching
// sha256(X.Bytes() || Y.Bytes()), the original, minimal (no leading zero
// bytes, no SEC1 prefix) big.Int encoding.
func TestHashPublicKey_ECDSAGoldenValues(t *testing.T) {
	curve := elliptic.P256()

	tests := []struct {
		name     string
		x, y     string // decimal
		wantHash string // hex
	}{
		{
			// Curve base point G: both coordinates are exactly 32 bytes (no
			// leading zero byte to trim).
			name:     "When both coordinates are full-width it should match the legacy X||Y hash",
			x:        curve.Params().Gx.String(),
			y:        curve.Params().Gy.String(),
			wantHash: "d875db7def232236aec738c6b0bb3e80142f5d0fd8f4df24fed6eef5cbb50d9f",
		},
		{
			// 43*G: Y's big-endian encoding is 31 bytes (leading zero byte),
			// exercising the trim-to-minimal-encoding path.
			name:     "When a coordinate has a leading zero byte it should match the legacy trimmed hash",
			x:        "68940400736695912148960890999721852106142292820061709409680925607912859161229",
			y:        "107423973961867739917642914452788810230878776317276661678353934116262542999",
			wantHash: "e54ccaf0b017f2583ccf644269203764d9a8839a945560d5739ca268756a6ebb",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			x, ok := new(big.Int).SetString(tt.x, 10)
			if !ok {
				t.Fatalf("invalid X coordinate: %s", tt.x)
			}
			y, ok := new(big.Int).SetString(tt.y, 10)
			if !ok {
				t.Fatalf("invalid Y coordinate: %s", tt.y)
			}
			pub := &ecdsa.PublicKey{Curve: curve, X: x, Y: y}

			want, err := hex.DecodeString(tt.wantHash)
			if err != nil {
				t.Fatalf("invalid want hash: %v", err)
			}

			got, err := HashPublicKey(pub)
			if err != nil {
				t.Fatalf("HashPublicKey returned error: %v", err)
			}
			if hex.EncodeToString(got) != hex.EncodeToString(want) {
				t.Fatalf("HashPublicKey(%s) = %x, want %x", tt.name, got, want)
			}
		})
	}
}
