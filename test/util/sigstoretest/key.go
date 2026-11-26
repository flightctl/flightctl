// Package sigstoretest creates throwaway keys for signing test images with
// sigstore signatures.
package sigstoretest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"

	"golang.org/x/crypto/nacl/secretbox"
	"golang.org/x/crypto/scrypt"
)

const (
	privateKeyPEMType = "ENCRYPTED COSIGN PRIVATE KEY"
	scryptN           = 65536
	scryptR           = 8
	scryptP           = 1
)

// Key is an ECDSA P-256 signing key pair.
type Key struct {
	// PrivateKeyPEM is the passphrase-encrypted private key in the format read
	// by podman push --sign-by-sigstore-private-key.
	PrivateKeyPEM []byte
	// Passphrase decrypts PrivateKeyPEM.
	Passphrase []byte
	// PublicKeyPEM is the PKIX public key.
	PublicKeyPEM []byte
}

// NewKey generates a key pair with a random passphrase.
func NewKey() (*Key, error) {
	private, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	privateDER, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		return nil, err
	}
	publicDER, err := x509.MarshalPKIXPublicKey(&private.PublicKey)
	if err != nil {
		return nil, err
	}
	secret := make([]byte, 16)
	if _, err := rand.Read(secret); err != nil {
		return nil, err
	}
	passphrase := []byte(hex.EncodeToString(secret))
	encrypted, err := encrypt(privateDER, passphrase)
	if err != nil {
		return nil, err
	}
	return &Key{
		PrivateKeyPEM: pem.EncodeToMemory(&pem.Block{Type: privateKeyPEMType, Bytes: encrypted}),
		Passphrase:    passphrase,
		PublicKeyPEM:  pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: publicDER}),
	}, nil
}

// KeyData returns the base64-encoded PEM public key, as carried by a keyed
// sigstoreSigned signature policy requirement.
func (k *Key) KeyData() string {
	return base64.StdEncoding.EncodeToString(k.PublicKeyPEM)
}

// envelope is the scrypt and nacl/secretbox encryption envelope used for
// sigstore private keys.
type envelope struct {
	KDF struct {
		Name   string `json:"name"`
		Params struct {
			N int `json:"N"`
			R int `json:"r"`
			P int `json:"p"`
		} `json:"params"`
		Salt []byte `json:"salt"`
	} `json:"kdf"`
	Cipher struct {
		Name  string `json:"name"`
		Nonce []byte `json:"nonce"`
	} `json:"cipher"`
	Ciphertext []byte `json:"ciphertext"`
}

func encrypt(plaintext, passphrase []byte) ([]byte, error) {
	var e envelope
	e.KDF.Name = "scrypt"
	e.KDF.Params.N, e.KDF.Params.R, e.KDF.Params.P = scryptN, scryptR, scryptP
	e.KDF.Salt = make([]byte, 32)
	if _, err := rand.Read(e.KDF.Salt); err != nil {
		return nil, err
	}
	derived, err := scrypt.Key(passphrase, e.KDF.Salt, scryptN, scryptR, scryptP, 32)
	if err != nil {
		return nil, fmt.Errorf("derive key encryption key: %w", err)
	}
	var key [32]byte
	copy(key[:], derived)
	var nonce [24]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	e.Cipher.Name = "nacl/secretbox"
	e.Cipher.Nonce = nonce[:]
	e.Ciphertext = secretbox.Seal(nil, plaintext, &nonce, &key)
	return json.Marshal(e)
}
